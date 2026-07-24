// Package aldi is an adapter for ALDI España (aldi.es). The storefront is a
// Next.js app that reads its catalog from a hosted Algolia index, so this talks
// to Algolia directly with the public search-only credentials the site ships.
//
// ALDI does not sell online in Spain, so this store is read-only: prices, sizes
// and unit prices for search, category browsing and comparison, but no cart.
//
// Prices are per region, and so is the index. GROCERY_ALDI_REGION selects
// pen (peninsula, the default), can (Canarias) or bal (Baleares).
package aldi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

// Public client-side Algolia credentials from the ALDI storefront bundle. They
// are search-only and can rotate; refetch from the site if requests start
// failing.
const (
	algoliaAppID  = "L9KNU74IO7"
	algoliaAPIKey = "83df5acd172c42ab174afa4583232b5d"

	// indexFmt takes the region code.
	indexFmt = "an_prd_es_es_%s_products2"

	shopBase  = "https://www.aldi.es"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"

	// ecoCategory is the category ALDI files its organic range under.
	ecoCategory = "bio"
)

var regions = map[string]bool{"pen": true, "can": true, "bal": true}

type Client struct {
	key   string
	index string
	http  *http.Client
	logf  func(string, ...any)
}

func New(key string, logf func(string, ...any)) *Client {
	region := strings.ToLower(strings.TrimSpace(os.Getenv("GROCERY_ALDI_REGION")))
	if !regions[region] {
		region = "pen"
	}
	return &Client{
		key:   key,
		index: fmt.Sprintf(indexFmt, region),
		http:  &http.Client{Timeout: 30 * time.Second},
		logf:  logf,
	}
}

func (c *Client) Key() string { return c.key }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

type aldiItem struct {
	ObjectID     string   `json:"objectID"`
	Name         string   `json:"name"`
	BrandName    string   `json:"brandName"`
	ProductSlug  string   `json:"productSlug"`
	SalesUnit    string   `json:"salesUnit"`
	IsAvailable  bool     `json:"isAvailable"`
	CategoryIDs  []string `json:"categoryIDs"`
	CurrentPrice struct {
		PriceValue float64 `json:"priceValue"`
		BasePrice  []struct {
			BasePriceValue float64 `json:"basePriceValue"`
			BasePriceScale string  `json:"basePriceScale"`
		} `json:"basePrice"`
	} `json:"currentPrice"`
	HierarchicalCategories struct {
		Lvl0 []string `json:"lvl0"`
		Lvl1 []string `json:"lvl1"`
	} `json:"hierarchicalCategories"`

	// Detail-only fields.
	ShortDescription string `json:"shortDescription"`
	LongDescription  string `json:"longDescription"`
}

// eco reports whether an item is organic. The index has no eco facet: ALDI's
// "bio" category only covers part of the range, and the rest is recognisable
// from its organic private labels (BIO, GUTBIO) or from the name.
func (it aldiItem) eco() bool {
	for _, c := range it.CategoryIDs {
		if c == ecoCategory {
			return true
		}
	}
	if strings.Contains(strings.ToUpper(it.BrandName), "BIO") {
		return true
	}
	return ecoNameRE.MatchString(it.Name)
}

var ecoNameRE = regexp.MustCompile(`(?i)ecol[oó]gic`)

// category returns the deepest hierarchical category name, dropping the "Marcas"
// (brands) tree, which duplicates every product under its own brand.
func (it aldiItem) category() string {
	for _, v := range it.HierarchicalCategories.Lvl1 {
		if !strings.HasPrefix(v, "Marcas > ") {
			return strings.TrimSpace(v[strings.LastIndex(v, " > ")+3:])
		}
	}
	for _, v := range it.HierarchicalCategories.Lvl0 {
		if v != "Marcas" {
			return v
		}
	}
	return ""
}

// salesUnitRE pulls the package size out of ALDI's salesUnit ("1 l unidad",
// "340 g unidad", "6 x 125 g unidad").
var salesUnitRE = regexp.MustCompile(`(?i)^\s*(\d+(?:[.,]\d+)?\s*(?:x\s*\d+(?:[.,]\d+)?\s*)?[a-zµ]+)`)

// name folds the package size into the product name. ALDI keeps them apart
// ("Leche evaporada" + "340 g unidad"), but every matcher in the CLI reads the
// size off the name, and so does a shopper comparing two stores.
func (it aldiItem) name() string {
	n := strings.TrimSpace(it.Name)
	m := salesUnitRE.FindStringSubmatch(it.SalesUnit)
	if m == nil {
		return n
	}
	size := strings.Join(strings.Fields(m[1]), " ")
	if size == "" || strings.Contains(strings.ToLower(n), strings.ToLower(size)) {
		return n
	}
	return n + " " + size
}

func (c *Client) toHit(it aldiItem) store.Hit {
	h := store.Hit{
		ID:        it.ObjectID,
		Name:      it.name(),
		Price:     it.CurrentPrice.PriceValue,
		Currency:  "EUR",
		Brand:     strings.TrimSpace(it.BrandName),
		Category:  it.category(),
		Eco:       it.eco(),
		Available: it.IsAvailable,
	}
	if len(it.CurrentPrice.BasePrice) > 0 {
		b := it.CurrentPrice.BasePrice[0]
		h.PricePerUnit = b.BasePriceValue
		h.Unit = unitFrom(b.BasePriceScale)
	}
	if it.ProductSlug != "" {
		h.URL = shopBase + "/producto/" + it.ProductSlug + ".html"
	}
	return h
}

func (c *Client) do(method, u string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-Algolia-Application-Id", algoliaAppID)
	req.Header.Set("X-Algolia-API-Key", algoliaAPIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
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

type queryResult struct {
	Hits   []aldiItem                `json:"hits"`
	NbHits int                       `json:"nbHits"`
	Facets map[string]map[string]int `json:"facets"`
}

func (c *Client) query(body map[string]any) (*queryResult, error) {
	u := fmt.Sprintf("https://%s-dsn.algolia.net/1/indexes/%s/query", algoliaAppID, c.index)
	var out queryResult
	if err := c.do(http.MethodPost, u, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// hits runs one query and converts the results. Organic items are a slice of any
// result set and the index cannot filter on them, so eco asks for more rows than
// the caller wants and drops the rest after filtering.
func (c *Client) hits(body map[string]any, limit int, eco bool) ([]store.Hit, error) {
	if eco {
		if hpp, ok := body["hitsPerPage"].(int); ok {
			body["hitsPerPage"] = min(hpp*8, 1000)
		}
	}
	res, err := c.query(body)
	if err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(res.Hits))
	for _, it := range res.Hits {
		if eco && !it.eco() {
			continue
		}
		out = append(out, c.toHit(it))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	hpp := limit
	if hpp <= 0 {
		hpp = 24
	}
	return c.hits(map[string]any{
		"query":       term,
		"hitsPerPage": hpp,
		// The index matches every query word by default, so a size in the term
		// ("aceite de oliva 1 L") returns nothing at all: no record has "1 L" in
		// its text, ALDI keeps the size in a separate field. Dropping trailing
		// words until something matches is what the storefront's own search box
		// does, and it is the difference between a hit and a false "not stocked".
		"removeWordsIfNoResults": "lastWords",
	}, limit, eco)
}

// CategoryProducts accepts either a category slug as it appears in categoryIDs
// ("yogures-y-postres-lacteos") or a display path as Categories returns it
// ("Lácteos y huevos > Leche y bebidas vegetales").
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	hpp := limit
	if hpp <= 0 {
		hpp = 50
	}
	body := map[string]any{"query": "", "hitsPerPage": hpp, "filters": categoryFilter(categoryID)}
	return c.hits(body, limit, eco)
}

func categoryFilter(id string) string {
	switch {
	case strings.Contains(id, " > "):
		return fmt.Sprintf("hierarchicalCategories.lvl1:%q", id)
	case strings.ContainsAny(id, " "):
		return fmt.Sprintf("hierarchicalCategories.lvl0:%q", id)
	default:
		return "categoryIDs:" + id
	}
}

func (c *Client) Product(id string) (*store.Product, error) {
	u := fmt.Sprintf("https://%s-dsn.algolia.net/1/indexes/%s/%s", algoliaAppID, c.index, id)
	var it aldiItem
	if err := c.do(http.MethodGet, u, nil, &it); err != nil {
		return nil, err
	}
	if it.ObjectID == "" {
		return nil, fmt.Errorf("product %s: not found", id)
	}
	// shortDescription is a free-text blurb ("Sin lactosa. Origen nacional.")
	// and longDescription is usually a pricing disclaimer, so neither maps onto
	// a specific detail field; the blurb goes where the CLI shows product info.
	return &store.Product{
		Hit:         c.toHit(it),
		Ingredients: strings.TrimSpace(it.ShortDescription),
	}, nil
}

// Categories builds the tree from the index's own category facets, so the names
// and the counts are always what the site is currently serving.
func (c *Client) Categories(depth int) ([]store.Category, error) {
	if depth == 0 {
		return nil, nil
	}
	res, err := c.query(map[string]any{
		"query":             "",
		"hitsPerPage":       0,
		"facets":            []string{"hierarchicalCategories.lvl0", "hierarchicalCategories.lvl1"},
		"maxValuesPerFacet": 1000,
	})
	if err != nil {
		return nil, err
	}
	tops := make([]string, 0, len(res.Facets["hierarchicalCategories.lvl0"]))
	for name := range res.Facets["hierarchicalCategories.lvl0"] {
		tops = append(tops, name)
	}
	sort.Strings(tops)

	children := map[string][]store.Category{}
	for path := range res.Facets["hierarchicalCategories.lvl1"] {
		i := strings.LastIndex(path, " > ")
		if i < 0 {
			continue
		}
		parent := path[:i]
		children[parent] = append(children[parent], store.Category{ID: path, Name: path[i+3:]})
	}

	out := make([]store.Category, 0, len(tops))
	for _, name := range tops {
		cat := store.Category{ID: name, Name: name}
		if depth != 1 {
			kids := children[name]
			sort.Slice(kids, func(i, j int) bool { return kids[i].Name < kids[j].Name })
			cat.Children = kids
		}
		out = append(out, cat)
	}
	c.log("aldi: %d top categories from %s", len(out), c.index)
	return out, nil
}

// unitFrom normalises ALDI's base-price scale ("kg", "l", "100 g") to the
// CLI's kg | L | u.
func unitFrom(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "kg":
		return "kg"
	case "l":
		return "L"
	case "stk", "u", "ud", "unidad":
		return "u"
	}
	return ""
}
