// Package sainsburys is an adapter for Sainsbury's (sainsburys.co.uk). It talks
// to the storefront's own gol-services REST API (the same calls the web SPA
// makes): product search under /product/v1/product and the basket under
// /basket/v2. Everything sits behind Akamai, which rejects requests that don't
// carry a logged-in browser session — so reads AND cart writes require the
// user's own Cookie header (see cart.go), pasted from a logged-in browser. The
// CLI fills the basket but never places an order.
//
// Ported from the TypeScript reference at github.com/abracadabra50/uk-grocery-cli
// (src/providers/sainsburys.ts). Built to the reference's request shapes;
// untested live (needs a real cookie).
package sainsburys

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	baseURL      = "https://www.sainsburys.co.uk/groceries-api/gol-services"
	siteURL      = "https://www.sainsburys.co.uk"
	userAgent    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
	defaultStore = "0560" // store_number; the reference's default, overridable via env
	defaultPage  = 24
)

// Client is the Sainsbury's adapter. It holds the HTTP client, the resolved
// store number, an optional cached Cookie header (loaded lazily) and a logf
// diagnostics hook.
type Client struct {
	key   string
	http  *http.Client
	logf  func(string, ...any)
	store string // store_number sent on basket calls

	cookie string // cached raw Cookie header; loaded lazily from disk
}

// New returns a Sainsbury's adapter. The store number defaults to the
// reference's "0560" and can be overridden with SAINSBURYS_STORE_NUMBER.
func New(key string, logf func(string, ...any)) *Client {
	sn := strings.TrimSpace(os.Getenv("SAINSBURYS_STORE_NUMBER"))
	if sn == "" {
		sn = defaultStore
	}
	return &Client{
		key:   key,
		http:  &http.Client{Timeout: 30 * time.Second},
		logf:  logf,
		store: sn,
	}
}

func (c *Client) Key() string { return c.key }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

const maxBodyBytes = 16 << 20

// newReq builds a request carrying the SPA's shared headers and the cached
// Cookie (plus the wcauthtoken extracted from it, which the basket API wants).
func (c *Client) newReq(method, u string, body any) (*http.Request, error) {
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
	req.Header.Set("accept-language", "en-GB,en;q=0.9")
	req.Header.Set("user-agent", userAgent)
	req.Header.Set("referer", siteURL+"/")
	req.Header.Set("origin", siteURL)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	if c.cookie != "" {
		req.Header.Set("cookie", c.cookie)
		if tok := c.wcAuthToken(); tok != "" {
			req.Header.Set("wcauthtoken", tok)
		}
	}
	return req, nil
}

// do sends req and returns the (capped) body, mapping a non-2xx status to an
// error carrying the status code.
func (c *Client) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("sainsburys api: http %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body from %s: %w", req.URL, readErr)
	}
	return data, resp.StatusCode, nil
}

func (c *Client) getJSON(u string, out any) error {
	if err := c.requireAuth(); err != nil {
		return err
	}
	req, err := c.newReq("GET", u, nil)
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

// --- catalog JSON shapes ---

// sbProduct mirrors the fields the gol-services product API returns. Only the
// ones the adapter maps are declared.
type sbProduct struct {
	ProductUID  string `json:"product_uid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	RetailPrice struct {
		Price float64 `json:"price"`
	} `json:"retail_price"`
	UnitPrice struct {
		Price   float64 `json:"price"`
		Measure string  `json:"measure"`
	} `json:"unit_price"`
	InStock     *bool  `json:"in_stock"`
	IsAvailable *bool  `json:"is_available"`
	Brand       string `json:"brand"`
	FullURL     string `json:"full_url"`
	Image       string `json:"image"`
}

// available follows the reference: available unless a flag is explicitly false.
func (p sbProduct) available() bool {
	if p.InStock != nil && !*p.InStock {
		return false
	}
	if p.IsAvailable != nil && !*p.IsAvailable {
		return false
	}
	return true
}

func (c *Client) toHit(p sbProduct) store.Hit {
	u := p.FullURL
	if u != "" && strings.HasPrefix(u, "/") {
		u = siteURL + u
	}
	return store.Hit{
		ID:           p.ProductUID,
		Name:         strings.TrimSpace(p.Name),
		Price:        p.RetailPrice.Price,
		PricePerUnit: p.UnitPrice.Price,
		Unit:         normUnit(p.UnitPrice.Measure),
		Currency:     "GBP",
		Brand:        strings.TrimSpace(p.Brand),
		Available:    p.available(),
		Eco:          isEco(p),
		URL:          u,
	}
}

func pageSize(limit int) int {
	if limit <= 0 {
		return defaultPage
	}
	return limit
}

// Search runs a full-text product search against the gol-services product API.
func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	n := pageSize(limit)
	q := url.Values{}
	q.Set("filter[keyword]", term)
	q.Set("page_number", "1")
	q.Set("page_size", strconv.Itoa(n))
	q.Set("sort_order", "FAVOURITES_FIRST")

	var resp struct {
		Products []sbProduct `json:"products"`
	}
	if err := c.getJSON(baseURL+"/product/v1/product?"+q.Encode(), &resp); err != nil {
		return nil, err
	}
	return c.collect(resp.Products, limit, eco), nil
}

// Product fetches full product detail for a single product_uid.
func (c *Client) Product(id string) (*store.Product, error) {
	var p sbProduct
	if err := c.getJSON(baseURL+"/product/v1/product/"+url.PathEscape(id), &p); err != nil {
		return nil, err
	}
	if p.ProductUID == "" {
		return nil, fmt.Errorf("product %s: not found", id)
	}
	prod := &store.Product{Hit: c.toHit(p)}
	prod.Ingredients = "" // detail endpoint fields beyond the shared shape aren't mapped
	return prod, nil
}

// CategoryProducts isn't exposed by the reference for Sainsbury's (its category
// browsing is a client-side facet over the same search endpoint).
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}

// Categories: the reference hits /product/categories/tree, but its response
// shape isn't documented well enough to map faithfully, so this stays
// unsupported rather than guessing a structure.
func (c *Client) Categories(depth int) ([]store.Category, error) {
	return nil, store.ErrUnsupported
}

// collect maps products to hits, applies the eco filter, and clamps to limit.
func (c *Client) collect(ps []sbProduct, limit int, eco bool) []store.Hit {
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

// --- helpers ---

func normUnit(measure string) string {
	m := strings.ToLower(strings.TrimSpace(measure))
	switch {
	case m == "":
		return ""
	case strings.Contains(m, "kg") || strings.Contains(m, "kilo") || m == "g" || strings.Contains(m, "gram"):
		return "kg"
	case m == "l" || strings.Contains(m, "litre") || strings.Contains(m, "liter") || strings.Contains(m, "cl") || strings.Contains(m, "ml"):
		return "L"
	case strings.Contains(m, "each") || strings.Contains(m, "unit") || m == "ea" || m == "u":
		return "u"
	default:
		return ""
	}
}

func isEco(p sbProduct) bool {
	hay := strings.ToLower(p.Name + " " + p.Brand)
	for _, kw := range []string{"organic", " bio", "bio ", "eco "} {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
