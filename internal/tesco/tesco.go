// Package tesco is an adapter for Tesco (tesco.com groceries). Data operations
// go to Tesco's GraphQL gateway at https://xapi.tesco.com/ as batched POSTs
// (an array of operation objects); product search is a two-step dance —
// search.api.tesco.com returns a list of TPNBs, then xapi batch-fetches full
// product detail for each. Every request carries a static public x-apikey plus
// language/region headers. The whole site sits behind Akamai, which rejects
// requests without a logged-in browser session, so reads AND cart writes require
// the user's own Cookie header (see cart.go). The CLI fills the basket but never
// places an order.
//
// Ported from the TypeScript reference at github.com/abracadabra50/uk-grocery-cli
// (src/providers/tesco/api.ts + index.ts). Built to the reference's request
// shapes; untested live (needs a real cookie).
package tesco

import (
	"bytes"
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
	xapiURL   = "https://xapi.tesco.com/"
	searchURL = "https://search.api.tesco.com/search"
	siteURL   = "https://www.tesco.com/groceries/en-GB/"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	// tescoAPIKey is the static public key baked into Tesco's mfe-* bundles
	// (confirmed in the reference's discovery notes). Not a secret.
	tescoAPIKey = "TvOSZJHlEk0pjniDGQFAc9Q59WGAR4dA"

	defaultCount = 24
)

// Client is the Tesco adapter. It holds the HTTP client, an optional cached
// Cookie header (loaded lazily) and a logf diagnostics hook.
type Client struct {
	key  string
	http *http.Client
	logf func(string, ...any)

	cookie string // cached raw Cookie header; loaded lazily from disk
}

// New returns a Tesco adapter.
func New(key string, logf func(string, ...any)) *Client {
	return &Client{
		key:  key,
		http: &http.Client{Timeout: 30 * time.Second},
		logf: logf,
	}
}

func (c *Client) Key() string { return c.key }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

const maxBodyBytes = 16 << 20

// gqlOp is one operation in a Tesco GraphQL batch.
type gqlOp struct {
	OperationName string `json:"operationName"`
	Variables     any    `json:"variables"`
	Query         string `json:"query"`
}

// gqlResult is one element of the batched GraphQL response array.
type gqlResult struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// newReq builds a request with Tesco's shared headers and the cached Cookie.
func (c *Client) newReq(method, u string, body []byte, xapi bool) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("user-agent", userAgent)
	if xapi {
		req.Header.Set("x-apikey", tescoAPIKey)
		req.Header.Set("language", "en-GB")
		req.Header.Set("region", "UK")
		req.Header.Set("referer", siteURL)
		req.Header.Set("origin", "https://www.tesco.com")
		if body != nil {
			req.Header.Set("content-type", "application/json")
		}
	} else {
		req.Header.Set("accept-language", "en-GB")
		req.Header.Set("referer", "https://www.tesco.com/")
	}
	if c.cookie != "" {
		req.Header.Set("cookie", c.cookie)
	}
	return req, nil
}

func (c *Client) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("tesco api: http %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body from %s: %w", req.URL, readErr)
	}
	return data, resp.StatusCode, nil
}

// gqlBatch POSTs a batch of operations to xapi and returns the parsed results in
// order. It requires a cached cookie.
func (c *Client) gqlBatch(ops []gqlOp) ([]gqlResult, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(ops)
	if err != nil {
		return nil, err
	}
	req, err := c.newReq("POST", xapiURL, body, true)
	if err != nil {
		return nil, err
	}
	data, _, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var out []gqlResult
	if err := json.Unmarshal(data, &out); err != nil {
		// A non-array response usually means an auth/HTML challenge page.
		return nil, fmt.Errorf("decode xapi response: %w", err)
	}
	return out, nil
}

// gql runs a single GraphQL operation and unmarshals its data into out.
func (c *Client) gql(op gqlOp, out any) error {
	results, err := c.gqlBatch([]gqlOp{op})
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return fmt.Errorf("empty xapi response for %s", op.OperationName)
	}
	r := results[0]
	if len(r.Errors) > 0 {
		msgs := make([]string, 0, len(r.Errors))
		for _, e := range r.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("graphql error (%s): %s", op.OperationName, strings.Join(msgs, ", "))
	}
	if out != nil && len(r.Data) > 0 {
		if err := json.Unmarshal(r.Data, out); err != nil {
			return fmt.Errorf("decode %s data: %w", op.OperationName, err)
		}
	}
	return nil
}

// --- product shapes ---

type tescoPrice struct {
	Actual float64 `json:"actual"`
}

type tescoUnitPrice struct {
	Price   float64 `json:"price"`
	Measure string  `json:"measure"`
}

type tescoProduct struct {
	ID           string         `json:"id"`
	GTIN         string         `json:"gtin"`
	Title        string         `json:"title"`
	Price        tescoPrice     `json:"price"`
	UnitPrice    tescoUnitPrice `json:"unitPrice"`
	DisplayPrice struct {
		Value float64 `json:"value"`
	} `json:"displayPrice"`
	IsAvailable     *bool  `json:"isAvailable"`
	DefaultImageURL string `json:"defaultImageUrl"`
	Description     struct {
		Features []string `json:"features"`
		Info     string   `json:"info"`
	} `json:"description"`
}

func (p tescoProduct) price() float64 {
	if p.DisplayPrice.Value > 0 {
		return p.DisplayPrice.Value
	}
	if p.Price.Actual > 0 {
		return p.Price.Actual
	}
	return p.UnitPrice.Price
}

func (p tescoProduct) available() bool {
	return p.IsAvailable == nil || *p.IsAvailable
}

func (c *Client) toHit(p tescoProduct) store.Hit {
	return store.Hit{
		ID:           p.ID,
		Name:         strings.TrimSpace(p.Title),
		Price:        p.price(),
		PricePerUnit: p.UnitPrice.Price,
		Unit:         normUnit(p.UnitPrice.Measure),
		Currency:     "GBP",
		Available:    p.available(),
		Eco:          isEco(p),
		URL:          "https://www.tesco.com/groceries/en-GB/products/" + p.ID,
	}
}

const productByTpnbQuery = `query GetProductByTpnb($tpnb: String) {
  product(tpnb: $tpnb) {
    id
    gtin
    title
    price { actual }
    unitPrice { price measure }
    isAvailable
    defaultImageUrl
  }
}`

const getProductQuery = `query GetProduct($tpnc: String, $skipReviews: Boolean, $offset: Int, $count: Int) {
  product(tpnc: $tpnc) {
    id
    gtin
    title
    unitPrice { price measure }
    displayPrice { value }
    isAvailable
    defaultImageUrl
    description { features info }
  }
}`

// Search runs the reference's two-step search: TPNBs from search.api.tesco.com,
// then a GraphQL batch to xapi for full detail on each.
func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	count := defaultCount
	if limit > 0 {
		count = limit
	}

	// Step 1: TPNBs from the public search API (still cookie-gated by Akamai).
	q := url.Values{}
	q.Set("distchannel", "ghs")
	q.Set("query", term)
	q.Set("count", strconv.Itoa(count))
	q.Set("offset", "0")
	req, err := c.newReq("GET", searchURL+"?"+q.Encode(), nil, false)
	if err != nil {
		return nil, err
	}
	data, _, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var sr struct {
		UK struct {
			GHS struct {
				Products struct {
					Results []struct {
						TPNB json.Number `json:"tpnb"`
					} `json:"results"`
				} `json:"products"`
			} `json:"ghs"`
		} `json:"uk"`
	}
	if err := json.Unmarshal(data, &sr); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	var tpnbs []string
	for _, r := range sr.UK.GHS.Products.Results {
		if s := r.TPNB.String(); s != "" {
			tpnbs = append(tpnbs, s)
		}
		if len(tpnbs) >= count {
			break
		}
	}
	if len(tpnbs) == 0 {
		return nil, nil
	}

	// Step 2: batch-fetch product detail (one op per TPNB).
	ops := make([]gqlOp, 0, len(tpnbs))
	for _, tpnb := range tpnbs {
		ops = append(ops, gqlOp{
			OperationName: "GetProductByTpnb",
			Variables:     map[string]any{"tpnb": tpnb},
			Query:         productByTpnbQuery,
		})
	}
	results, err := c.gqlBatch(ops)
	if err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(results))
	for _, r := range results {
		if len(r.Errors) > 0 || len(r.Data) == 0 {
			continue
		}
		var d struct {
			Product tescoProduct `json:"product"`
		}
		if json.Unmarshal(r.Data, &d) != nil || d.Product.ID == "" {
			continue
		}
		h := c.toHit(d.Product)
		if eco && !h.Eco {
			continue
		}
		out = append(out, h)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Product fetches full detail for a single Tesco product number (tpnc).
func (c *Client) Product(id string) (*store.Product, error) {
	var d struct {
		Product tescoProduct `json:"product"`
	}
	op := gqlOp{
		OperationName: "GetProduct",
		Variables:     map[string]any{"tpnc": id, "skipReviews": true, "offset": 0, "count": 5},
		Query:         getProductQuery,
	}
	if err := c.gql(op, &d); err != nil {
		return nil, err
	}
	if d.Product.ID == "" {
		return nil, fmt.Errorf("product %s: not found", id)
	}
	prod := &store.Product{Hit: c.toHit(d.Product)}
	prod.EAN = d.Product.GTIN
	if len(d.Product.Description.Features) > 0 {
		prod.Ingredients = strings.Join(d.Product.Description.Features, "; ")
	} else {
		prod.Ingredients = strings.TrimSpace(d.Product.Description.Info)
	}
	return prod, nil
}

// CategoryProducts isn't exposed by the reference for Tesco (browsing goes
// through the SSR pages Akamai blocks).
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}

const taxonomyQuery = `query Taxonomy($includeChildren: Boolean = true) {
  taxonomy(includeInspirationEvents: false) {
    name
    label
    children @include(if: $includeChildren) {
      id
      name
      label
      children { id name label }
    }
  }
}`

type taxNode struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Label    string    `json:"label"`
	Children []taxNode `json:"children"`
}

// Categories returns the Tesco taxonomy tree via the xapi Taxonomy query. depth
// is advisory; the query fetches the levels Tesco exposes.
func (c *Client) Categories(depth int) ([]store.Category, error) {
	var d struct {
		Taxonomy []taxNode `json:"taxonomy"`
	}
	op := gqlOp{
		OperationName: "Taxonomy",
		Variables:     map[string]any{"includeChildren": true},
		Query:         taxonomyQuery,
	}
	if err := c.gql(op, &d); err != nil {
		return nil, err
	}
	return mapCats(d.Taxonomy), nil
}

func mapCats(in []taxNode) []store.Category {
	out := make([]store.Category, 0, len(in))
	for _, n := range in {
		id := n.ID
		if id == "" {
			id = n.Name
		}
		name := n.Label
		if name == "" {
			name = n.Name
		}
		out = append(out, store.Category{ID: id, Name: name, Children: mapCats(n.Children)})
	}
	return out
}

// --- helpers ---

func normUnit(measure string) string {
	m := strings.ToLower(strings.TrimSpace(measure))
	switch {
	case m == "":
		return ""
	case strings.Contains(m, "kg") || strings.Contains(m, "kilo") || m == "g" || strings.Contains(m, "gram"):
		return "kg"
	case m == "l" || strings.Contains(m, "litre") || strings.Contains(m, "liter") || strings.Contains(m, "cl") || strings.Contains(m, "ml"):
		return "L"
	case strings.Contains(m, "each") || strings.Contains(m, "unit") || m == "ea" || m == "u":
		return "u"
	default:
		return ""
	}
}

func isEco(p tescoProduct) bool {
	hay := strings.ToLower(p.Title)
	for _, kw := range []string{"organic", " bio", "bio ", "eco "} {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
