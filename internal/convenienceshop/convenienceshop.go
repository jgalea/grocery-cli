// Package convenienceshop is an adapter for The Convenience Shop (Malta), which
// runs on the Suppy commerce platform. Suppy exposes an HMAC-signed JSON API:
// every request is a POST whose body is signed with HMAC-SHA256 and sent as the
// X-OSP-Signature header. Prices are the Malta branch (branchId 186) prices in
// EUR. Only search (and therefore batch) is wired up today.
package convenienceshop

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	baseURL   = "https://api.suppy.app/api"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
	branchID  = 186 // The Convenience Shop, Malta

	// apiKey / apiSecret are PUBLIC client values shipped in the storefront's
	// config.js (used to HMAC-sign requests from the browser). They are not
	// secrets; they may rotate if the storefront config changes.
	apiKey    = "9904F967C2C156C77FDC9B973CEA7CAD43E1C473651CF88E78BE8EA7AB679578"
	apiSecret = "12503183CB4BA996C387B3E71105A67408AEA87822FB497BDA8A64C8154C0CA0"
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

type searchRequest struct {
	BranchID           int    `json:"branchId"`
	Light              bool   `json:"light"`
	IncludeProbablyOOS bool   `json:"includeProbablyOOS"`
	SortBy             int    `json:"sortBy"`
	TypeOfRequest      int    `json:"typeOfRequest"`
	Search             string `json:"search"`
	IsASearchRequest   bool   `json:"isASearchRequest"`
}

type suppyItem struct {
	ID                    int     `json:"id"`
	Name                  string  `json:"name"`
	Price                 float64 `json:"price"`
	PricePerNormalizedUoM float64 `json:"pricePerNormalizedUoM"`
	NormalizedUoM         string  `json:"normalizedUoM"`
	Brand                 string  `json:"brand"`
	CategoryName          string  `json:"categoryName"`
	CurrencyID            int     `json:"currencyId"`
}

type searchResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Items []suppyItem `json:"items"`
	} `json:"data"`
}

func (c *Client) toHit(it suppyItem) store.Hit {
	return store.Hit{
		ID:           strconv.Itoa(it.ID),
		Name:         strings.TrimSpace(it.Name),
		Price:        it.Price,
		PricePerUnit: it.PricePerNormalizedUoM,
		Unit:         it.NormalizedUoM,
		Currency:     "EUR",
		Brand:        strings.TrimSpace(it.Brand),
		Category:     strings.TrimSpace(it.CategoryName),
		Available:    true,
	}
}

// postJSON marshals body once, signs those exact bytes, and sends the same bytes
// as the request body so the HMAC matches what the server verifies.
func (c *Client) postJSON(path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(apiSecret))
	mac.Write(raw)
	sig := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("X-OSP-API-Key", apiKey)
	req.Header.Set("X-OSP-Signature", sig)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-OSP-Content-Language", "EN")
	req.Header.Set("X-OSP-Source", "WSA")
	req.Header.Set("X-OSP-Version", "20250206")
	req.Header.Set("X-OSP-Subdomain", "myconvenience")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}

func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	reqBody := searchRequest{
		BranchID:           branchID,
		Light:              false,
		IncludeProbablyOOS: true,
		SortBy:             0,
		TypeOfRequest:      1,
		Search:             term,
		IsASearchRequest:   true,
	}
	var resp searchResponse
	if err := c.postJSON("/items", reqBody, &resp); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(resp.Data.Items))
	for _, it := range resp.Data.Items {
		out = append(out, c.toHit(it))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// The Suppy storefront categories and product-detail paths aren't wired up here
// yet; search (and batch, which builds on search) is what works today.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
func (c *Client) Product(id string) (*store.Product, error)      { return nil, store.ErrUnsupported }
func (c *Client) Categories(depth int) ([]store.Category, error) { return nil, store.ErrUnsupported }
