// Package edeka24 is an adapter for Edeka24 (edeka24.de), a German grocery
// storefront running on OXID eShop with server-rendered HTML. There is no clean
// guest JSON API, so search results are scraped from the SSR listing markup.
// Prices are German (lang=0) prices in EUR.
package edeka24

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
	baseURL   = "https://www.edeka24.de"
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

var (
	// Product tile anchor: <a id="listitem_N" href="<url>" class="title" title="<name>">
	tileRe = regexp.MustCompile(`(?s)<a id="listitem_\d+"\s+href="([^"]+)"\s+class="title"\s+title="([^"]*)"`)
	// Price block: <div class="price"> 4,59 € </div> (may be "price salesprice").
	priceRe = regexp.MustCompile(`(?s)class="price(?:\s+salesprice)?"\s*>\s*([\d.,]+)\s*&euro;|class="price(?:\s+salesprice)?"\s*>\s*([\d.,]+)\s*€`)
	// Slug is the last path segment ending in .html.
	slugRe = regexp.MustCompile(`/([^/?#]+)\.html`)
)

func (c *Client) get(u string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "de-DE,de;q=0.9")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return raw, nil
}

func slug(u string) string {
	if m := slugRe.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	return ""
}

func cleanURL(rawHref string) string {
	u := html.UnescapeString(rawHref)
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	if strings.HasPrefix(u, "/") {
		u = baseURL + u
	}
	return u
}

// parsePrice converts a German-formatted decimal ("4,59" or "1.299,00") to float.
func parsePrice(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", ".")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	u := baseURL + "/index.php?cl=search&searchparam=" + url.QueryEscape(term) + "&lang=0"
	body, err := c.get(u)
	if err != nil {
		return nil, err
	}
	doc := string(body)

	tiles := tileRe.FindAllStringSubmatchIndex(doc, -1)
	prices := priceRe.FindAllStringSubmatchIndex(doc, -1)
	if len(tiles) == 0 {
		if c.logf != nil {
			c.logf("edeka24: no product tiles matched for %q", term)
		}
		return []store.Hit{}, nil
	}

	out := make([]store.Hit, 0, len(tiles))
	seen := make(map[string]bool)

	for i, t := range tiles {
		hrefRaw := doc[t[2]:t[3]]
		nameRaw := doc[t[4]:t[5]]

		id := slug(html.UnescapeString(hrefRaw))
		if id == "" || seen[id] {
			continue
		}

		// Price is the first price block appearing after this tile's anchor and
		// before the next tile's anchor.
		anchorEnd := t[1]
		nextAnchor := len(doc)
		if i+1 < len(tiles) {
			nextAnchor = tiles[i+1][0]
		}
		var price float64
		for _, p := range prices {
			if p[0] < anchorEnd {
				continue
			}
			if p[0] >= nextAnchor {
				break
			}
			// Group 1 (&euro;) or group 2 (€) captured the number.
			var num string
			if p[2] >= 0 {
				num = doc[p[2]:p[3]]
			} else if p[4] >= 0 {
				num = doc[p[4]:p[5]]
			}
			price = parsePrice(num)
			break
		}

		seen[id] = true
		out = append(out, store.Hit{
			ID:        id,
			Name:      strings.TrimSpace(html.UnescapeString(nameRaw)),
			Price:     price,
			Currency:  "EUR",
			Available: true,
			URL:       cleanURL(hrefRaw),
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

func (c *Client) Product(id string) (*store.Product, error) { return nil, store.ErrUnsupported }

func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
