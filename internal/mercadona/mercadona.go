// Package mercadona is an adapter for Mercadona's online store, which is not
// Salesforce Commerce Cloud: search runs on Algolia (public search-only creds
// embedded in the SPA) and product/category detail on Mercadona's open REST API.
// Mercadona's catalog is per-warehouse; the adapter targets one warehouse.
package mercadona

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

// Config is the per-instance wiring. The Algolia creds are the public,
// search-only pair embedded in Mercadona's web bundle (they rotate; see the
// note in the registry).
type Config struct {
	Key        string
	BaseURL    string // https://tienda.mercadona.es
	AlgoliaApp string
	AlgoliaKey string
	IndexBase  string // products_prod
	Warehouse  string // e.g. bcn1, mad1, vlc1
	Lang       string // es
}

const userAgent = "grocery-cli (+https://github.com/jgalea/grocery-cli)"

type Client struct {
	cfg  Config
	http *http.Client
	logf func(string, ...any)
}

func New(cfg Config, logf func(string, ...any)) *Client {
	if cfg.Warehouse == "" {
		cfg.Warehouse = "bcn1"
	}
	if cfg.Lang == "" {
		cfg.Lang = "es"
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.cfg.Key }

func (c *Client) index() string {
	return c.cfg.IndexBase + "_" + c.cfg.Warehouse + "_" + c.cfg.Lang
}

// flexFloat parses a JSON number or quoted numeric string ("3.90").
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexFloat(v)
	}
	return nil
}

// flexStr accepts a JSON string or number and yields a string.
type flexStr string

func (s *flexStr) UnmarshalJSON(b []byte) error {
	*s = flexStr(strings.Trim(string(b), `"`))
	return nil
}

type priceInstructions struct {
	UnitPrice       flexFloat `json:"unit_price"`
	ReferencePrice  flexFloat `json:"reference_price"`
	ReferenceFormat string    `json:"reference_format"`
}

type mHit struct {
	ID       flexStr           `json:"id"`
	Name     string            `json:"display_name"`
	Slug     string            `json:"slug"`
	ShareURL string            `json:"share_url"`
	Thumb    string            `json:"thumbnail"`
	Price    priceInstructions `json:"price_instructions"`
}

func (c *Client) toHit(h mHit) store.Hit {
	return store.Hit{
		ID: string(h.ID), Name: strings.TrimSpace(h.Name),
		Price: float64(h.Price.UnitPrice), PricePerUnit: float64(h.Price.ReferencePrice),
		Unit: normUnit(h.Price.ReferenceFormat), Currency: "EUR", Available: true,
		URL: h.ShareURL,
	}
}

// Search queries the Algolia index. eco is ignored (Mercadona has no eco facet).
func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	if limit <= 0 {
		limit = 24
	}
	body, _ := json.Marshal(map[string]any{"query": term, "hitsPerPage": limit})
	url := "https://" + c.cfg.AlgoliaApp + "-dsn.algolia.net/1/indexes/" + c.index() + "/query"
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("X-Algolia-Application-Id", c.cfg.AlgoliaApp)
	req.Header.Set("X-Algolia-API-Key", c.cfg.AlgoliaKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("algolia http %d", resp.StatusCode)
	}
	var sr struct {
		Hits []mHit `json:"hits"`
	}
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(sr.Hits))
	for _, h := range sr.Hits {
		out = append(out, c.toHit(h))
	}
	return out, nil
}

func (c *Client) apiJSON(path string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, c.cfg.BaseURL+path, nil)
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

type mProduct struct {
	ID       flexStr           `json:"id"`
	EAN      string            `json:"ean"`
	Name     string            `json:"display_name"`
	Brand    string            `json:"brand"`
	ShareURL string            `json:"share_url"`
	Slug     string            `json:"slug"`
	Origin   string            `json:"origin"`
	Price    priceInstructions `json:"price_instructions"`
}

func (c *Client) Product(id string) (*store.Product, error) {
	var p mProduct
	if err := c.apiJSON("/api/products/"+id+"/", &p); err != nil {
		return nil, err
	}
	url := p.ShareURL
	if url == "" && p.Slug != "" {
		url = c.cfg.BaseURL + "/product/" + string(p.ID) + "/" + p.Slug
	}
	return &store.Product{
		Hit: store.Hit{
			ID: string(p.ID), Name: strings.TrimSpace(p.Name),
			Price: float64(p.Price.UnitPrice), PricePerUnit: float64(p.Price.ReferencePrice),
			Unit: normUnit(p.Price.ReferenceFormat), Currency: "EUR", Brand: p.Brand,
			Available: true, URL: url,
		},
		EAN: p.EAN, Origin: p.Origin,
	}, nil
}

type mCategory struct {
	ID         flexStr     `json:"id"`
	Name       string      `json:"name"`
	Categories []mCategory `json:"categories"`
	Products   []mHit      `json:"products"`
}

func (c *Client) Categories(depth int) ([]store.Category, error) {
	var resp struct {
		Results []mCategory `json:"results"`
	}
	if err := c.apiJSON("/api/categories/", &resp); err != nil {
		return nil, err
	}
	return mapCats(resp.Results), nil
}

func mapCats(in []mCategory) []store.Category {
	out := make([]store.Category, 0, len(in))
	for _, c := range in {
		out = append(out, store.Category{ID: string(c.ID), Name: c.Name, Children: mapCats(c.Categories)})
	}
	return out
}

// CategoryProducts fetches a category and gathers its products (recursing into
// subcategories, since Mercadona nests products under leaf categories).
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	var cat mCategory
	if err := c.apiJSON("/api/categories/"+categoryID+"/", &cat); err != nil {
		return nil, err
	}
	var hits []store.Hit
	var walk func(m mCategory)
	walk = func(m mCategory) {
		for _, h := range m.Products {
			hits = append(hits, c.toHit(h))
		}
		for _, sub := range m.Categories {
			walk(sub)
		}
	}
	walk(cat)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func normUnit(u string) string {
	switch strings.ToLower(strings.TrimSpace(u)) {
	case "kg", "g":
		return "kg"
	case "l", "ml", "cl":
		return "L"
	case "u", "un", "ud", "uds", "unidad", "unitat", "pack":
		return "u"
	default:
		return ""
	}
}
