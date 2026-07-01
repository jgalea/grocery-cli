// Package consum is an adapter for Consum (tienda.consum.es), which runs the
// Aktios "Tol" e-commerce platform with an open REST catalog API on the
// storefront origin. Prices are the default-region prices.
package consum

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	baseURL   = "https://tienda.consum.es"
	apiBase   = "/api/rest/V1.0/catalog/product"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
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

// cProduct is one product. priceData.prices[].value.centAmount is mis-named — it
// actually holds the euro price (e.g. 0.96), not cents.
type cProduct struct {
	ID          json.Number `json:"id"`
	EAN         string      `json:"ean"`
	ProductData struct {
		Name string `json:"name"`
	} `json:"productData"`
	PriceData struct {
		Prices []struct {
			Value struct {
				CentAmount float64 `json:"centAmount"`
			} `json:"value"`
		} `json:"prices"`
		UnitPriceUnitType string `json:"unitPriceUnitType"`
	} `json:"priceData"`
}

func (p cProduct) price() float64 {
	if len(p.PriceData.Prices) > 0 {
		return p.PriceData.Prices[0].Value.CentAmount
	}
	return 0
}

func (c *Client) toHit(p cProduct) store.Hit {
	return store.Hit{
		ID: p.ID.String(), Name: strings.TrimSpace(p.ProductData.Name), Price: p.price(),
		Unit: unitFrom(p.PriceData.UnitPriceUnitType), Currency: "EUR", Available: true,
		URL: baseURL + "/producto/" + p.ID.String(),
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	var resp struct {
		Products []cProduct `json:"products"`
	}
	if err := c.getJSON(baseURL+apiBase+"?q="+url.QueryEscape(term), &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Products))
	for _, p := range resp.Products {
		out = append(out, c.toHit(p))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *Client) Product(id string) (*store.Product, error) {
	var p cProduct
	if err := c.getJSON(baseURL+apiBase+"/"+url.PathEscape(id), &p); err != nil {
		return nil, err
	}
	if p.ID.String() == "" {
		return nil, fmt.Errorf("product %s: not found", id)
	}
	h := c.toHit(p)
	return &store.Product{Hit: h, EAN: p.EAN}, nil
}

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }

// unitFrom turns "1 L" / "100 g" / "1 ud" into kg | L | u.
func unitFrom(s string) string {
	f := strings.Fields(strings.ToLower(s))
	if len(f) == 0 {
		return ""
	}
	switch f[len(f)-1] {
	case "kg", "g":
		return "kg"
	case "l", "ml", "cl":
		return "L"
	case "ud", "uds", "u", "unidad":
		return "u"
	}
	return ""
}
