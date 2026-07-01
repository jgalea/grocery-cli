// Package greens is an adapter for Greens Supermarket (Malta), an ASP.NET store
// whose product API needs a Bearer token that the site renders inline in each
// product page (not a secret). Free-text search returns nothing server-side, so
// this adapter browses by category (the reliable path).
package greens

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	baseURL   = "https://www.greens.com.mt"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
)

type Client struct {
	key   string
	http  *http.Client
	logf  func(string, ...any)
	token string
}

func New(key string, logf func(string, ...any)) *Client {
	return &Client{key: key, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.key }

var tokenRe = regexp.MustCompile(`getProductList\('([^']+)'`)

// ensureToken scrapes the (non-secret) Bearer token the storefront embeds.
func (c *Client) ensureToken() error {
	if c.token != "" {
		return nil
	}
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/products?cat=Beverages", nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	m := tokenRe.FindSubmatch(body)
	if m == nil {
		return fmt.Errorf("greens: could not find API token in product page")
	}
	c.token = string(m[1])
	return nil
}

type productDetails struct {
	PartNumber  string  `json:"PART_NUMBER"`
	Description string  `json:"PART_DESCRIPTION"`
	SalesPrice  float64 `json:"SALES_PRICE"`
}

type listResponse struct {
	ProductList []struct {
		ProductDetails productDetails `json:"ProductDetails"`
	} `json:"ProductList"`
	Categories []struct {
		Group    string `json:"GROUP"`
		Shortcut string `json:"GROUP_SHORTCUT"`
	} `json:"Categories"`
}

func (c *Client) list(searchCriteria, category string, records int) (*listResponse, error) {
	if err := c.ensureToken(); err != nil {
		return nil, err
	}
	if records <= 0 {
		records = 48
	}
	q := url.Values{}
	q.Set("Agent", "GREENS")
	q.Set("Loc", "SM")
	q.Set("Eid", "N/A")
	q.Set("SearchCriteria", searchCriteria)
	q.Set("page", "1")
	q.Set("NumberOfRecords", fmt.Sprint(records))
	q.Set("SortType", "Position")
	q.Set("SortDirection", "Asc")
	q.Set("Category", category)
	q.Set("Category2", "")
	q.Set("Category3", "")
	q.Set("Type", "")
	q.Set("Cid", "00000000-0000-0000-0000-000000000000")
	q.Set("Cart", "00000000-0000-0000-0000-000000000000")
	q.Set("SubType", "")
	q.Set("Brand", "")
	q.Set("ProductListType", "products")
	q.Set("Mobdev", "False")
	q.Set("Detailed", "True")

	req, _ := http.NewRequest(http.MethodGet, baseURL+"/apiservices/retail/sync/productlist?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	var lr listResponse
	if err := json.Unmarshal(raw, &lr); err != nil {
		return nil, err
	}
	return &lr, nil
}

func (lr *listResponse) hits(limit int) []store.Hit {
	out := make([]store.Hit, 0, len(lr.ProductList))
	for _, p := range lr.ProductList {
		d := p.ProductDetails
		if d.PartNumber == "" {
			continue
		}
		out = append(out, store.Hit{
			ID: d.PartNumber, Name: strings.TrimSpace(d.Description), Price: d.SalesPrice,
			Currency: "EUR", Available: true,
		})
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Search tries free-text (the store returns nothing for it in practice); use
// categories to browse.
func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	lr, err := c.list(term, "", limit)
	if err != nil {
		return nil, err
	}
	return lr.hits(limit), nil
}

func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	lr, err := c.list("", categoryID, limit)
	if err != nil {
		return nil, err
	}
	return lr.hits(limit), nil
}

// Categories seeds the full taxonomy with a broad search (an empty query returns
// none; any letter returns every category as "Department|Subgroup" shortcuts).
// Departments are the reliable CategoryProducts key.
func (c *Client) Categories(depth int) ([]store.Category, error) {
	lr, err := c.list("a", "", 400)
	if err != nil {
		return nil, err
	}
	order := []string{}
	subs := map[string][]store.Category{}
	seenDept := map[string]bool{}
	for _, cat := range lr.Categories {
		parts := strings.SplitN(cat.Shortcut, "|", 2)
		dept := parts[0]
		if dept == "" {
			continue
		}
		if !seenDept[dept] {
			seenDept[dept] = true
			order = append(order, dept)
		}
		if depth > 1 && len(parts) == 2 && parts[1] != "" {
			subs[dept] = append(subs[dept], store.Category{ID: parts[1], Name: parts[1]})
		}
	}
	out := make([]store.Category, 0, len(order))
	for _, dept := range order {
		out = append(out, store.Category{ID: dept, Name: dept, Children: subs[dept]})
	}
	return out, nil
}

func (c *Client) Product(id string) (*store.Product, error) { return nil, store.ErrUnsupported }
