package tesco

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jgalea/grocery-cli/internal/store"
)

// Tesco authenticates by pasting the user's own browser Cookie header. The header
// carries the session cookies (and Akamai clearance) that xapi requires; the CLI
// caches it and replays it on reads and cart writes. It never places an order —
// the user reviews and pays in the browser.

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

// requireAuth guards every request: Akamai blocks anonymous reads and writes.
func (c *Client) requireAuth() error {
	if !c.loadCookie() {
		return fmt.Errorf("log in first: grocery --store %s login (paste your browser Cookie)", c.key)
	}
	return nil
}

// --- basket shapes ---

const getBasketQuery = `query GetBasket($basketContexts: [BasketContextType]) {
  basket(basketContexts: $basketContexts) {
    id
    splitView {
      id
      totalPrice
      totalItems
      items {
        id
        quantity
        cost
        unit
        product {
          id
          tpnb
          gtin
          title
          price { actual }
        }
      }
    }
  }
}`

const updateBasketMutation = `mutation UpdateBasket($items: [BasketLineItemInputType], $orderId: ID) {
  basket(items: $items, orderId: $orderId) {
    id
    splitView {
      id
      totalPrice
      totalItems
      items {
        id
        quantity
        cost
        product { id title price { actual } }
      }
    }
  }
}`

type basketLineRaw struct {
	ID       string  `json:"id"`
	Quantity float64 `json:"quantity"`
	Cost     float64 `json:"cost"`
	Product  struct {
		ID    string     `json:"id"`
		Title string     `json:"title"`
		Price tescoPrice `json:"price"`
	} `json:"product"`
}

type splitViewRaw struct {
	ID         string          `json:"id"`
	TotalPrice float64         `json:"totalPrice"`
	TotalItems int             `json:"totalItems"`
	Items      []basketLineRaw `json:"items"`
}

// basketRaw handles splitView being either a single object or an array (the
// reference guards for both).
type basketRaw struct {
	ID        string       `json:"id"`
	SplitView splitViewRaw `json:"splitView"`
}

func (b *basketRaw) UnmarshalJSON(data []byte) error {
	var probe struct {
		ID        string          `json:"id"`
		SplitView json.RawMessage `json:"splitView"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	b.ID = probe.ID
	if len(probe.SplitView) == 0 {
		return nil
	}
	if probe.SplitView[0] == '[' {
		var arr []splitViewRaw
		if err := json.Unmarshal(probe.SplitView, &arr); err != nil {
			return err
		}
		if len(arr) > 0 {
			b.SplitView = arr[0]
		}
		return nil
	}
	return json.Unmarshal(probe.SplitView, &b.SplitView)
}

func (b *basketRaw) toStore() *store.Cart {
	out := &store.Cart{Currency: "GBP", Total: b.SplitView.TotalPrice, Count: b.SplitView.TotalItems}
	for _, l := range b.SplitView.Items {
		unit := 0.0
		if l.Product.Price.Actual > 0 {
			unit = l.Product.Price.Actual
		} else if l.Quantity != 0 {
			unit = l.Cost / l.Quantity
		}
		out.Lines = append(out.Lines, store.CartLine{
			ID:    l.Product.ID, // TPNC — the id used to add/set/remove
			Name:  strings.TrimSpace(l.Product.Title),
			Qty:   l.Quantity,
			Price: unit,
		})
	}
	if out.Count == 0 {
		out.Count = len(out.Lines)
	}
	return out
}

func (c *Client) basket() (*basketRaw, error) {
	var d struct {
		Basket basketRaw `json:"basket"`
	}
	op := gqlOp{
		OperationName: "GetBasket",
		Variables:     map[string]any{},
		Query:         getBasketQuery,
	}
	if err := c.gql(op, &d); err != nil {
		return nil, err
	}
	return &d.Basket, nil
}

// CartGet fetches the active basket.
func (c *Client) CartGet() (*store.Cart, error) {
	b, err := c.basket()
	if err != nil {
		return nil, err
	}
	return b.toStore(), nil
}

// updateBasket sets a product line to an absolute quantity (Tesco's UpdateBasket
// takes newValue as the target quantity; 0 removes). It needs the basket orderId.
func (c *Client) updateBasket(tpnc string, quantity float64, orderID string) (*store.Cart, error) {
	var d struct {
		Basket basketRaw `json:"basket"`
	}
	op := gqlOp{
		OperationName: "UpdateBasket",
		Variables: map[string]any{
			"orderId": orderID,
			"items": []map[string]any{{
				"adjustment":    false,
				"id":            tpnc,
				"newValue":      quantity,
				"newUnitChoice": "pcs",
			}},
		},
		Query: updateBasketMutation,
	}
	if err := c.gql(op, &d); err != nil {
		return nil, err
	}
	return d.Basket.toStore(), nil
}

// currentQty returns the current line quantity for a product in the basket, and
// the basket orderId.
func (c *Client) currentQty(b *basketRaw, tpnc string) float64 {
	for _, l := range b.SplitView.Items {
		if l.Product.ID == tpnc {
			return l.Quantity
		}
	}
	return 0
}

// CartAdd adds qty to a product's current basket quantity.
func (c *Client) CartAdd(productID string, qty float64) (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	b, err := c.basket()
	if err != nil {
		return nil, err
	}
	if b.ID == "" {
		return nil, fmt.Errorf("could not resolve basket id — is the session logged in?")
	}
	return c.updateBasket(productID, c.currentQty(b, productID)+qty, b.ID)
}

// CartSet sets a product's line to an absolute quantity.
func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	b, err := c.basket()
	if err != nil {
		return nil, err
	}
	if b.ID == "" {
		return nil, fmt.Errorf("could not resolve basket id — is the session logged in?")
	}
	return c.updateBasket(productID, qty, b.ID)
}

// CartClear empties the basket by setting every line to 0.
func (c *Client) CartClear() (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	b, err := c.basket()
	if err != nil {
		return nil, err
	}
	if b.ID == "" {
		return nil, fmt.Errorf("could not resolve basket id — is the session logged in?")
	}
	var last *store.Cart = b.toStore()
	for _, l := range b.SplitView.Items {
		if l.Quantity == 0 {
			continue
		}
		last, err = c.updateBasket(l.Product.ID, 0, b.ID)
		if err != nil {
			return nil, err
		}
	}
	return last, nil
}
