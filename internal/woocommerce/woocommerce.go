// Package woocommerce is a generic adapter for any store running the built-in
// WooCommerce Store API (wc/store/v1). Those endpoints serve read-only product,
// category and search data as JSON with no authentication, so a single
// config-driven client works against any WooCommerce shop. Prices come back as
// integer strings in the currency's minor units (e.g. "199" with minor_unit 2
// means 1.99), which this adapter normalises to decimal.
package woocommerce

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"

// Config points the client at one WooCommerce store. BaseURL is the site origin
// (no trailing slash needed, e.g. https://www.scotts.com.mt). Currency is the
// fallback ISO code used when a product's JSON omits currency_code.
type Config struct {
	Key      string
	BaseURL  string
	Currency string
}

type Client struct {
	cfg  Config
	http *http.Client
	logf func(string, ...any)
}

func New(cfg Config, logf func(string, ...any)) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.cfg.Key }

func (c *Client) storeAPI(path string) string {
	return c.cfg.BaseURL + "/wp-json/wc/store/v1" + path
}

// wcPrices mirrors the prices object on a Store API product. price and its
// siblings are integer strings in minor units; currency_minor_unit is the power
// of ten to divide by.
type wcPrices struct {
	Price        string `json:"price"`
	RegularPrice string `json:"regular_price"`
	SalePrice    string `json:"sale_price"`
	CurrencyCode string `json:"currency_code"`
	MinorUnit    int    `json:"currency_minor_unit"`
}

type wcCategoryRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type wcProduct struct {
	ID          int             `json:"id"`
	Name        string          `json:"name"`
	SKU         string          `json:"sku"`
	Permalink   string          `json:"permalink"`
	Description string          `json:"description"`
	OnSale      bool            `json:"on_sale"`
	Prices      wcPrices        `json:"prices"`
	IsInStock   bool            `json:"is_in_stock"`
	Categories  []wcCategoryRef `json:"categories"`
}

// minorToDecimal converts an integer-minor-units string ("199") to a decimal
// value (1.99) using the given minor-unit exponent.
func minorToDecimal(minor string, minorUnit int) float64 {
	minor = strings.TrimSpace(minor)
	if minor == "" {
		return 0
	}
	// Some stores may already return a decimal; handle both defensively.
	if strings.Contains(minor, ".") {
		f, _ := strconv.ParseFloat(minor, 64)
		return f
	}
	n, err := strconv.ParseInt(minor, 10, 64)
	if err != nil {
		return 0
	}
	if minorUnit <= 0 {
		return float64(n)
	}
	return float64(n) / math.Pow10(minorUnit)
}

func (c *Client) currencyOf(p wcPrices) string {
	if p.CurrencyCode != "" {
		return p.CurrencyCode
	}
	return c.cfg.Currency
}

func (c *Client) toHit(p wcProduct) store.Hit {
	h := store.Hit{
		ID:        strconv.Itoa(p.ID),
		Name:      strings.TrimSpace(p.Name),
		Price:     minorToDecimal(p.Prices.Price, p.Prices.MinorUnit),
		Currency:  c.currencyOf(p.Prices),
		Available: p.IsInStock,
		URL:       p.Permalink,
	}
	if len(p.Categories) > 0 {
		h.Category = p.Categories[0].Name
	}
	return h
}

func (c *Client) getJSON(u string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
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

func perPage(limit int) int {
	if limit > 0 {
		return limit
	}
	return 24
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	u := c.storeAPI("/products") + "?search=" + url.QueryEscape(term) +
		"&per_page=" + strconv.Itoa(perPage(limit))
	var items []wcProduct
	if err := c.getJSON(u, &items); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(items))
	for _, p := range items {
		out = append(out, c.toHit(p))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	u := c.storeAPI("/products") + "?category=" + url.QueryEscape(categoryID) +
		"&per_page=" + strconv.Itoa(perPage(limit))
	var items []wcProduct
	if err := c.getJSON(u, &items); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(items))
	for _, p := range items {
		out = append(out, c.toHit(p))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// looksLikeBarcode reports whether an SKU is plausibly an EAN/UPC barcode
// (all digits, 8/12/13/14 long) rather than an internal article number.
func looksLikeBarcode(sku string) bool {
	sku = strings.TrimSpace(sku)
	switch len(sku) {
	case 8, 12, 13, 14:
	default:
		return false
	}
	for _, r := range sku {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (c *Client) Product(id string) (*store.Product, error) {
	var p wcProduct
	if err := c.getJSON(c.storeAPI("/products/"+url.PathEscape(id)), &p); err != nil {
		return nil, err
	}
	prod := &store.Product{
		Hit:    c.toHit(p),
		OnSale: p.OnSale,
	}
	if looksLikeBarcode(p.SKU) {
		prod.EAN = p.SKU
	}
	return prod, nil
}

type wcCategory struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Parent int    `json:"parent"`
}

// Categories fetches the flat category list (paginating) and assembles it into a
// tree. depth <= 0 returns the full tree; depth 1 returns top-level nodes only,
// depth 2 their children, and so on.
func (c *Client) Categories(depth int) ([]store.Category, error) {
	var all []wcCategory
	for page := 1; page <= 50; page++ {
		u := c.storeAPI("/products/categories") + "?per_page=100&page=" + strconv.Itoa(page)
		var batch []wcCategory
		if err := c.getJSON(u, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}

	return buildTree(all, 0, depth, 1), nil
}

// buildTree recursively assembles categories whose parent is parentID. Roots are
// the categories whose parent is 0 (WooCommerce's "no parent" sentinel).
func buildTree(all []wcCategory, parentID, maxDepth, level int) []store.Category {
	if maxDepth > 0 && level > maxDepth {
		return nil
	}
	var out []store.Category
	for _, cat := range all {
		if cat.Parent != parentID {
			continue
		}
		node := store.Category{ID: strconv.Itoa(cat.ID), Name: cat.Name}
		node.Children = buildTree(all, cat.ID, maxDepth, level+1)
		out = append(out, node)
	}
	return out
}
