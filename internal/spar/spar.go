// Package spar is an adapter for SPAR Malta (shop.spar.com.mt), a server-rendered
// PHP storefront. Search results are scraped from the category page; each card
// shows a was-price (.prev) and the price actually charged (.current), and we bill
// the latter. Prices are EUR.
package spar

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
	baseURL   = "https://shop.spar.com.mt"
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

// Cards open with class="product-slide-entry". Inside, the favourite button holds
// the item code, .title holds the name with the pack size embedded, and .current
// holds the charged price. Cards are sliced on their own boundaries so a greedy
// match cannot pull the next card's fields into this one.
var (
	cardRe    = regexp.MustCompile(`class="product-slide-entry"`)
	codeRe    = regexp.MustCompile(`data-itemcode="([^"]+)"`)
	titleRe   = regexp.MustCompile(`(?s)<div class="title"[^>]*>(.*?)</div>`)
	currentRe = regexp.MustCompile(`(?s)<div class="current">\s*(?:&euro;|€)?\s*([0-9]+(?:\.[0-9]+)?)`)
	tagRe     = regexp.MustCompile(`<[^>]*>`)
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
	body, err := c.get(baseURL + "/category.php?search=" + url.QueryEscape(term) + "&categoryid=&sort=default")
	if err != nil {
		return nil, err
	}

	loc := cardRe.FindAllStringIndex(body, -1)
	if len(loc) == 0 {
		c.log("spar: no product cards found for %q", term)
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

		cm := codeRe.FindStringSubmatch(card)
		tm := titleRe.FindStringSubmatch(card)
		if cm == nil || tm == nil {
			continue
		}
		id, name := cm[1], cleanText(tm[1])
		if id == "" || name == "" {
			continue
		}
		// The same item code can appear more than once across a results page.
		key := id + "|" + name
		if seen[key] {
			continue
		}

		var price float64
		if pm := currentRe.FindStringSubmatch(card); pm != nil {
			if p, perr := strconv.ParseFloat(pm[1], 64); perr == nil {
				price = p
			}
		}
		if price <= 0 {
			c.log("spar: card %s (%s) has no current price, skipping", id, name)
			continue
		}

		seen[key] = true
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

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Product(id string) (*store.Product, error)      { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
