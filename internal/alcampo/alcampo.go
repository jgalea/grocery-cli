// Package alcampo is an adapter for Alcampo (compraonline.alcampo.es), the
// Spanish Auchan storefront. It has no open JSON API for guests; the search
// page is server-rendered with a Redux preloaded-state blob inlined in the
// HTML, keyed "productEntities". We locate that object, brace-match it, and
// read each product entity. Prices are EUR, default region.
package alcampo

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	baseURL   = "https://www.compraonline.alcampo.es"
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

// amount is a price field that Alcampo serves as either a JSON string ("5.76")
// or, occasionally, a number. It unmarshals from both.
type amount float64

func (a *amount) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*a = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		*a = 0
		return nil
	}
	*a = amount(f)
	return nil
}

type entity struct {
	RetailerProductID string   `json:"retailerProductId"`
	Name              string   `json:"name"`
	Brand             string   `json:"brand"`
	Available         bool     `json:"available"`
	CategoryPath      []string `json:"categoryPath"`
	Price             struct {
		Current struct {
			Amount   amount `json:"amount"`
			Currency string `json:"currency"`
		} `json:"current"`
		Unit struct {
			Label   string `json:"label"`
			Current struct {
				Amount   amount `json:"amount"`
				Currency string `json:"currency"`
			} `json:"current"`
		} `json:"unit"`
	} `json:"price"`
}

// normaliseUnit maps Alcampo's fop unit labels to the CLI's kg | L | u.
func normaliseUnit(label string) string {
	l := strings.ToLower(label)
	switch {
	case strings.Contains(l, "litre") || strings.Contains(l, "liter") || strings.Contains(l, "litro"):
		return "L"
	case strings.Contains(l, "kilo") || strings.Contains(l, "gram"):
		return "kg"
	case strings.Contains(l, "unit") || strings.Contains(l, "piece") || strings.Contains(l, "unid"):
		return "u"
	default:
		return ""
	}
}

func (c *Client) toHit(id string, e entity) store.Hit {
	cat := ""
	if n := len(e.CategoryPath); n > 0 {
		cat = e.CategoryPath[n-1]
	}
	cur := e.Price.Current.Currency
	if cur == "" {
		cur = "EUR"
	}
	return store.Hit{
		ID:           id,
		Name:         strings.TrimSpace(e.Name),
		Price:        float64(e.Price.Current.Amount),
		PricePerUnit: float64(e.Price.Unit.Current.Amount),
		Unit:         normaliseUnit(e.Price.Unit.Label),
		Currency:     cur,
		Brand:        strings.TrimSpace(e.Brand),
		Category:     cat,
		Available:    true,
		URL:          baseURL + "/products/" + id,
	}
}

func (c *Client) fetch(u string) ([]byte, error) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9")
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

// extractProductEntities finds the "productEntities" object in the SSR HTML and
// returns its raw JSON bytes, or nil if not present.
func extractProductEntities(html []byte) []byte {
	key := `"productEntities"`
	idx := strings.Index(string(html), key)
	if idx < 0 {
		return nil
	}
	// Advance to the first '{' after the key.
	start := -1
	for i := idx + len(key); i < len(html); i++ {
		if html[i] == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(html); i++ {
		c := html[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : i+1]
			}
		}
	}
	return nil
}

func (c *Client) parse(html []byte) ([]store.Hit, error) {
	blob := extractProductEntities(html)
	if blob == nil {
		c.logf("alcampo: productEntities not found in page")
		return []store.Hit{}, nil
	}
	// Decode as raw messages first so a single malformed entity doesn't sink all.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(blob, &raw); err != nil {
		c.logf("alcampo: productEntities unmarshal failed: %v", err)
		return []store.Hit{}, nil
	}
	out := make([]store.Hit, 0, len(raw))
	for _, rm := range raw {
		// Detect display-only entities: empty amount string means no purchasable price.
		var probe struct {
			Price struct {
				Current struct {
					Amount json.RawMessage `json:"amount"`
				} `json:"current"`
			} `json:"price"`
		}
		if err := json.Unmarshal(rm, &probe); err == nil {
			if strings.Trim(string(probe.Price.Current.Amount), `"`) == "" {
				continue
			}
		}
		var e entity
		if err := json.Unmarshal(rm, &e); err != nil {
			continue
		}
		if e.RetailerProductID == "" {
			continue
		}
		out = append(out, c.toHit(e.RetailerProductID, e))
	}
	return out, nil
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	html, err := c.fetch(baseURL + "/search?q=" + url.QueryEscape(term))
	if err != nil {
		return nil, err
	}
	hits, err := c.parse(html)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// CategoryProducts / Product / Categories aren't wired up yet: category listing
// needs a facet path we haven't mapped, and product detail lives behind the SPA.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}

func (c *Client) Product(id string) (*store.Product, error) { return nil, store.ErrUnsupported }

func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
