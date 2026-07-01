// Package welbees is an adapter for Welbee's (welbees.mt), a Malta supermarket
// whose storefront is server-rendered HTML. There is no public JSON API, so
// search results are scraped from the shop listing page with stdlib regexp.
// Prices are EUR.
package welbees

import (
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

const (
	baseURL   = "https://welbees.mt"
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

// Each product tile opens with data-product-code="..."; the tile then carries an
// <h6> name and a price rendered as €/&euro; inside a div.text-tertiary. We slice
// the HTML into per-tile windows on the code attribute, then pull name and price
// from each window.
var (
	codeRe  = regexp.MustCompile(`data-product-code="([^"]+)"`)
	nameRe  = regexp.MustCompile(`(?s)<h6[^>]*>(.*?)</h6>`)
	priceRe = regexp.MustCompile(`text-tertiary[^>]*>\s*(?:&euro;|€)\s*([0-9]+(?:\.[0-9]+)?)`)
	tagRe   = regexp.MustCompile(`<[^>]*>`)
)

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

func (c *Client) get(u string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	return string(raw), nil
}

func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	body, err := c.get(baseURL + "/shop?s=" + url.QueryEscape(term))
	if err != nil {
		return nil, err
	}

	loc := codeRe.FindAllStringSubmatchIndex(body, -1)
	if len(loc) == 0 {
		c.log("welbees: no product tiles found for %q", term)
		return []store.Hit{}, nil
	}

	out := make([]store.Hit, 0, len(loc))
	seen := make(map[string]bool, len(loc))
	for i, m := range loc {
		id := body[m[2]:m[3]]
		if id == "" || seen[id] {
			continue
		}
		// Tile window runs from this code attribute to the start of the next one.
		end := len(body)
		if i+1 < len(loc) {
			end = loc[i+1][0]
		}
		tile := body[m[0]:end]

		var name string
		if nm := nameRe.FindStringSubmatch(tile); nm != nil {
			name = cleanText(nm[1])
		}
		if name == "" {
			c.log("welbees: tile %s has no name, skipping", id)
			continue
		}

		var price float64
		if pm := priceRe.FindStringSubmatch(tile); pm != nil {
			if p, perr := strconv.ParseFloat(pm[1], 64); perr == nil {
				price = p
			}
		}

		seen[id] = true
		out = append(out, store.Hit{
			ID:        id,
			Name:      name,
			Price:     price,
			Currency:  "EUR",
			Available: true,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// The storefront has no guest-accessible product-detail or category JSON, so
// only search (and batch on top of it) is supported today.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Product(id string) (*store.Product, error)      { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
