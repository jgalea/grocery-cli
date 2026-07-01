// Package morrisons is an adapter for Morrisons (groceries.morrisons.com), which
// exposes its storefront search behind an open product-page REST service. Prices
// are UK prices in GBP; Morrisons resolves the store/region server-side.
package morrisons

import (
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
	baseURL    = "https://groceries.morrisons.com"
	productURL = baseURL + "/products/"
	userAgent  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
)

type Client struct {
	key  string
	http *http.Client
	logf func(string, ...any)
}

func New(key string, logf func(string, ...any)) *Client {
	return &Client{key: key, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.key }

// amount is Morrisons' money shape; amounts arrive as JSON strings ("2.00").
type amount struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (a amount) float() float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(a.Amount), 64)
	return f
}

type morrItem struct {
	RetailerProductID string `json:"retailerProductId"`
	Name              string `json:"name"`
	Brand             string `json:"brand"`
	PackSize          string `json:"packSizeDescription"`
	Available         bool   `json:"available"`
	Price             amount `json:"price"`
	UnitPrice         struct {
		Price    amount `json:"price"`
		UnitName string `json:"unitName"`
	} `json:"unitPrice"`
}

func (c *Client) toHit(it morrItem) store.Hit {
	cur := it.Price.Currency
	if cur == "" {
		cur = "GBP"
	}
	return store.Hit{
		ID:           it.RetailerProductID,
		Name:         strings.TrimSpace(it.Name),
		Price:        it.Price.float(),
		PricePerUnit: it.UnitPrice.Price.float(),
		Unit:         normUnit(it.UnitPrice.UnitName),
		Currency:     cur,
		Brand:        it.Brand,
		Available:    it.Available,
		URL:          productURL + it.RetailerProductID,
	}
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

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	page := limit
	if page <= 0 {
		page = 40
	}
	u := fmt.Sprintf(
		"%s/api/webproductpagews/v6/product-pages/search?q=%s&tag=web&maxPageSize=%d&maxProductsToDecorate=%d&includeAdditionalPageInfo=true",
		baseURL, url.QueryEscape(term), page, page,
	)
	var resp struct {
		ProductGroups []struct {
			DecoratedProducts []morrItem `json:"decoratedProducts"`
		} `json:"productGroups"`
	}
	if err := c.getJSON(u, &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, page)
	for _, g := range resp.ProductGroups {
		for _, it := range g.DecoratedProducts {
			out = append(out, c.toHit(it))
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CategoryProducts, Product and Categories aren't wired to the Morrisons REST
// surface yet; search and batch work today.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Product(id string) (*store.Product, error)      { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }

// normUnit maps Morrisons unit tokens/names to the CLI's kg | L | u vocabulary.
func normUnit(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "kg", "g":
		return "kg"
	case "l", "ml", "cl":
		return "L"
	case "ea", "each", "unit", "u":
		return "u"
	}
	switch {
	case strings.Contains(s, "kg"), strings.Contains(s, "gram"):
		return "kg"
	case strings.Contains(s, "litre"), strings.Contains(s, "liter"):
		return "L"
	case strings.Contains(s, "each"), strings.Contains(s, "unit"):
		return "u"
	}
	return ""
}
