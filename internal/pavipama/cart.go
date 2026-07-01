package pavipama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jgalea/grocery-cli/internal/store"
)

// PAVI/PAMA's read API is open, but the cart is per-account and gated behind a
// login: the storefront SPA obtains a JWT from POST /cli/profiles/login and sends
// it as `Authorization: Bearer <jwt>` on every cart call. Unauthenticated cart
// requests 404. There's no cookie session — auth is purely the bearer token.
//
// The CLI can't run the password login flow for the user (it would have to hold
// the password), so this adapter follows the CookieAuth pattern: the user copies
// the bearer token from a logged-in browser session (DevTools → Network → any
// /api/cli/ecommerce/cart request → Authorization header) and the CLI caches it.
// The cart is filled but never checked out — the user reviews and pays online.
//
// The cart API keys lines on the product barcode (EAN), not the internal UUID,
// so cart add/set/get operate on barcodes.

const terminalID = "9095bfc3-2dad-44dc-89e0-b9f232542f32"

// session is the machine-managed auth cache (the bearer token).
type session struct {
	Token string `json:"token"`
}

func (c *Client) authPath() string {
	dir := os.Getenv("GROCERY_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grocery")
	}
	return filepath.Join(dir, "auth-"+c.key+".json")
}

// SetCookie caches the bearer token lifted from a logged-in browser session. It
// accepts a raw JWT, a "Bearer <jwt>" value, or a full "Authorization: Bearer
// <jwt>" header and normalises to the bare token.
func (c *Client) SetCookie(cookieHeader string) error {
	tok := normalizeToken(cookieHeader)
	if tok == "" {
		return fmt.Errorf("empty token — paste the Authorization bearer token from a logged-in pavipama.com.mt browser request")
	}
	if err := os.MkdirAll(filepath.Dir(c.authPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(session{Token: tok}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.authPath(), b, 0o600)
}

func normalizeToken(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(strings.ToLower(v), "bearer "); i >= 0 {
		v = v[i+len("bearer "):]
	}
	return strings.TrimSpace(v)
}

// loadToken reads the cached bearer token. Returns false when none is cached.
func (c *Client) loadToken() (string, bool) {
	b, err := os.ReadFile(c.authPath())
	if err != nil {
		return "", false
	}
	var s session
	if json.Unmarshal(b, &s) != nil || strings.TrimSpace(s.Token) == "" {
		return "", false
	}
	return s.Token, true
}

// LoggedIn reports whether a usable cached session exists.
func (c *Client) LoggedIn() bool {
	_, ok := c.loadToken()
	return ok
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// storeID / deliveryMode pick where a fresh cart is opened. The PAMA Mosta store
// (200530) with click-and-collect is a sensible default that needs no address;
// both are overridable for users who shop a different store or want delivery.
func storeID() string {
	if v := os.Getenv("GROCERY_PAVIPAMA_STORE"); v != "" {
		return v
	}
	return "200530"
}

func deliveryMode() string {
	if v := os.Getenv("GROCERY_PAVIPAMA_DELIVERY"); v != "" {
		return v
	}
	return "IN_STORE"
}

// --- cart JSON shapes ---

// apiEnvelope wraps every /cli response. responseCode 0 means success.
type apiEnvelope struct {
	ResponseCode int    `json:"responseCode"`
	ErrorMessage string `json:"errorMessage"`
}

func envErr(e apiEnvelope) string {
	if e.ErrorMessage != "" {
		return e.ErrorMessage
	}
	return fmt.Sprintf("responseCode %d", e.ResponseCode)
}

// paviCartLine is one line in the cart. Lines reuse the product shape, so a
// piece-priced item carries its count in amount and a weight-priced item in
// weight; purchaseUm ("PZ" vs a weight unit) says which.
type paviCartLine struct {
	ID          string  `json:"id"`
	Barcode     string  `json:"barcode"`
	Ref         string  `json:"ref"`
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Weight      float64 `json:"weight"`
	Um          string  `json:"um"`
	PurchaseUm  string  `json:"purchaseUm"`
	Price       float64 `json:"price"`
	NetPrice    float64 `json:"netPrice"`
	RowNetPrice float64 `json:"rowNetPrice"`
}

func (l paviCartLine) qty() float64 {
	if !strings.EqualFold(l.PurchaseUm, "PZ") && l.Weight > 0 {
		return l.Weight
	}
	return l.Amount
}

func (l paviCartLine) unitPrice() float64 {
	if l.NetPrice > 0 {
		return l.NetPrice
	}
	return l.Price
}

func (l paviCartLine) key() string {
	if l.Barcode != "" {
		return l.Barcode
	}
	if l.Ref != "" {
		return l.Ref
	}
	return l.ID
}

type paviCart struct {
	ID       string         `json:"id"`
	StoreID  string         `json:"storeId"`
	NetTotal float64        `json:"netTotal"`
	Total    float64        `json:"total"`
	Items    []paviCartLine `json:"items"`
}

func (pc *paviCart) toStore() *store.Cart {
	out := &store.Cart{Currency: "EUR"}
	if pc == nil {
		return out
	}
	for _, l := range pc.Items {
		out.Lines = append(out.Lines, store.CartLine{
			ID:    l.key(),
			Name:  strings.TrimSpace(l.Description),
			Qty:   l.qty(),
			Price: l.unitPrice(),
		})
		out.Count++
	}
	out.Total = pc.NetTotal
	if out.Total == 0 {
		for _, l := range pc.Items {
			if l.RowNetPrice > 0 {
				out.Total += l.RowNetPrice
			} else {
				out.Total += l.unitPrice() * l.qty()
			}
		}
	}
	return out
}

// lineFor returns the current quantity and purchase unit of the line whose
// barcode/ref/id matches id. Missing lines default to "PZ" (piece), which is the
// unit for the overwhelming majority of the catalogue.
func lineFor(pc *paviCart, id string) (qty float64, um string) {
	um = "PZ"
	if pc == nil {
		return 0, um
	}
	for _, l := range pc.Items {
		if l.Barcode == id || l.Ref == id || l.ID == id {
			if l.PurchaseUm != "" {
				um = l.PurchaseUm
			}
			return l.qty(), um
		}
	}
	return 0, um
}

// --- HTTP ---

// cartDo performs an authenticated cart request, decoding into out.
func (c *Client) cartDo(method, path string, body, out any) error {
	tok, ok := c.loadToken()
	if !ok {
		return fmt.Errorf("not logged in; run `grocery --store %s login` and paste the Authorization bearer token from a logged-in pavipama.com.mt browser session", c.key)
	}
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, baseURL+path, rdr)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("cart request failed (http %d) — the session is missing or expired; run `grocery --store %s login` again with a fresh token", resp.StatusCode, c.key)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// currentCart fetches the account's active cart. Returns (nil, nil) when there
// is no open cart yet (a non-zero responseCode), which callers treat as empty.
func (c *Client) currentCart() (*paviCart, error) {
	var r struct {
		apiEnvelope
		Data *paviCart `json:"data"`
	}
	if err := c.cartDo(http.MethodGet, "/cli/ecommerce/cart/current", nil, &r); err != nil {
		return nil, err
	}
	if r.ResponseCode != 0 {
		return nil, nil
	}
	return r.Data, nil
}

// openCart creates a fresh cart for the default store/delivery mode.
func (c *Client) openCart() (*paviCart, error) {
	body := map[string]any{
		"storeId":      storeID(),
		"deliveryMode": deliveryMode(),
		"addressId":    "",
		"terminalType": "WEB",
		"terminalId":   terminalID,
	}
	var r struct {
		apiEnvelope
		Data struct {
			Cart *paviCart `json:"cart"`
		} `json:"data"`
	}
	if err := c.cartDo(http.MethodPost, "/cli/ecommerce/cart/open", body, &r); err != nil {
		return nil, err
	}
	if r.ResponseCode != 0 || r.Data.Cart == nil {
		return nil, fmt.Errorf("could not open a cart (%s) — set GROCERY_PAVIPAMA_STORE/GROCERY_PAVIPAMA_DELIVERY, or open a cart on pavipama.com.mt first", envErr(r.apiEnvelope))
	}
	return r.Data.Cart, nil
}

// ensureCart returns the active cart, opening one if the account has none.
func (c *Client) ensureCart() (*paviCart, error) {
	pc, err := c.currentCart()
	if err != nil {
		return nil, err
	}
	if pc != nil && pc.ID != "" {
		return pc, nil
	}
	return c.openCart()
}

// storeItem sets a line to an absolute quantity (the same call the web app's
// add/stepper makes: amount for piece items, weight for weight items).
func (c *Client) storeItem(cartID, barcode string, qty float64, um string) (*paviCart, error) {
	body := map[string]any{"cartId": cartID, "barcode": barcode, "preview": false}
	if um == "" || strings.EqualFold(um, "PZ") {
		body["amount"] = qty
	} else {
		body["weight"] = qty
	}
	var r struct {
		apiEnvelope
		Data struct {
			Cart *paviCart `json:"cart"`
		} `json:"data"`
	}
	if err := c.cartDo(http.MethodPost, "/cli/ecommerce/cart/store", body, &r); err != nil {
		return nil, err
	}
	if r.ResponseCode != 0 {
		return nil, fmt.Errorf("pavipama: add failed: %s", envErr(r.apiEnvelope))
	}
	return r.Data.Cart, nil
}

// removeItem deletes a line from the cart.
func (c *Client) removeItem(cartID, barcode string) (*paviCart, error) {
	body := map[string]any{"cartId": cartID, "barcode": barcode, "preview": false}
	var r struct {
		apiEnvelope
		Data struct {
			Cart *paviCart `json:"cart"`
		} `json:"data"`
	}
	if err := c.cartDo(http.MethodPost, "/cli/ecommerce/cart/delete", body, &r); err != nil {
		return nil, err
	}
	if r.ResponseCode != 0 {
		return nil, fmt.Errorf("pavipama: remove failed: %s", envErr(r.apiEnvelope))
	}
	return r.Data.Cart, nil
}

// finalize prefers the cart echoed by a write; falls back to a fresh read.
func (c *Client) finalize(pc *paviCart) (*store.Cart, error) {
	if pc != nil {
		return pc.toStore(), nil
	}
	return c.CartGet()
}

// --- store.Carter ---

func (c *Client) CartGet() (*store.Cart, error) {
	pc, err := c.currentCart()
	if err != nil {
		return nil, err
	}
	return pc.toStore(), nil
}

func (c *Client) CartAdd(productID string, qty float64) (*store.Cart, error) {
	pc, err := c.ensureCart()
	if err != nil {
		return nil, err
	}
	cur, um := lineFor(pc, productID)
	resp, err := c.storeItem(pc.ID, productID, cur+qty, um)
	if err != nil {
		return nil, err
	}
	c.log("%s: cart add %s ×%v", c.key, productID, qty)
	return c.finalize(resp)
}

func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	pc, err := c.ensureCart()
	if err != nil {
		return nil, err
	}
	_, um := lineFor(pc, productID)
	if qty <= 0 {
		resp, rerr := c.removeItem(pc.ID, productID)
		if rerr != nil {
			return nil, rerr
		}
		return c.finalize(resp)
	}
	resp, err := c.storeItem(pc.ID, productID, qty, um)
	if err != nil {
		return nil, err
	}
	c.log("%s: cart set %s =%v", c.key, productID, qty)
	return c.finalize(resp)
}

// CartClear abandons the active cart (the web app's "abort"), which empties it.
func (c *Client) CartClear() (*store.Cart, error) {
	pc, err := c.currentCart()
	if err != nil {
		return nil, err
	}
	if pc == nil || pc.ID == "" {
		return &store.Cart{Currency: "EUR"}, nil
	}
	var r struct {
		apiEnvelope
	}
	if err := c.cartDo(http.MethodPost, "/cli/ecommerce/cart/abort", map[string]any{"cartId": pc.ID}, &r); err != nil {
		return nil, err
	}
	if r.ResponseCode != 0 {
		return nil, fmt.Errorf("pavipama: clear failed: %s", envErr(r.apiEnvelope))
	}
	return c.CartGet()
}
