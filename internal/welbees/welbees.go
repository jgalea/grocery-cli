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
//
// The tile also shows the shelf-edge unit price ("€22.11/kg") and the pack size in
// its own font-light div ("90grms"). Neither appears in the product name, so both
// are lifted out: the unit price populates PricePerUnit, and the pack size is
// appended to the name so size matching and per-unit ranking have something to
// work with. A struck-through "RRP €2.99" is the was-price and is ignored; the
// div.text-tertiary figure is what the shopper actually pays.
var (
	codeRe    = regexp.MustCompile(`data-product-code="([^"]+)"`)
	nameRe    = regexp.MustCompile(`(?s)<h6[^>]*>(.*?)</h6>`)
	priceRe   = regexp.MustCompile(`text-tertiary[^>]*>\s*(?:&euro;|€)\s*([0-9]+(?:\.[0-9]+)?)`)
	perUnitRe = regexp.MustCompile(`(?i)(?:&euro;|€)\s*([0-9]+(?:\.[0-9]+)?)\s*/\s*(kg|l|p)\b`)
	packRe    = regexp.MustCompile(`(?s)font-light[^>]*>\s*([^<]*?\d[^<]*?)\s*</div>`)
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

		// Pack size lives outside the name ("90grms"), so fold it in.
		if pk := packRe.FindStringSubmatch(tile); pk != nil {
			if s := cleanText(pk[1]); s != "" {
				name = name + " " + s
			}
		}

		var perUnit float64
		var unit string
		if um := perUnitRe.FindStringSubmatch(tile); um != nil {
			if v, uerr := strconv.ParseFloat(um[1], 64); uerr == nil && v > 0 {
				perUnit = v
				switch strings.ToLower(um[2]) {
				case "kg":
					unit = "kg"
				case "l":
					unit = "L"
				case "p": // priced per piece
					unit = "u"
				}
			}
		}

		seen[id] = true
		out = append(out, store.Hit{
			ID:           id,
			Name:         name,
			Price:        price,
			PricePerUnit: perUnit,
			Unit:         unit,
			Currency:     "EUR",
			Available:    true,
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
