// Package gadis is an adapter for Gadis (gadisline.com), the online shop of the
// Galician chain Gadisa. The storefront is a Next.js app in front of a set of
// public microservices; this talks to the catalog service directly rather than
// scraping the rendered page.
//
// Every catalog call needs a site-id and a store-id header. Both are published
// by the site service for the storefront domain, so the client bootstraps them
// on first use. The store id fixes which assortment (and therefore which
// prices) you see; GROCERY_GADIS_STORE overrides the default one.
package gadis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	siteBase    = "https://site.gadisline.com/api/v3"
	catalogBase = "https://catalog.gadisline.com/api/v3"
	shopBase    = "https://www.gadisline.com"
	domain      = "www.gadisline.com"
	userAgent   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"

	// ecoProperty is the property code Gadis puts on organic items ("Ecológico").
	ecoProperty = "36"
)

type Client struct {
	key  string
	lang string // ES | GL, sent as accept-language
	http *http.Client
	logf func(string, ...any)

	once            sync.Once
	siteID, storeID string
	bootErr         error
}

func New(key, lang string, logf func(string, ...any)) *Client {
	l := strings.ToUpper(strings.TrimSpace(lang))
	if l != "GL" {
		l = "ES"
	}
	return &Client{key: key, lang: l, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.key }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// ids resolves the site and store ids, once per client.
func (c *Client) ids() (siteID, storeID string, err error) {
	c.once.Do(func() {
		var resp struct {
			Elements []struct {
				ID                     string `json:"id"`
				DefaultAssortmentStore string `json:"default_assortment_store"`
			} `json:"elements"`
		}
		if err := c.do(http.MethodGet, siteBase+"/sites?domain="+domain, nil, &resp); err != nil {
			c.bootErr = fmt.Errorf("gadis: site lookup: %w", err)
			return
		}
		if len(resp.Elements) == 0 {
			c.bootErr = fmt.Errorf("gadis: site lookup returned no site for %s", domain)
			return
		}
		c.siteID = resp.Elements[0].ID
		c.storeID = resp.Elements[0].DefaultAssortmentStore
		if v := strings.TrimSpace(os.Getenv("GROCERY_GADIS_STORE")); v != "" {
			c.storeID = v
			c.log("gadis: using store %s from GROCERY_GADIS_STORE", v)
		}
		if c.siteID == "" || c.storeID == "" {
			c.bootErr = fmt.Errorf("gadis: site lookup returned no site/store id")
		}
	})
	return c.siteID, c.storeID, c.bootErr
}

// do issues a request and decodes the JSON body into out. Catalog calls need the
// id headers; the site lookup that discovers them does not, so they are only
// attached once known.
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
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("accept-language", c.lang)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.siteID != "" {
		req.Header.Set("site-id", c.siteID)
	}
	if c.storeID != "" {
		req.Header.Set("store-id", c.storeID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

// translated is Gadis' repeated shape for localised text: a list of
// {language, value}. Product detail flattens the same fields to a plain string,
// so both forms have to decode.
type translated []struct {
	Language string `json:"language"`
	Value    string `json:"value"`
}

type text struct {
	list translated
	flat string
}

func (t *text) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &t.flat)
	}
	return json.Unmarshal(b, &t.list)
}

func (t text) value(lang string) string {
	if t.flat != "" {
		return t.flat
	}
	for _, e := range t.list {
		if strings.EqualFold(e.Language, lang) {
			return e.Value
		}
	}
	if len(t.list) > 0 {
		return t.list[0].Value
	}
	return ""
}

type gProduct struct {
	ID                  string  `json:"id"`
	CommercialDesc      text    `json:"commercial_description"`
	ProductCode         int64   `json:"product_code"`
	Price               float64 `json:"price"`
	PriceKiloLitre      float64 `json:"price_kilo_litre"`
	PriceKiloLitreSuffx text    `json:"price_kilo_litre_suffix"`
	Slug                string  `json:"slug"`
	Brand               string  `json:"brand_description"`
	Categories          []struct {
		ID    string `json:"id"`
		Name  text   `json:"name"`
		Descs text   `json:"descriptions_translate"`
		Level int    `json:"level"`
	} `json:"categories"`
	Properties []struct {
		Code string `json:"property_code"`
		Desc text   `json:"description"`
	} `json:"properties"`
	Offers          []json.RawMessage `json:"offers"`
	AecocProperties []struct {
		Code    string `json:"code"`
		Details []struct {
			Language string `json:"language"`
			Value    string `json:"value"`
		} `json:"details"`
	} `json:"aecoc_properties"`
}

func (p gProduct) eco() bool {
	for _, pr := range p.Properties {
		if pr.Code == ecoProperty {
			return true
		}
	}
	return false
}

// category returns the deepest category name, which is the useful one.
func (p gProduct) category(lang string) string {
	var name string
	deepest := -1
	for _, c := range p.Categories {
		if c.Level >= deepest {
			deepest = c.Level
			if v := c.Name.value(lang); v != "" {
				name = v
			} else {
				name = c.Descs.value(lang)
			}
		}
	}
	return name
}

func (c *Client) toHit(p gProduct) store.Hit {
	u := ""
	if p.Slug != "" {
		u = shopBase + p.Slug
	}
	return store.Hit{
		ID:           p.ID,
		Name:         strings.TrimSpace(p.CommercialDesc.value(c.lang)),
		Price:        p.Price,
		PricePerUnit: p.PriceKiloLitre,
		Unit:         unitFrom(p.PriceKiloLitreSuffx.value(c.lang)),
		Currency:     "EUR",
		Brand:        strings.TrimSpace(p.Brand),
		Category:     p.category(c.lang),
		Eco:          p.eco(),
		Available:    true,
		URL:          u,
	}
}

// searchBody is the catalog search payload. minimum_should_match mirrors the
// storefront, which sends 1 so that any query token can match.
type searchBody struct {
	SearchTerm         string   `json:"search_term,omitempty"`
	MinimumShouldMatch int      `json:"minimum_should_match"`
	CategoryIDs        []string `json:"category_ids,omitempty"`
}

// search runs one catalog search and returns hits, filtering to organic items
// when eco is set (the API has no eco facet, the flag is a product property).
func (c *Client) search(body searchBody, limit int, eco bool) ([]store.Hit, error) {
	if _, _, err := c.ids(); err != nil {
		return nil, err
	}
	rows := limit
	if rows <= 0 {
		rows = 24
	}
	if eco {
		// Organic items are a small slice of any result set, so ask for more
		// rows than the caller wants and cut back after filtering.
		rows *= 4
	}
	if rows > 100 {
		rows = 100
	}
	q := url.Values{
		"page_number":   {"0"},
		"rows_per_page": {fmt.Sprint(rows)},
		"keep_request":  {"true"},
	}
	body.MinimumShouldMatch = 1

	var resp struct {
		Elements []gProduct `json:"elements"`
	}
	if err := c.do(http.MethodPost, catalogBase+"/catalog/products/search?"+q.Encode(), body, &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Elements))
	for _, p := range resp.Elements {
		if eco && !p.eco() {
			continue
		}
		out = append(out, c.toHit(p))
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	return c.search(searchBody{SearchTerm: term}, limit, eco)
}

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return c.search(searchBody{CategoryIDs: []string{categoryID}}, limit, eco)
}

// Product reads full detail. The catalog service exposes it under the product's
// own /search path; a plain GET on /catalog/products/<id> is not a route.
func (c *Client) Product(id string) (*store.Product, error) {
	if _, _, err := c.ids(); err != nil {
		return nil, err
	}
	var p gProduct
	if err := c.do(http.MethodGet, catalogBase+"/catalog/products/"+url.PathEscape(id)+"/search", nil, &p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, fmt.Errorf("product %s: not found", id)
	}
	out := &store.Product{Hit: c.toHit(p)}
	for _, a := range p.AecocProperties {
		v := c.aecocValue(a.Details)
		if v == "" {
			continue
		}
		switch a.Code {
		case "ORIGE":
			out.Origin = v
		case "INFIN":
			out.Ingredients = v
		case "INFNU":
			out.Nutrients = v
		case "INFCO":
			out.Conservation = v
		}
	}
	out.OnSale = len(p.Offers) > 0
	return out, nil
}

func (c *Client) aecocValue(details []struct {
	Language string `json:"language"`
	Value    string `json:"value"`
}) string {
	for _, d := range details {
		if strings.EqualFold(d.Language, c.lang) {
			return cleanHTML(d.Value)
		}
	}
	if len(details) > 0 {
		return cleanHTML(details[0].Value)
	}
	return ""
}

type gCategory struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Nested struct {
		Categories []gCategory `json:"categories"`
	} `json:"nested_categories"`
}

func (c *Client) Categories(depth int) ([]store.Category, error) {
	if _, _, err := c.ids(); err != nil {
		return nil, err
	}
	var resp struct {
		Categories []gCategory `json:"categories"`
	}
	if err := c.do(http.MethodGet, catalogBase+"/catalog/categories", nil, &resp); err != nil {
		return nil, err
	}
	return convertCategories(resp.Categories, depth), nil
}

func convertCategories(in []gCategory, depth int) []store.Category {
	if depth == 0 {
		return nil
	}
	out := make([]store.Category, 0, len(in))
	for _, c := range in {
		out = append(out, store.Category{
			ID:       c.ID,
			Name:     c.Name,
			Children: convertCategories(c.Nested.Categories, depth-1),
		})
	}
	return out
}

var (
	brRe  = regexp.MustCompile(`(?i)<br\s*/?>`)
	tagRe = regexp.MustCompile(`(?s)<[^>]*>`)
	// Long info fields come back cut off mid-tag ("...4,6 g)<BR"), so a dangling
	// opening bracket at the end is stripped too.
	danglingRe = regexp.MustCompile(`<[^>]*$`)
)

// cleanHTML flattens the small HTML fragments Gadis stores in product info.
func cleanHTML(s string) string {
	s = brRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = danglingRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return strings.TrimSpace(s)
}

// unitFrom turns the price suffix ("el kilo", "o litro", "la unidad") into
// kg | L | u. Both Spanish and Galician wording appear.
func unitFrom(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "kilo"):
		return "kg"
	case strings.Contains(s, "litro"):
		return "L"
	case strings.Contains(s, "unidad"), strings.Contains(s, "unidade"):
		return "u"
	}
	return ""
}
