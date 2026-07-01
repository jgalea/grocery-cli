// Package ssr is an adapter for classic (server-rendered) Salesforce Commerce
// Cloud stores that don't expose a guest API. It reads the SSR search grid and
// parses the per-tile JSON the storefront embeds for its own analytics
// (data-product-tile-impression), which carries id, name, price, brand and
// category — no auth, no scraping of prices out of markup.
package ssr

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

// Config is the per-store SSR wiring.
type Config struct {
	Key      string
	BaseURL  string // e.g. https://www.continente.pt
	SiteID   string // SFCC site id, e.g. continente
	Locale   string // SFCC locale segment, e.g. pt_PT
	Currency string // e.g. EUR
}

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"

// Client is an SSR adapter for one store.
type Client struct {
	cfg  Config
	http *http.Client
	logf func(string, ...any)
}

func New(cfg Config, logf func(string, ...any)) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.cfg.Key }

// tileRe matches the per-tile analytics JSON SFCC storefronts embed. Themes use
// different attribute names and key styles:
//
//	Continente: data-product-tile-impression → {id,name,price,brand,category}
//	Auchan:     data-gtm                      → {id,name,price,brand,category}
//	Pingo Doce: data-gtm-info                 → {item_id,item_name,price,item_brand} (GA4)
var tileRe = regexp.MustCompile(`data-(?:product-tile-impression|gtm|gtm-info)=(?:'([^']*)'|"([^"]*)")`)

// tileItem is a product inside a GA4-style event blob (Pingo Doce's data-gtm-info
// wraps its product in {value, items:[{item_id,...}]}).
type tileItem struct {
	ItemID       string    `json:"item_id"`
	ItemName     string    `json:"item_name"`
	ItemBrand    string    `json:"item_brand"`
	ItemCategory string    `json:"item_category"`
	Price        flexFloat `json:"price"`
}

type tileJSON struct {
	// flat shapes (Continente / Auchan)
	ID           string    `json:"id"`
	ItemID       string    `json:"item_id"`
	Name         string    `json:"name"`
	ItemName     string    `json:"item_name"`
	Brand        string    `json:"brand"`
	ItemBrand    string    `json:"item_brand"`
	Category     string    `json:"category"`
	ItemCategory string    `json:"item_category"`
	Price        flexFloat `json:"price"`
	// nested GA4 event shape (Pingo Doce)
	Value flexFloat  `json:"value"`
	Items []tileItem `json:"items"`
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// products flattens a tile blob into one or more store.Hit-ready records,
// handling both the flat shape and the nested GA4 items[] shape.
func (t tileJSON) products() []tileItem {
	if len(t.Items) > 0 {
		out := t.Items
		// GA4 events carry the price at the top (value); backfill items missing it.
		for i := range out {
			if out[i].Price == 0 {
				out[i].Price = t.Value
			}
		}
		return out
	}
	return []tileItem{{
		ItemID:       first(t.ID, t.ItemID),
		ItemName:     first(t.Name, t.ItemName),
		ItemBrand:    first(t.Brand, t.ItemBrand),
		ItemCategory: first(t.Category, t.ItemCategory),
		Price:        t.Price,
	}}
}

// flexFloat parses a JSON number or a quoted numeric string ("0.86").
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err == nil {
		*f = flexFloat(v)
	}
	return nil
}

func (c *Client) get(u string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "text/html")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	return string(body), nil
}

// Search reads the SSR grid and parses the embedded product tiles.
func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	q := url.Values{}
	q.Set("q", term)
	if limit > 0 {
		q.Set("sz", strconv.Itoa(limit))
	}
	u := fmt.Sprintf("%s/on/demandware.store/Sites-%s-Site/%s/Search-UpdateGrid?%s",
		c.cfg.BaseURL, c.cfg.SiteID, c.cfg.Locale, q.Encode())
	body, err := c.get(u)
	if err != nil {
		return nil, err
	}
	hits := c.parseTiles(body)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (c *Client) parseTiles(body string) []store.Hit {
	seen := map[string]bool{}
	var out []store.Hit
	for _, m := range tileRe.FindAllStringSubmatch(body, -1) {
		raw := m[1]
		if raw == "" {
			raw = m[2]
		}
		var t tileJSON
		if !decodeTile(raw, &t) {
			continue
		}
		for _, it := range t.products() {
			if it.ItemID == "" || it.ItemName == "" || seen[it.ItemID] {
				continue
			}
			seen[it.ItemID] = true
			cat := it.ItemCategory
			if i := strings.LastIndexByte(cat, '/'); i >= 0 {
				cat = cat[i+1:]
			}
			out = append(out, store.Hit{
				ID: it.ItemID, Name: strings.TrimSpace(it.ItemName), Price: float64(it.Price),
				Brand: it.ItemBrand, Category: cat, Currency: c.cfg.Currency, Available: true,
			})
		}
	}
	return out
}

// Product / Categories aren't wired for SSR stores yet (they need PDP / nav
// parsing). Search — and therefore batch — work today.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Product(id string) (*store.Product, error) { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) {
	return nil, store.ErrUnsupported
}

// decodeTile HTML-unescapes the embedded JSON and unmarshals it.
func decodeTile(raw string, t *tileJSON) bool {
	return json.Unmarshal([]byte(html.UnescapeString(raw)), t) == nil
}
