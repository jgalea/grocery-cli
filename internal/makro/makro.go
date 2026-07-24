// Package makro is an adapter for Makro España (tienda.makro.es), the Spanish
// arm of METRO's cash-and-carry business. Reads work without an account: the
// search service answers anonymously, and so does the article service that
// carries the names and the full price breakdown. Category browsing on the site
// itself is behind a login, but the same category filter works on search.
//
// Two things make Makro different from a retail grocer, and both are handled
// here so a Makro line can sit next to a Mercadona one in a comparison:
//
//   - It quotes net prices. The site shows ex-VAT figures to its trade
//     customers; this adapter reports the gross price, which is what the same
//     basket costs anywhere else.
//   - It sells wholesale packs. "leche desnatada 1 L contiene 6 unidades" is six
//     litres, not one, so the per-unit price is computed off the pack total.
//
// Prices and availability are per depot. GROCERY_MAKRO_STORE selects one;
// the default is 00001.
package makro

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	base        = "https://tienda.makro.es"
	searchPath  = "/searchdiscover/articlesearch/search"
	catsPath    = "/searchdiscover/articlesearch/mainCategories"
	variantPath = "/evaluate.article.v1/betty-variants"

	country      = "ES"
	locale       = "es-ES"
	defaultStore = "00001"
	userAgent    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"

	// variantBatch caps how many article ids go into one detail request.
	variantBatch = 50
)

type Client struct {
	key     string
	storeID string
	http    *http.Client
	logf    func(string, ...any)
}

func New(key string, logf func(string, ...any)) *Client {
	s := strings.TrimSpace(os.Getenv("GROCERY_MAKRO_STORE"))
	if s == "" {
		s = defaultStore
	}
	return &Client{key: key, storeID: s, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.key }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

func (c *Client) get(u string, out any) error {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", locale)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

// searchResult is the article-search response. results carries the priced,
// store-specific part; the names live in the article service.
type searchResult struct {
	Amount    int      `json:"amount"`
	ResultIDs []string `json:"resultIds"`
	Results   map[string]struct {
		ID          string  `json:"id"`
		Price       float64 `json:"price"`
		IsAvailable bool    `json:"isAvailable"`
		Offers      []struct {
			Type string `json:"type"`
		} `json:"offers"`
	} `json:"results"`
}

// sellingPrice is one depot's price for one bundle. netPrice is what the site
// shows its trade customers; grossPrice is the same figure with VAT.
type sellingPrice struct {
	NetPrice   float64  `json:"netPrice"`
	GrossPrice float64  `json:"grossPrice"`
	FinalPrice float64  `json:"finalPrice"`
	ShelfPrice float64  `json:"shelfPrice"`
	KgNet      *float64 `json:"kgNet"`
	KgGross    *float64 `json:"kgGross"`
	Currency   string   `json:"currency"`
	Valid      bool     `json:"valid"`
}

type fulfillment struct {
	SellingPriceInfo sellingPrice `json:"sellingPriceInfo"`
}

type deliveryMode struct {
	PossibleFulfillmentTypes map[string]fulfillment `json:"possibleFulfillmentTypes"`
}

type bundleStore struct {
	PossibleDeliveryModes map[string]deliveryMode `json:"possibleDeliveryModes"`
}

type bundle struct {
	BundleID struct {
		BundleNumber string `json:"bundleNumber"`
	} `json:"bundleId"`
	Availability string                 `json:"availability"`
	Stores       map[string]bundleStore `json:"stores"`
}

type variant struct {
	BettyVariantID struct {
		ArticleNumber  string `json:"articleNumber"`
		VariantNumber  string `json:"variantNumber"`
		BettyVariantID string `json:"bettyVariantId"`
	} `json:"bettyVariantId"`
	Description  string            `json:"description"`
	Availability string            `json:"availability"`
	Bundles      map[string]bundle `json:"bundles"`
	Categories   []struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	} `json:"categories"`
}

type variantsResponse struct {
	Result map[string]struct {
		Variants map[string]variant `json:"variants"`
	} `json:"result"`
}

// price picks the depot price for a variant, preferring an in-store fulfilment
// and the lowest-numbered bundle so repeated runs report the same figure.
func (v variant) price(storeID string) (sellingPrice, string, bool) {
	nums := make([]string, 0, len(v.Bundles))
	for n := range v.Bundles {
		nums = append(nums, n)
	}
	sort.Strings(nums)
	for _, n := range nums {
		b := v.Bundles[n]
		st, ok := b.Stores[storeID]
		if !ok {
			continue
		}
		modes := make([]string, 0, len(st.PossibleDeliveryModes))
		for m := range st.PossibleDeliveryModes {
			modes = append(modes, m)
		}
		sort.Slice(modes, func(i, j int) bool {
			if (modes[i] == "STORE") != (modes[j] == "STORE") {
				return modes[i] == "STORE"
			}
			return modes[i] < modes[j]
		})
		for _, m := range modes {
			for _, ft := range []string{"STORE", "PICKUP", "PARCEL_DELIVERY"} {
				f, ok := st.PossibleDeliveryModes[m].PossibleFulfillmentTypes[ft]
				if ok && f.SellingPriceInfo.GrossPrice > 0 {
					return f.SellingPriceInfo, n, true
				}
			}
		}
	}
	return sellingPrice{}, "", false
}

func (c *Client) toHit(v variant, fallbackNet float64, available bool) store.Hit {
	desc := strings.TrimSpace(v.Description)
	h := store.Hit{
		ID:        v.BettyVariantID.BettyVariantID,
		Name:      packName(desc),
		Currency:  "EUR",
		Brand:     brandFrom(desc),
		Available: available && !strings.EqualFold(v.Availability, "NOT_AVAILABLE"),
	}
	if len(v.Categories) > 0 {
		name := v.Categories[0].Name
		if i := strings.LastIndex(name, " / "); i >= 0 {
			name = name[i+3:]
		}
		h.Category = strings.TrimSpace(name)
	}

	sp, bundleNo, ok := v.price(c.storeID)
	switch {
	case ok:
		h.Price = sp.GrossPrice
		h.URL = fmt.Sprintf("%s/shop/pv/%s/%s/%s", base, v.BettyVariantID.ArticleNumber,
			v.BettyVariantID.VariantNumber, bundleNo)
	default:
		// No bundle priced for this depot: the search service still gave a net
		// figure, which is better than nothing but is not VAT-inclusive.
		h.Price = fallbackNet
	}

	if ok && sp.KgGross != nil && *sp.KgGross > 0 {
		h.PricePerUnit = *sp.KgGross
		h.Unit = "kg"
	} else if ppu, unit, found := perUnit(h.Price, h.Name); found {
		h.PricePerUnit = ppu
		h.Unit = unit
	}
	return h
}

func (c *Client) search(q url.Values, limit int) ([]store.Hit, error) {
	rows := limit
	if rows <= 0 {
		rows = 24
	}
	if rows > 100 {
		rows = 100
	}
	q.Set("storeId", c.storeID)
	q.Set("language", locale)
	q.Set("country", country)
	q.Set("rows", strconv.Itoa(rows))
	q.Set("page", "1")

	var res searchResult
	if err := c.get(base+searchPath+"?"+q.Encode(), &res); err != nil {
		return nil, err
	}
	if len(res.ResultIDs) == 0 {
		return nil, nil
	}
	variants, err := c.variants(res.ResultIDs)
	if err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(res.ResultIDs))
	for _, id := range res.ResultIDs {
		v, ok := variants[id]
		if !ok {
			c.log("makro: no article detail for %s, skipping", id)
			continue
		}
		r := res.Results[id]
		out = append(out, c.toHit(v, r.Price, r.IsAvailable))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// variants resolves ids to article detail, keyed by the id that was asked for.
func (c *Client) variants(ids []string) (map[string]variant, error) {
	out := map[string]variant{}
	for start := 0; start < len(ids); start += variantBatch {
		end := start + variantBatch
		if end > len(ids) {
			end = len(ids)
		}
		q := url.Values{}
		q.Set("storeIds", c.storeID)
		q.Set("country", country)
		q.Set("locale", locale)
		for _, id := range ids[start:end] {
			q.Add("ids", id)
		}
		var resp variantsResponse
		if err := c.get(base+variantPath+"?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, art := range resp.Result {
			for _, v := range art.Variants {
				if id := v.BettyVariantID.BettyVariantID; id != "" {
					out[id] = v
				}
			}
		}
	}
	return out, nil
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	if eco {
		return nil, store.ErrUnsupported
	}
	return c.search(url.Values{"query": {term}}, limit)
}

// CategoryProducts takes the category's url path as Categories reports it
// ("frescos/carne/vacuno/ternera"); the leaf id on its own is not a filter.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	if eco {
		return nil, store.ErrUnsupported
	}
	return c.search(url.Values{"filter": {"category:" + categoryID}}, limit)
}

func (c *Client) Product(id string) (*store.Product, error) {
	variants, err := c.variants([]string{id})
	if err != nil {
		return nil, err
	}
	v, ok := variants[id]
	if !ok {
		return nil, fmt.Errorf("product %s: not found", id)
	}
	h := c.toHit(v, 0, true)
	p := &store.Product{Hit: h}
	if sp, _, found := v.price(c.storeID); found {
		p.OnSale = sp.ShelfPrice > 0 && sp.FinalPrice > 0 && sp.FinalPrice < sp.ShelfPrice
	}
	return p, nil
}

type mkCategory struct {
	Name            string                `json:"name"`
	DisplayName     string                `json:"displayName"`
	URLCategoryPath string                `json:"urlCategoryPath"`
	CategoryID      string                `json:"categoryId"`
	Children        map[string]mkCategory `json:"children"`
	Order           []string              `json:"order"`
}

func (c *Client) Categories(depth int) ([]store.Category, error) {
	q := url.Values{
		"storeId":  {c.storeID},
		"language": {locale},
		"country":  {country},
	}
	var resp struct {
		Children map[string]mkCategory `json:"children"`
		Order    []string              `json:"order"`
	}
	if err := c.get(base+catsPath+"?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return convert(resp.Children, depth), nil
}

// convert flattens Makro's keyed category map into the CLI's tree. The id is the
// url path, because that is what the search filter accepts.
func convert(in map[string]mkCategory, depth int) []store.Category {
	if depth == 0 || len(in) == 0 {
		return nil
	}
	out := make([]store.Category, 0, len(in))
	for _, c := range in {
		name := c.DisplayName
		if name == "" {
			name = c.Name
		}
		out = append(out, store.Category{
			ID:       c.URLCategoryPath,
			Name:     name,
			Children: convert(c.Children, depth-1),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

const unitAlt = `kg|kgs|g|gr|grs|l|lt|ltr|ml|cl`

var (
	sizeRE  = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(` + unitAlt + `)\b`)
	packRE  = regexp.MustCompile(`(?i)(?:(\d+)\s*[x×]\s*)?(\d+(?:[.,]\d+)?)\s*(` + unitAlt + `)\b`)
	brandRE = regexp.MustCompile(`^([\p{Lu}][\p{Lu}\d&.'’-]*(?:\s+[\p{Lu}][\p{Lu}\d&.'’-]*)*)\s`)

	// Makro states a pack in three ways: "contiene 6 unidades", "10 ud. x 1 Kg",
	// and a "(total 10 Kg)" restatement after either.
	containsRE = regexp.MustCompile(`(?i)\s*contiene\s+(\d+).*$`)
	udPerRE    = regexp.MustCompile(`(?i)(\d+)\s*ud\.?\s*[x×]\s*`)
	totalRE    = regexp.MustCompile(`(?i)\s*\(\s*(?:total\s*)?\d+(?:[.,]\d+)?\s*(?:` + unitAlt + `)\s*\)\s*$`)
)

// packName rewrites Makro's wholesale phrasing into the multipack notation the
// rest of the CLI reads: "leche entera 1 L contiene 6 unidades" and "Azúcar
// blanco 10 ud. x 1 Kg (total 10 Kg)" both become "... 6x1 L" / "... 10x1 Kg".
// Same pack, same price, stated the way a shelf label states it, which is also
// the only form the product matcher can size.
func packName(desc string) string {
	name := totalRE.ReplaceAllString(desc, "")
	name = udPerRE.ReplaceAllString(name, "${1}x")

	if m := containsRE.FindStringSubmatch(name); m != nil {
		head := strings.TrimSpace(containsRE.ReplaceAllString(name, ""))
		sizes := sizeRE.FindAllStringIndex(head, -1)
		if len(sizes) == 0 {
			return strings.TrimSpace(desc)
		}
		last := sizes[len(sizes)-1]
		size := strings.Join(strings.Fields(head[last[0]:last[1]]), " ")
		name = head[:last[0]] + m[1] + "x" + size + head[last[1]:]
	}
	if name = strings.TrimSpace(name); name == "" {
		return strings.TrimSpace(desc)
	}
	return name
}

// perUnit works out what a kilo or a litre costs, off the whole pack. A Makro
// line is routinely six or ten times the size of a retail one, so pricing it as
// if it were a single unit is how a wholesale basket looks falsely expensive.
// name must already be in shelf notation (see packName).
func perUnit(price float64, name string) (float64, string, bool) {
	if price <= 0 {
		return 0, "", false
	}
	all := packRE.FindAllStringSubmatch(name, -1)
	if len(all) == 0 {
		return 0, "", false
	}
	m := all[len(all)-1]
	n, err := strconv.ParseFloat(strings.ReplaceAll(m[2], ",", "."), 64)
	if err != nil || n <= 0 {
		return 0, "", false
	}
	packs := 1.0
	if m[1] != "" {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil && p > 0 {
			packs = p
		}
	}
	total := n * packs
	switch strings.ToLower(m[3]) {
	case "kg", "kgs":
		return price / total, "kg", true
	case "g", "gr", "grs":
		return price / (total / 1000), "kg", true
	case "l", "lt", "ltr":
		return price / total, "L", true
	case "ml":
		return price / (total / 1000), "L", true
	case "cl":
		return price / (total / 100), "L", true
	}
	return 0, "", false
}

// brandFrom reads the leading all-caps run Makro puts at the start of a
// description ("PRÉSIDENT leche desnatada 1 L").
func brandFrom(desc string) string {
	m := brandRE.FindStringSubmatch(desc)
	if m == nil {
		return ""
	}
	b := strings.TrimSpace(m[1])
	if len([]rune(b)) < 2 {
		return ""
	}
	return b
}
