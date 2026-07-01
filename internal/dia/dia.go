// Package dia is an adapter for DIA (dia.es), which runs its own open REST
// catalog API (search-back / plp-back microservices) on the storefront origin.
// Prices are the default-region (Madrid) prices; DIA fixes the region server-side.
package dia

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
	baseURL   = "https://www.dia.es"
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

type diaItem struct {
	ObjectID string `json:"object_id"`
	Name     string `json:"display_name"`
	Brand    string `json:"brand"`
	URL      string `json:"url"`
	Prices   struct {
		Price        float64 `json:"price"`
		PricePerUnit float64 `json:"price_per_unit"`
	} `json:"prices"`
}

func (c *Client) toHit(it diaItem) store.Hit {
	u := it.URL
	if u != "" && strings.HasPrefix(u, "/") {
		u = baseURL + u
	}
	return store.Hit{
		ID: it.ObjectID, Name: strings.TrimSpace(it.Name), Price: it.Prices.Price,
		PricePerUnit: it.Prices.PricePerUnit, Brand: it.Brand, Currency: "EUR",
		Available: true, URL: u,
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
		Items []diaItem `json:"search_items"`
	}
	if err := c.getJSON(baseURL+"/api/v1/search-back/search?q="+url.QueryEscape(term), &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Items))
	for _, it := range resp.Items {
		out = append(out, c.toHit(it))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	var resp struct {
		Items []diaItem `json:"products"`
	}
	if err := c.getJSON(baseURL+"/api/v1/plp-back/products?navigation="+url.QueryEscape(categoryID), &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Items))
	for _, it := range resp.Items {
		out = append(out, c.toHit(it))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Product / Categories aren't exposed as clean JSON for guests (detail is in the
// SSR page); search, batch and category listing work today.
func (c *Client) Product(id string) (*store.Product, error)      { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
