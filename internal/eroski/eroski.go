// Package eroski is an adapter for Eroski (supermercado.eroski.es), whose
// storefront is a server-rendered Apache Tapestry app with no clean guest JSON
// catalog. Each product tile embeds a data-metrics analytics blob (HTML-entity
// encoded JSON) on its title link carrying id, name, brand, price and currency,
// so we scrape the search page and read that blob rather than the visible price
// spans (which sit before the title in the tile and are error-prone to pair up).
// Prices are the default-region EUR prices Eroski serves to guests.
package eroski

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	searchURL = "https://supermercado.eroski.es/es/search/results/?q=%s&suggestionActive=false"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
)

// tileRe captures the analytics JSON and href from each product title link.
// The data-metrics value is HTML-entity encoded, so it contains no raw double
// quotes and [^"]* stays inside a single attribute.
var tileRe = regexp.MustCompile(`<a class="product-title-link" data-metrics="([^"]*)"\s+href="([^"]*)"`)

type Client struct {
	key  string
	http *http.Client
	logf func(string, ...any)
}

func New(key string, logf func(string, ...any)) *Client {
	return &Client{key: key, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.key }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// metrics mirrors the GA4 select_item payload Eroski embeds per product tile.
type metrics struct {
	Ecommerce struct {
		Currency string `json:"currency"`
		Items    []struct {
			Price float64 `json:"price"`
			Name  string  `json:"item_name"`
			ID    string  `json:"item_id"`
			Brand string  `json:"item_brand"`
		} `json:"items"`
	} `json:"ecommerce"`
}

func (c *Client) fetch(u string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return body, nil
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	body, err := c.fetch(fmt.Sprintf(searchURL, url.QueryEscape(term)))
	if err != nil {
		return nil, err
	}

	matches := tileRe.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		c.log("eroski: no product tiles matched for %q (markup may have changed)", term)
		return []store.Hit{}, nil
	}

	out := make([]store.Hit, 0, len(matches))
	seen := make(map[string]bool)
	for _, m := range matches {
		var mt metrics
		if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &mt); err != nil {
			c.log("eroski: skipping tile with unparseable metrics: %v", err)
			continue
		}
		if len(mt.Ecommerce.Items) == 0 {
			continue
		}
		it := mt.Ecommerce.Items[0]
		if it.ID == "" || seen[it.ID] {
			continue
		}
		seen[it.ID] = true

		cur := mt.Ecommerce.Currency
		if cur == "" {
			cur = "EUR"
		}
		out = append(out, store.Hit{
			ID:        it.ID,
			Name:      strings.TrimSpace(it.Name),
			Price:     it.Price,
			Currency:  cur,
			Brand:     strings.TrimSpace(it.Brand),
			Available: true,
			URL:       html.UnescapeString(string(m[2])),
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// CategoryProducts, Product and Categories aren't scraped yet: Eroski gates the
// category tree and detail pages behind heavier Tapestry state. Search + batch
// work today.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Product(id string) (*store.Product, error)      { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
