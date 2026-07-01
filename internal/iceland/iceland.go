// Package iceland is an adapter for Iceland (iceland.co.uk, UK), which serves
// its product catalog through a hosted Algolia search index. Prices are in GBP.
// The Algolia endpoint rejects requests without a matching storefront Referer.
package iceland

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

// These are the public, client-side Algolia credentials shipped in the Iceland
// storefront JS. They are search-only and can rotate; refetch from the site if
// requests start failing.
const (
	algoliaAppID  = "FAWURXX413"
	algoliaAPIKey = "dd51afec328646fc6b538411032deeb0"
	algoliaIndex  = "r1_iceuk_production__products__default"
	referer       = "https://www.iceland.co.uk/"

	baseURL   = "https://www.iceland.co.uk"
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

type icelandItem struct {
	ID       string             `json:"id"`
	ObjectID string             `json:"objectID"`
	Name     string             `json:"name"`
	Brand    string             `json:"brand"`
	Price    map[string]float64 `json:"price"`
	InStock  *bool              `json:"in_stock"`
	URL      string             `json:"url"`

	// Detail-only fields.
	EAN                  string          `json:"ean"`
	ManufacturerAddress  string          `json:"manufacturerAddress"`
	Ingredients          string          `json:"ingredients"`
	NutritionInformation json.RawMessage `json:"nutritionInformation"`
	StorageInformation   string          `json:"storageInformation"`
	PromotionalBadgeText string          `json:"promotionalBadgeText"`
}

func (it icelandItem) id() string {
	if it.ObjectID != "" {
		return it.ObjectID
	}
	return it.ID
}

func (c *Client) toHit(it icelandItem) store.Hit {
	u := it.URL
	if u != "" && strings.HasPrefix(u, "/") {
		u = baseURL + u
	}
	available := true
	if it.InStock != nil {
		available = *it.InStock
	}
	return store.Hit{
		ID:        it.id(),
		Name:      strings.TrimSpace(it.Name),
		Price:     it.Price["GBP"],
		Brand:     strings.TrimSpace(it.Brand),
		Currency:  "GBP",
		Available: available,
		URL:       u,
	}
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("X-Algolia-Application-Id", algoliaAppID)
	req.Header.Set("X-Algolia-API-Key", algoliaAPIKey)
	req.Header.Set("Referer", referer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	hpp := limit
	if hpp <= 0 {
		hpp = 24
	}
	body, _ := json.Marshal(map[string]any{"query": term, "hitsPerPage": hpp})
	u := fmt.Sprintf("https://%s-dsn.algolia.net/1/indexes/%s/query", algoliaAppID, algoliaIndex)
	req, _ := http.NewRequest(http.MethodPost, u, bytes.NewReader(body))
	var resp struct {
		Hits []icelandItem `json:"hits"`
	}
	if err := c.do(req, &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Hits))
	for _, it := range resp.Hits {
		out = append(out, c.toHit(it))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *Client) Product(id string) (*store.Product, error) {
	u := fmt.Sprintf("https://%s-dsn.algolia.net/1/indexes/%s/%s", algoliaAppID, algoliaIndex, id)
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	var it icelandItem
	if err := c.do(req, &it); err != nil {
		return nil, err
	}
	p := &store.Product{
		Hit:          c.toHit(it),
		EAN:          it.EAN,
		Origin:       strings.TrimSpace(it.ManufacturerAddress),
		Ingredients:  strings.TrimSpace(it.Ingredients),
		Conservation: strings.TrimSpace(it.StorageInformation),
		OnSale:       strings.TrimSpace(it.PromotionalBadgeText) != "",
	}
	if len(it.NutritionInformation) > 0 && string(it.NutritionInformation) != "null" {
		p.Nutrients = string(it.NutritionInformation)
	}
	return p, nil
}

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}

func (c *Client) Categories(depth int) ([]store.Category, error) {
	return nil, store.ErrUnsupported
}
