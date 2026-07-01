package sainsburys

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

// Sainsbury's authenticates by pasting the user's own browser Cookie header. The
// header carries the WCSSO session plus the Akamai clearance; the basket API
// also wants the WC_AUTHENTICATION_* value echoed in a wcauthtoken header, which
// this adapter extracts from the cookie automatically (see newReq). The CLI
// caches the header and replays it on reads and cart writes; it never places an
// order — the user reviews and pays in the browser.

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

// wcAuthToken pulls the WC_AUTHENTICATION_* value out of the cached cookie; the
// basket API expects it in a wcauthtoken header.
func (c *Client) wcAuthToken() string {
	for _, part := range strings.Split(c.cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "WC_AUTHENTICATION_") {
			if i := strings.IndexByte(part, '='); i >= 0 {
				return strings.TrimSpace(part[i+1:])
			}
		}
	}
	return ""
}

// pickTime is the pick_time the basket endpoints require: tomorrow, same time.
func pickTime() string {
	return time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
}

// basketParams are the shared query params on every /basket/v2 call.
func (c *Client) basketParams() string {
	return fmt.Sprintf("pick_time=%s&store_number=%s&slot_booked=false", pickTime(), c.store)
}

// --- basket JSON shapes ---

type basketItemRaw struct {
	ItemUID string `json:"item_uid"`
	Product struct {
		SKU  string `json:"sku"`
		Name string `json:"name"`
	} `json:"product"`
	Quantity      float64 `json:"quantity"`
	SubtotalPrice string  `json:"subtotal_price"`
}

type basketRaw struct {
	Items      []basketItemRaw `json:"items"`
	ItemCount  int             `json:"item_count"`
	TotalPrice string          `json:"total_price"`
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func (b *basketRaw) toStore() *store.Cart {
	out := &store.Cart{Currency: "GBP", Count: b.ItemCount, Total: parseFloat(b.TotalPrice)}
	for _, it := range b.Items {
		unit := 0.0
		if it.Quantity != 0 {
			unit = parseFloat(it.SubtotalPrice) / it.Quantity
		}
		out.Lines = append(out.Lines, store.CartLine{
			ID:    it.Product.SKU,
			Name:  strings.TrimSpace(it.Product.Name),
			Qty:   it.Quantity,
			Price: unit,
		})
	}
	if out.Count == 0 {
		out.Count = len(out.Lines)
	}
	return out
}

func (c *Client) basket() (*basketRaw, error) {
	var b basketRaw
	if err := c.getJSON(baseURL+"/basket/v2/basket?"+c.basketParams(), &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// CartGet fetches the active basket.
func (c *Client) CartGet() (*store.Cart, error) {
	b, err := c.basket()
	if err != nil {
		return nil, err
	}
	return b.toStore(), nil
}

// postJSON sends a body to a basket endpoint (method POST/PUT) after the auth
// guard; the response body is ignored (the caller re-reads the basket).
func (c *Client) sendBasket(method, u string, body any) error {
	if err := c.requireAuth(); err != nil {
		return err
	}
	req, err := c.newReq(method, u, body)
	if err != nil {
		return err
	}
	_, _, err = c.do(req)
	return err
}

// CartAdd adds qty of a product to the basket. The POST /basket/item call is
// additive, matching the reference's addToBasket.
func (c *Client) CartAdd(productID string, qty float64) (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	body := map[string]any{
		"product_uid":          productID,
		"quantity":             qty,
		"uom":                  "ea",
		"selected_catchweight": "",
	}
	if err := c.sendBasket("POST", baseURL+"/basket/v2/basket/item?"+c.basketParams(), body); err != nil {
		return nil, err
	}
	return c.CartGet()
}

// CartSet sets a product's line to an absolute quantity. It finds the existing
// line to get its item_uid and PUTs the new quantity; if the product isn't in
// the basket yet it falls back to an additive add.
func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	b, err := c.basket()
	if err != nil {
		return nil, err
	}
	var line *basketItemRaw
	for i := range b.Items {
		if b.Items[i].Product.SKU == productID {
			line = &b.Items[i]
			break
		}
	}
	if line == nil {
		if qty <= 0 {
			return b.toStore(), nil
		}
		return c.CartAdd(productID, qty)
	}
	body := map[string]any{
		"items": []map[string]any{{
			"product_uid":          productID,
			"quantity":             qty,
			"uom":                  "ea",
			"selected_catchweight": "",
			"item_uid":             line.ItemUID,
			"decreasing_quantity":  qty < line.Quantity,
		}},
	}
	if err := c.sendBasket("PUT", baseURL+"/basket/v2/basket?"+c.basketParams(), body); err != nil {
		return nil, err
	}
	return c.CartGet()
}

// CartClear empties the basket by setting every line to quantity 0.
func (c *Client) CartClear() (*store.Cart, error) {
	if err := c.requireAuth(); err != nil {
		return nil, err
	}
	b, err := c.basket()
	if err != nil {
		return nil, err
	}
	for i := range b.Items {
		it := b.Items[i]
		body := map[string]any{
			"items": []map[string]any{{
				"product_uid":          it.Product.SKU,
				"quantity":             0,
				"uom":                  "ea",
				"selected_catchweight": "",
				"item_uid":             it.ItemUID,
				"decreasing_quantity":  true,
			}},
		}
		if err := c.sendBasket("PUT", baseURL+"/basket/v2/basket?"+c.basketParams(), body); err != nil {
			return nil, err
		}
	}
	return c.CartGet()
}
