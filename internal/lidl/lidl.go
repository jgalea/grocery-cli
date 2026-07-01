// Package lidl is an adapter for Lidl storefronts (lidl.pt, lidl.es, ...), which
// expose an open REST search API at /q/api/search on the storefront origin. The
// same JSON shape works across countries, so the adapter is config-driven: one
// Config per country (host + assortment + locale + currency). Lidl's assortment
// is weekly offers plus the non-food bazaar rather than a full grocery catalog,
// so results reflect what the search API returns for a term at the time.
package lidl

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"

// Config describes a single Lidl country storefront.
type Config struct {
	Key        string // adapter key, e.g. "lidl-pt"
	Host       string // e.g. "www.lidl.pt"
	Assortment string // e.g. "PT"
	Locale     string // e.g. "pt_PT"
	Currency   string // e.g. "EUR"
}

type Client struct {
	cfg  Config
	http *http.Client
	logf func(string, ...any)
}

func New(cfg Config, logf func(string, ...any)) *Client {
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.cfg.Key }

// lidlItem mirrors the relevant slice of items[].gridbox.data in the search JSON.
type lidlItem struct {
	Code    string `json:"code"`
	GridBox struct {
		Data struct {
			ItemID    int64  `json:"itemId"`
			ErpNumber string `json:"erpNumber"`
			FullTitle string `json:"fullTitle"`
			Category  string `json:"category"`
			Brand     struct {
				Name string `json:"name"`
			} `json:"brand"`
			CanonicalURL string `json:"canonicalUrl"`
			Price        struct {
				Price     float64 `json:"price"`
				BasePrice struct {
					Text string `json:"text"`
				} `json:"basePrice"`
			} `json:"price"`
		} `json:"data"`
	} `json:"gridbox"`
}

func (c *Client) baseURL() string { return "https://" + c.cfg.Host }

func (c *Client) getJSON(u string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	// NOTE: do NOT send Accept: application/json — the Lidl edge returns 406 for it.
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.Unmarshal(raw, out)
}

// basePriceRe matches unit-price strings like "1 L = 1.75", "100 g = 0.50",
// "1 kg = 2.99". Group 1 = quantity, group 2 = unit token, group 3 = price.
var basePriceRe = regexp.MustCompile(`(?i)([\d.,]+)\s*([a-zµ]+)\s*=\s*([\d.,]+)`)

func parseNum(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseBasePrice extracts (pricePerUnit, normalisedUnit) from a Lidl basePrice
// text. Units are normalised to kg | L | u; returns zero values if unparseable.
func parseBasePrice(text string) (float64, string) {
	m := basePriceRe.FindStringSubmatch(text)
	if m == nil {
		return 0, ""
	}
	qty, okQ := parseNum(m[1])
	price, okP := parseNum(m[3])
	if !okP {
		return 0, ""
	}
	unit := strings.ToLower(m[2])
	switch unit {
	case "kg":
		return price, "kg"
	case "g":
		// price is per (qty) grams; normalise to per kg.
		if okQ && qty > 0 {
			return price * 1000 / qty, "kg"
		}
		return price, "kg"
	case "l":
		return price, "L"
	case "ml", "cl":
		return price, "L"
	case "u", "ud", "uds", "un", "st", "stk":
		return price, "u"
	default:
		return price, unit
	}
}

func (c *Client) toHit(it lidlItem) store.Hit {
	d := it.GridBox.Data
	id := strings.TrimSpace(it.Code)
	if id == "" {
		id = strings.TrimSpace(d.ErpNumber)
	}
	if id == "" && d.ItemID != 0 {
		id = strconv.FormatInt(d.ItemID, 10)
	}
	u := strings.TrimSpace(d.CanonicalURL)
	if u != "" && strings.HasPrefix(u, "/") {
		u = c.baseURL() + u
	}
	ppu, unit := parseBasePrice(d.Price.BasePrice.Text)
	return store.Hit{
		ID:           id,
		Name:         strings.TrimSpace(d.FullTitle),
		Price:        d.Price.Price,
		PricePerUnit: ppu,
		Unit:         unit,
		Currency:     c.cfg.Currency,
		Brand:        strings.TrimSpace(d.Brand.Name),
		Category:     strings.TrimSpace(d.Category),
		Available:    true,
		URL:          u,
	}
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	q := url.Values{}
	q.Set("q", term)
	q.Set("assortment", c.cfg.Assortment)
	q.Set("locale", c.cfg.Locale)
	q.Set("version", "2.0") // mandatory; omitting it yields a non-JSON response
	u := c.baseURL() + "/q/api/search?" + q.Encode()

	var resp struct {
		Items []lidlItem `json:"items"`
	}
	if err := c.getJSON(u, &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Items))
	for _, it := range resp.Items {
		h := c.toHit(it)
		if h.ID == "" && h.Name == "" {
			continue
		}
		out = append(out, h)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CategoryProducts / Product / Categories aren't exposed as clean guest JSON on
// the Lidl search origin; only search (and batching over it) is supported.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Product(id string) (*store.Product, error)      { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
