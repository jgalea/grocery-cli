// Package pavipama is an adapter for PAVI/PAMA (pavipama.com.mt), the Maltese
// supermarket chain, which exposes an open JSON ecommerce API on its storefront
// origin. Prices are in EUR and no authentication is required.
package pavipama

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
	baseURL   = "https://pavipama.com.mt/api"
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

// paviItem is one product row in the ecommerce/products response.
type paviItem struct {
	ID          string            `json:"id"`
	Barcode     string            `json:"barcode"`
	Description string            `json:"description"`
	Brand       string            `json:"brand"`
	Category    string            `json:"categoryDescription"`
	Price       float64           `json:"price"`
	NetPrice    float64           `json:"netPrice"`
	PricePerUm  float64           `json:"pricePerUm"`
	UmPerUm     string            `json:"umPerUm"`
	ImageURL    string            `json:"imageUrl"`
	Available   bool              `json:"available"`
	Promotions  []json.RawMessage `json:"promotions"`
}

func (c *Client) toHit(it paviItem) store.Hit {
	// Prefer the barcode as the id: it's what the cart API keys on, so search
	// results feed straight into `cart add`.
	id := it.Barcode
	if id == "" {
		id = it.ID
	}
	return store.Hit{
		ID:           id,
		Name:         strings.TrimSpace(it.Description),
		Price:        it.Price,
		PricePerUnit: it.PricePerUm,
		Unit:         strings.TrimSpace(it.UmPerUm),
		Currency:     "EUR",
		Brand:        strings.TrimSpace(it.Brand),
		Category:     strings.TrimSpace(it.Category),
		Available:    it.Available,
		URL:          it.ImageURL,
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
	var resp struct {
		Data []paviItem `json:"data"`
	}
	u := baseURL + "/cli/ecommerce/products?store=&q=" + url.QueryEscape(term) +
		"&p=0&category=&onlyPromotions=false&onlyBranded=false&tag="
	if err := c.getJSON(u, &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Data))
	for _, it := range resp.Data {
		out = append(out, c.toHit(it))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	var resp struct {
		Data []paviItem `json:"data"`
	}
	u := baseURL + "/cli/ecommerce/products?store=&q=&p=0&category=" + url.QueryEscape(categoryID) +
		"&onlyPromotions=false&onlyBranded=false&tag="
	if err := c.getJSON(u, &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Data))
	for _, it := range resp.Data {
		out = append(out, c.toHit(it))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// paviCategory is a node in the /cli/categories response. Top-level nodes carry
// their subcategories in Items.
type paviCategory struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Items       []paviCategory `json:"items"`
}

func (c *Client) Categories(depth int) ([]store.Category, error) {
	var resp struct {
		Data []paviCategory `json:"data"`
	}
	if err := c.getJSON(baseURL+"/cli/categories", &resp); err != nil {
		return nil, err
	}
	out := make([]store.Category, 0, len(resp.Data))
	for _, cat := range resp.Data {
		out = append(out, toCategory(cat, depth, 1))
	}
	return out, nil
}

func toCategory(cat paviCategory, depth, level int) store.Category {
	node := store.Category{ID: cat.ID, Name: strings.TrimSpace(cat.Description)}
	if depth > 0 && level >= depth {
		return node
	}
	for _, child := range cat.Items {
		node.Children = append(node.Children, toCategory(child, depth, level+1))
	}
	return node
}

// Product detail has no by-id JSON endpoint exposed for guests.
func (c *Client) Product(id string) (*store.Product, error) { return nil, store.ErrUnsupported }
