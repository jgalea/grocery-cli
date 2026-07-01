// Package bonpreu is an adapter for Bonpreu i Esclat
// (compraonline.bonpreuesclat.cat). Its catalog reads run against the SPA's own
// webproductpagews REST API, but the site sits behind an AWS WAF that scores the
// TLS (JA3) fingerprint — a plain net/http client gets a 403 / empty prices — so
// every request goes out over a uTLS transport presenting Chrome's ClientHello
// (see transport.go). Anonymous reads work from any IP; cart writes need the
// user's own browser Cookie header (see cart.go).
package bonpreu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	baseURL   = "https://www.compraonline.bonpreuesclat.cat"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"
	// clientName mirrors the apollographql-client-name the SPA sends.
	clientName = "ecom-web"

	searchTermMax = 50  // the SPA truncates the query to 50 chars
	apiMaxPage    = 300 // the API rejects maxPageSize/maxProductsToDecorate > 300
	defaultPage   = 100
)

// Client is the Bonpreu adapter. It holds the uTLS-backed HTTP client, the
// per-instance config, an optional cached Cookie header (for cart writes) and a
// logf diagnostics hook.
type Client struct {
	key     string
	lang    string // "ca" or "es"; sent via accept-language
	baseURL string
	ua      string
	http    *http.Client
	logf    func(string, ...any)

	cookie string // cached raw Cookie header (cart writes); loaded lazily
}

// New returns a Bonpreu adapter. lang selects the accept-language ("ca" default,
// "es" supported). The HTTP client presents Chrome's TLS fingerprint via uTLS;
// if that fails to initialise it falls back to the stdlib transport (reads may
// then hit the WAF challenge).
func New(key, lang string, logf func(string, ...any)) *Client {
	if lang == "" {
		lang = "ca"
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	c := &Client{
		key:     key,
		lang:    lang,
		baseURL: baseURL,
		ua:      userAgent,
		http:    hc,
		logf:    logf,
	}
	if tr, err := newChromeTransport(); err == nil {
		hc.Transport = tr
	} else {
		c.log("uTLS Chrome fingerprint unavailable (%v) — using stdlib transport; requests may hit the WAF challenge", err)
	}
	return c
}

func (c *Client) Key() string { return c.key }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// newReq builds a request with the SPA's shared headers.
func (c *Client) newReq(method, u string, body any, extra map[string]string) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	if c.lang == "es" {
		req.Header.Set("accept-language", "es-ES,es;q=0.9,ca;q=0.8,en;q=0.7")
	} else {
		req.Header.Set("accept-language", "ca-ES,ca;q=0.9,es;q=0.8,en;q=0.7")
	}
	req.Header.Set("user-agent", c.ua)
	req.Header.Set("referer", c.baseURL+"/")
	req.Header.Set("origin", c.baseURL)
	req.Header.Set("apollographql-client-name", clientName)
	req.Header.Set("ecom-request-source", "web")
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	if c.cookie != "" {
		req.Header.Set("cookie", c.cookie)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	return req, nil
}

const maxBodyBytes = 16 << 20

// do sends req and returns the (capped) response body, mapping a non-2xx status
// to an error that carries the status code.
func (c *Client) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("bonpreu api: http %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body from %s: %w", req.URL, readErr)
	}
	return data, resp.StatusCode, nil
}

func (c *Client) getJSON(u string, out any) error {
	req, err := c.newReq("GET", u, nil, nil)
	if err != nil {
		return err
	}
	data, _, err := c.do(req)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s: %w", u, err)
		}
	}
	return nil
}

// getText fetches a URL as text (used to scrape SSR product pages for detail and
// the embedded CSRF token).
func (c *Client) getText(u string) (string, int, error) {
	req, err := c.newReq("GET", u, nil, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("accept", "text/html,application/xhtml+xml")
	data, status, err := c.do(req)
	if err != nil {
		return "", status, err
	}
	return string(data), status, nil
}

// --- catalog JSON shapes ---

type money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m money) float() float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(m.Amount), 64)
	return f
}

type unitPrice struct {
	Price    money  `json:"price"`
	Unit     string `json:"unit"`     // e.g. "fop.price.per.kg"
	UnitName string `json:"unitName"` // e.g. "PER_1KG", "EACH", "PER_1L"
}

type promotion struct {
	Description string `json:"description"`
}

type bpProduct struct {
	ProductID           string      `json:"productId"`         // stable UUID (cart uses this)
	RetailerProductID   string      `json:"retailerProductId"` // short human id shown on the site
	Name                string      `json:"name"`
	Brand               string      `json:"brand"`
	PackSizeDescription string      `json:"packSizeDescription"`
	Price               money       `json:"price"`
	UnitPrice           unitPrice   `json:"unitPrice"`
	Available           bool        `json:"available"`
	Promotions          []promotion `json:"promotions"`
	CategoryPath        []string    `json:"categoryPath"`
}

type productGroup struct {
	DecoratedProducts []bpProduct `json:"decoratedProducts"`
}

type productPage struct {
	ProductGroups []productGroup `json:"productGroups"`
}

func (pp *productPage) products() []bpProduct {
	var out []bpProduct
	for _, g := range pp.ProductGroups {
		out = append(out, g.DecoratedProducts...)
	}
	return out
}

// toHit maps a Bonpreu product to a store.Hit. ID is the short retailer id (the
// value the CLI passes back for product detail and cart writes).
func (c *Client) toHit(p bpProduct) store.Hit {
	h := store.Hit{
		ID:           p.RetailerProductID,
		Name:         strings.TrimSpace(p.Name),
		Price:        p.Price.float(),
		PricePerUnit: p.UnitPrice.Price.float(),
		Unit:         normUnit(p.UnitPrice.UnitName),
		Currency:     firstNonEmpty(p.Price.Currency, "EUR"),
		Brand:        strings.TrimSpace(p.Brand),
		Available:    p.Available,
		Eco:          isEco(p),
		URL:          c.baseURL + "/products/" + p.RetailerProductID,
	}
	if len(p.CategoryPath) > 0 {
		h.Category = p.CategoryPath[len(p.CategoryPath)-1]
	}
	return h
}

func pageSize(limit int) int {
	if limit <= 0 {
		return defaultPage
	}
	if limit > apiMaxPage {
		return apiMaxPage
	}
	return limit
}

func setListParams(q url.Values, n int) {
	q.Set("tag", "web")
	q.Set("maxProductsToDecorate", strconv.Itoa(n))
	q.Set("maxPageSize", strconv.Itoa(n))
}

// Search runs a full-text product search over the uTLS transport.
func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	n := pageSize(limit)
	if r := []rune(term); len(r) > searchTermMax {
		term = string(r[:searchTermMax])
	}
	q := url.Values{}
	q.Set("q", term)
	setListParams(q, n)
	var pp productPage
	u := c.baseURL + "/api/webproductpagews/v6/product-pages/search?" + q.Encode()
	if err := c.getJSON(u, &pp); err != nil {
		return nil, err
	}
	return c.collect(pp.products(), limit, eco), nil
}

// CategoryProducts lists a category's products. id may be the short retailer
// category code (plain digits, e.g. "0301") or the category UUID (hyphenated).
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	n := pageSize(limit)
	q := url.Values{}
	if strings.Contains(categoryID, "-") {
		q.Set("category", categoryID)
		q.Set("isRetailerCategoryId", "false")
	} else {
		q.Set("retailerCategoryId", categoryID)
	}
	setListParams(q, n)
	var pp productPage
	u := c.baseURL + "/api/webproductpagews/v6/product-pages?" + q.Encode()
	if err := c.getJSON(u, &pp); err != nil {
		return nil, err
	}
	return c.collect(pp.products(), limit, eco), nil
}

// collect maps products to hits, applies the eco filter, and clamps to limit.
func (c *Client) collect(ps []bpProduct, limit int, eco bool) []store.Hit {
	out := make([]store.Hit, 0, len(ps))
	for _, p := range ps {
		h := c.toHit(p)
		if eco && !h.Eco {
			continue
		}
		out = append(out, h)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// --- category tree ---

type bpCategory struct {
	CategoryID         string       `json:"categoryId"`
	RetailerCategoryID string       `json:"retailerCategoryId"`
	Name               string       `json:"name"`
	ChildCategories    []bpCategory `json:"childCategories"`
}

// Categories returns the catalog tree to the given depth (1 = top level).
func (c *Client) Categories(depth int) ([]store.Category, error) {
	if depth <= 0 {
		depth = 1
	}
	var cats []bpCategory
	u := fmt.Sprintf("%s/api/webproductpagews/v1/categories?decoration=false&categoryDepth=%d", c.baseURL, depth)
	if err := c.getJSON(u, &cats); err != nil {
		return nil, err
	}
	return mapCats(cats), nil
}

func mapCats(in []bpCategory) []store.Category {
	out := make([]store.Category, 0, len(in))
	for _, cat := range in {
		id := cat.RetailerCategoryID
		if id == "" {
			id = cat.CategoryID
		}
		out = append(out, store.Category{ID: id, Name: cat.Name, Children: mapCats(cat.ChildCategories)})
	}
	return out
}

// --- helpers ---

func normUnit(unitName string) string {
	u := strings.ToUpper(strings.TrimSpace(unitName))
	switch {
	case strings.Contains(u, "KG") || strings.Contains(u, "GRAM") || u == "PER_1G":
		return "kg"
	case strings.Contains(u, "LITRE") || strings.Contains(u, "LITER") || strings.Contains(u, "_1L") || u == "PER_1L":
		return "L"
	case u == "EACH" || strings.Contains(u, "UNIT") || strings.Contains(u, "EACH"):
		return "u"
	default:
		return ""
	}
}

func isEco(p bpProduct) bool {
	hay := strings.ToLower(p.Name + " " + p.Brand + " " + strings.Join(p.CategoryPath, " "))
	for _, kw := range []string{"ecològic", "ecologic", "ecológico", "eco ", " bio", "bio ", "organic"} {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
