package woocommerce

// This file adds an authenticated shopping cart on top of the read-only Store API
// adapter. The WooCommerce Store API (wc/store/v1) exposes a cart that works for a
// guest with no login: each cart is identified by the `Cart-Token` header the
// server hands back, and writes must echo the `Nonce` header from a prior read.
// We keep a cookie jar for the session and persist the Cart-Token so the same
// guest cart survives across separate CLI invocations.
//
// When the user pastes their logged-in browser Cookie header (SetCookie), we
// replay it on every request so items land in their own account cart instead of
// an anonymous one. Either way the CLI never checks out — the user reviews and
// pays in the browser.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jgalea/grocery-cli/internal/store"
)

// wcSession is the machine-managed auth/cart cache stored at
// ~/.grocery/auth-<key>.json (0600). Cookie is the raw account Cookie header the
// user optionally supplies; CartToken is the guest cart identity we capture.
type wcSession struct {
	Cookie    string `json:"cookie,omitempty"`
	CartToken string `json:"cart_token,omitempty"`
}

var (
	sessMu    sync.Mutex
	sessCache = map[string]*wcSession{}
)

func (c *Client) authPath() string {
	dir := os.Getenv("GROCERY_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grocery")
	}
	return filepath.Join(dir, "auth-"+c.cfg.Key+".json")
}

// loadSession returns the cached session, reading it from disk once. It never
// returns nil so callers can read fields unconditionally.
func (c *Client) loadSession() *wcSession {
	sessMu.Lock()
	defer sessMu.Unlock()
	if s := sessCache[c.cfg.Key]; s != nil {
		return s
	}
	s := &wcSession{}
	if b, err := os.ReadFile(c.authPath()); err == nil {
		_ = json.Unmarshal(b, s)
	}
	sessCache[c.cfg.Key] = s
	return s
}

func (c *Client) saveSession(s *wcSession) error {
	sessMu.Lock()
	sessCache[c.cfg.Key] = s
	sessMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.authPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.authPath(), b, 0o600)
}

// rememberCartToken persists a freshly issued guest Cart-Token, keeping any
// account cookie already on file.
func (c *Client) rememberCartToken(token string) {
	s := c.loadSession()
	if s.CartToken == token {
		return
	}
	updated := &wcSession{Cookie: s.Cookie, CartToken: token}
	_ = c.saveSession(updated)
}

// SetCookie caches the raw Cookie header copied from a logged-in browser session
// so cart writes land in the user's account cart rather than a guest one.
func (c *Client) SetCookie(cookieHeader string) error {
	cookieHeader = strings.TrimSpace(cookieHeader)
	if cookieHeader == "" {
		return fmt.Errorf("empty cookie header")
	}
	s := c.loadSession()
	return c.saveSession(&wcSession{Cookie: cookieHeader, CartToken: s.CartToken})
}

// LoggedIn always reports true: the Store API cart works for a guest, so the cart
// commands can always run. Without a cached account cookie the items go into an
// anonymous guest cart; with one they go into the user's own account cart.
func (c *Client) LoggedIn() bool { return true }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

func (c *Client) ensureJar() {
	if c.http.Jar == nil {
		if jar, err := cookiejar.New(nil); err == nil {
			c.http.Jar = jar
		}
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// cartRequest performs one Store API cart call, replaying the account cookie and
// guest Cart-Token, attaching the write Nonce when given, and capturing any
// rotated Cart-Token from the response.
func (c *Client) cartRequest(method, path string, body any, nonce string) (http.Header, []byte, error) {
	c.ensureJar()
	s := c.loadSession()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.storeAPI(path), rdr)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.Cookie != "" {
		req.Header.Set("Cookie", s.Cookie)
	}
	if s.CartToken != "" {
		req.Header.Set("Cart-Token", s.CartToken)
	}
	if nonce != "" {
		req.Header.Set("Nonce", nonce)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if tok := resp.Header.Get("Cart-Token"); tok != "" {
		c.rememberCartToken(tok)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.Header, raw, fmt.Errorf("http %d: %s", resp.StatusCode, clip(string(raw), 200))
	}
	return resp.Header, raw, nil
}

// --- cart JSON shapes ---

// wcCartItem is one line in the Store API cart. prices reuses wcPrices; the item
// id is the numeric product id used by add-item, key is the per-line handle used
// by update-item / remove-item.
type wcCartItem struct {
	Key      string   `json:"key"`
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Quantity float64  `json:"quantity"`
	Prices   wcPrices `json:"prices"`
}

type wcTotals struct {
	TotalPrice   string `json:"total_price"`
	CurrencyCode string `json:"currency_code"`
	MinorUnit    int    `json:"currency_minor_unit"`
}

type wcCart struct {
	Items  []wcCartItem `json:"items"`
	Totals wcTotals     `json:"totals"`
}

func (c *Client) cartToStore(rc *wcCart) *store.Cart {
	out := &store.Cart{Currency: c.cfg.Currency}
	for _, it := range rc.Items {
		out.Lines = append(out.Lines, store.CartLine{
			ID:    strconv.Itoa(it.ID),
			Name:  strings.TrimSpace(it.Name),
			Qty:   it.Quantity,
			Price: minorToDecimal(it.Prices.Price, it.Prices.MinorUnit),
		})
		out.Count++
	}
	out.Total = minorToDecimal(rc.Totals.TotalPrice, rc.Totals.MinorUnit)
	if rc.Totals.CurrencyCode != "" {
		out.Currency = rc.Totals.CurrencyCode
	}
	return out
}

// getCart fetches the current cart and returns it with the fresh write nonce.
func (c *Client) getCart() (*wcCart, string, error) {
	h, raw, err := c.cartRequest(http.MethodGet, "/cart", nil, "")
	if err != nil {
		return nil, "", err
	}
	var rc wcCart
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, "", err
	}
	return &rc, h.Get("Nonce"), nil
}

func numericID(productID string) (int, error) {
	id, err := strconv.Atoi(strings.TrimSpace(productID))
	if err != nil {
		return 0, fmt.Errorf("woocommerce cart needs a numeric product id, got %q", productID)
	}
	return id, nil
}

// CartGet returns the current cart (guest or account).
func (c *Client) CartGet() (*store.Cart, error) {
	rc, _, err := c.getCart()
	if err != nil {
		return nil, err
	}
	return c.cartToStore(rc), nil
}

// CartAdd increments a product's line by qty (add-item is additive when the line
// already exists).
func (c *Client) CartAdd(productID string, qty float64) (*store.Cart, error) {
	id, err := numericID(productID)
	if err != nil {
		return nil, err
	}
	_, nonce, err := c.getCart()
	if err != nil {
		return nil, err
	}
	_, raw, err := c.cartRequest(http.MethodPost, "/cart/add-item",
		map[string]any{"id": id, "quantity": qty}, nonce)
	if err != nil {
		return nil, err
	}
	var rc wcCart
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, err
	}
	c.log("%s: added %d x%v to cart", c.cfg.Key, id, qty)
	return c.cartToStore(&rc), nil
}

// CartSet sets a product's line to an absolute quantity; qty <= 0 removes it.
func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	id, err := numericID(productID)
	if err != nil {
		return nil, err
	}
	rc, nonce, err := c.getCart()
	if err != nil {
		return nil, err
	}
	key := ""
	for _, it := range rc.Items {
		if it.ID == id {
			key = it.Key
			break
		}
	}
	if key == "" {
		if qty <= 0 {
			return c.cartToStore(rc), nil
		}
		_, raw, err := c.cartRequest(http.MethodPost, "/cart/add-item",
			map[string]any{"id": id, "quantity": qty}, nonce)
		if err != nil {
			return nil, err
		}
		return c.decodeCart(raw)
	}
	if qty <= 0 {
		_, raw, err := c.cartRequest(http.MethodPost, "/cart/remove-item",
			map[string]any{"key": key}, nonce)
		if err != nil {
			return nil, err
		}
		return c.decodeCart(raw)
	}
	_, raw, err := c.cartRequest(http.MethodPost, "/cart/update-item",
		map[string]any{"key": key, "quantity": qty}, nonce)
	if err != nil {
		return nil, err
	}
	return c.decodeCart(raw)
}

// CartClear empties the cart. It uses the Store API's bulk DELETE /cart/items and
// falls back to removing each line if that endpoint is unavailable.
func (c *Client) CartClear() (*store.Cart, error) {
	rc, nonce, err := c.getCart()
	if err != nil {
		return nil, err
	}
	// DELETE /cart/items empties the whole cart in one call; its body is the
	// (now empty) items array rather than a full cart, so re-read for the total.
	if _, _, err := c.cartRequest(http.MethodDelete, "/cart/items", nil, nonce); err == nil {
		return c.CartGet()
	}
	for _, it := range rc.Items {
		h, _, err := c.cartRequest(http.MethodPost, "/cart/remove-item",
			map[string]any{"key": it.Key}, nonce)
		if err != nil {
			return nil, err
		}
		if n := h.Get("Nonce"); n != "" {
			nonce = n
		}
	}
	return c.CartGet()
}

func (c *Client) decodeCart(raw []byte) (*store.Cart, error) {
	var rc wcCart
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, err
	}
	return c.cartToStore(&rc), nil
}
