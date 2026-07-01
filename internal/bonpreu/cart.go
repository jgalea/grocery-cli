package bonpreu

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jgalea/grocery-cli/internal/store"
)

// bonpreu authenticates by pasting the user's own browser Cookie header (the
// login is cookie/SSO-based). The header carries the session (global_sid) plus
// the AWS WAF clearance; the CLI caches it and replays it on cart reads/writes.
// It never places an order — the user reviews and pays in the browser.

// session is the machine-managed auth cache.
type session struct {
	Cookie string `json:"cookie"`
}

func (c *Client) authPath() string {
	dir := os.Getenv("GROCERY_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grocery")
	}
	return filepath.Join(dir, "auth-"+c.key+".json")
}

// SetCookie caches the raw Cookie header copied from a logged-in browser session.
func (c *Client) SetCookie(cookieHeader string) error {
	cookieHeader = strings.TrimSpace(cookieHeader)
	if cookieHeader == "" {
		return fmt.Errorf("empty cookie header")
	}
	c.cookie = cookieHeader
	if err := os.MkdirAll(filepath.Dir(c.authPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(session{Cookie: cookieHeader}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.authPath(), b, 0o600)
}

// loadCookie loads the cached Cookie header onto the client (once). Returns false
// when no session is cached.
func (c *Client) loadCookie() bool {
	if c.cookie != "" {
		return true
	}
	b, err := os.ReadFile(c.authPath())
	if err != nil {
		return false
	}
	var s session
	if json.Unmarshal(b, &s) != nil || strings.TrimSpace(s.Cookie) == "" {
		return false
	}
	c.cookie = s.Cookie
	return true
}

// LoggedIn reports whether a usable cached cookie session exists.
func (c *Client) LoggedIn() bool { return c.loadCookie() }

func (c *Client) requireAuth() error {
	if !c.loadCookie() {
		return fmt.Errorf("not logged in; run `grocery --store %s set-cookie <header>` with the Cookie header from a logged-in browser session", c.key)
	}
	return nil
}

// --- cart JSON shapes ---

const (
	cartActive    = "/api/cart/v1/carts/active"
	applyQuantity = cartActive + "/apply-quantity?cartProductSorting=CATEGORIES"
)

// cartItem is one element of an apply-quantity request: the product's stable UUID
// (productId, not the short retailer id) and a quantity DELTA (+adds, -removes).
type cartItem struct {
	ProductID string  `json:"productId"`
	Quantity  float64 `json:"quantity"`
}

type cartLineRaw struct {
	ProductID   string  `json:"productId"`
	Quantity    float64 `json:"quantity"`
	Name        string  `json:"name"`
	FinalPrice  money   `json:"finalPrice"`
	TotalPrices struct {
		FinalPrice money `json:"finalPrice"`
	} `json:"totalPrices"`
	Product struct {
		Name              string `json:"name"`
		RetailerProductID string `json:"retailerProductId"`
	} `json:"product"`
}

func (l cartLineRaw) name() string {
	if l.Name != "" {
		return l.Name
	}
	return l.Product.Name
}

type cartShape struct {
	Items  []cartLineRaw `json:"items"`
	Totals struct {
		ItemPriceAfterPromos money `json:"itemPriceAfterPromos"`
	} `json:"totals"`
}

func (c *Client) activeCart() (*cartShape, error) {
	var s cartShape
	if err := c.getJSON(c.baseURL+cartActive, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *cartShape) toStore() *store.Cart {
	out := &store.Cart{Currency: "EUR"}
	for _, l := range s.Items {
		out.Lines = append(out.Lines, store.CartLine{
			ID:    l.ProductID,
			Name:  strings.TrimSpace(l.name()),
			Qty:   l.Quantity,
			Price: l.FinalPrice.float(),
		})
		out.Count++
	}
	out.Total = s.Totals.ItemPriceAfterPromos.float()
	if cur := s.Totals.ItemPriceAfterPromos.Currency; cur != "" {
		out.Currency = cur
	}
	return out
}

// CartGet fetches the active cart.
func (c *Client) CartGet() (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	s, err := c.activeCart()
	if err != nil {
		return nil, err
	}
	return s.toStore(), nil
}

var csrfRE = regexp.MustCompile(`"csrf":\{"token":"([0-9a-fA-F-]{8,})"`)

// csrfToken reads the CSRF token the web app embeds in the authenticated
// homepage SSR state (session.csrf.token); cart writes require it.
func (c *Client) csrfToken() (string, error) {
	page, _, err := c.getText(c.baseURL + "/")
	if err != nil {
		return "", err
	}
	m := csrfRE.FindStringSubmatch(page)
	if m == nil {
		return "", fmt.Errorf("no CSRF token in page — the session may not be authenticated")
	}
	return m[1], nil
}

// resolveUUID turns a short retailer product id into the stable productId UUID
// the cart API keys on, by scraping the SSR product page. Ids that already look
// like a UUID (hyphenated) are returned as-is.
func (c *Client) resolveUUID(id string) (string, error) {
	if strings.Contains(id, "-") {
		return id, nil
	}
	page, status, err := c.getText(c.baseURL + "/products/" + id)
	if err != nil {
		if status == 404 {
			return "", fmt.Errorf("product %s: not found", id)
		}
		return "", err
	}
	node := findProductNode(page, id)
	if node == nil {
		return "", fmt.Errorf("product %s: could not resolve to a cart id", id)
	}
	if uuid, _ := node["productId"].(string); uuid != "" {
		return uuid, nil
	}
	return "", fmt.Errorf("product %s: no productId on page", id)
}

// applyQuantities posts quantity DELTAS to the active cart (the same call the web
// app's add/remove buttons make), fetching a CSRF token first, then returns the
// refreshed cart.
func (c *Client) applyQuantities(items []cartItem) (*store.Cart, error) {
	if len(items) == 0 {
		return c.CartGet()
	}
	csrf, err := c.csrfToken()
	if err != nil {
		return nil, err
	}
	req, err := c.newReq("POST", c.baseURL+applyQuantity, items, map[string]string{
		"x-csrf-token":        csrf,
		"ecom-request-source": "web",
	})
	if err != nil {
		return nil, err
	}
	if _, _, err := c.do(req); err != nil {
		return nil, err
	}
	return c.CartGet()
}

// CartAdd adds qty of a product to the cart (a positive delta).
func (c *Client) CartAdd(productID string, qty float64) (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	uuid, err := c.resolveUUID(productID)
	if err != nil {
		return nil, err
	}
	return c.applyQuantities([]cartItem{{ProductID: uuid, Quantity: qty}})
}

// CartSet sets a product's line to an absolute quantity. The API is additive, so
// this computes the delta from the current line quantity.
func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	uuid, err := c.resolveUUID(productID)
	if err != nil {
		return nil, err
	}
	s, err := c.activeCart()
	if err != nil {
		return nil, err
	}
	var current float64
	for _, l := range s.Items {
		if l.ProductID == uuid {
			current = l.Quantity
			break
		}
	}
	delta := qty - current
	if delta == 0 {
		return s.toStore(), nil
	}
	return c.applyQuantities([]cartItem{{ProductID: uuid, Quantity: delta}})
}

// CartClear empties the cart by applying a negative delta for every line.
func (c *Client) CartClear() (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	s, err := c.activeCart()
	if err != nil {
		return nil, err
	}
	items := make([]cartItem, 0, len(s.Items))
	for _, l := range s.Items {
		if l.Quantity != 0 {
			items = append(items, cartItem{ProductID: l.ProductID, Quantity: -l.Quantity})
		}
	}
	return c.applyQuantities(items)
}
