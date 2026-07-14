// Package savemart is an adapter for Savemart (savemart.com.mt), a Malta
// supermarket running a server-rendered Laravel storefront. Search results are
// scraped from the results page; every card carries a data-price attribute with
// the current (post-discount) price, which is what we bill. Prices are EUR.
package savemart

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
	baseURL   = "https://savemart.com.mt"
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

// Cards open with <div class="product">. Within a card the title link carries the
// product id in its href and the name (pack size included) as its text. The price
// box shows a struck-through was-price followed by the current one; data-price
// holds that current figure numerically, so we prefer it and fall back to the
// last €-amount in the box when the attribute is missing.
var (
	cardRe  = regexp.MustCompile(`<div class="product"`)
	titleRe = regexp.MustCompile(`(?s)<h2 class="product-title">\s*<a[^>]*/product/(\d+)[^"]*"[^>]*>(.*?)</a>`)
	dataRe  = regexp.MustCompile(`data-price="([0-9]+(?:\.[0-9]+)?)"`)
	euroRe  = regexp.MustCompile(`(?:&euro;|€)\s*([0-9]+(?:\.[0-9]+)?)`)
	priceRe = regexp.MustCompile(`(?s)<span class="product-price">(.*?)</span>`)
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
	body, err := c.get(baseURL + "/search?q=" + url.QueryEscape(term))
	if err != nil {
		return nil, err
	}

	loc := cardRe.FindAllStringIndex(body, -1)
	if len(loc) == 0 {
		c.log("savemart: no product cards found for %q", term)
		return []store.Hit{}, nil
	}

	out := make([]store.Hit, 0, len(loc))
	seen := make(map[string]bool, len(loc))
	for i, m := range loc {
		end := len(body)
		if i+1 < len(loc) {
			end = loc[i+1][0]
		}
		card := body[m[0]:end]

		tm := titleRe.FindStringSubmatch(card)
		if tm == nil {
			continue
		}
		id, name := tm[1], cleanText(tm[2])
		if id == "" || name == "" || seen[id] {
			continue
		}

		price := priceFrom(card)
		if price <= 0 {
			c.log("savemart: card %s (%s) has no price, skipping", id, name)
			continue
		}

		seen[id] = true
		out = append(out, store.Hit{
			ID:        id,
			Name:      name,
			Price:     price,
			Currency:  "EUR",
			Available: true,
			URL:       baseURL + "/product/" + id,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// priceFrom prefers the numeric data-price attribute (the current, post-discount
// price). Failing that it takes the LAST €-amount in the price box, because the
// first is the struck-through was-price.
func priceFrom(card string) float64 {
	if dm := dataRe.FindStringSubmatch(card); dm != nil {
		if p, err := strconv.ParseFloat(dm[1], 64); err == nil && p > 0 {
			return p
		}
	}
	pm := priceRe.FindStringSubmatch(card)
	if pm == nil {
		return 0
	}
	all := euroRe.FindAllStringSubmatch(pm[1], -1)
	if len(all) == 0 {
		return 0
	}
	p, err := strconv.ParseFloat(all[len(all)-1][1], 64)
	if err != nil {
		return 0
	}
	return p
}

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Product(id string) (*store.Product, error)      { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
