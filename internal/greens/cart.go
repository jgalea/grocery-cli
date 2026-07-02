package greens

// Authenticated cart for Greens. Greens has NO guest cart (the anonymous
// zero-GUID cart 500s server-side), so cart ops need the user's own logged-in
// session: they paste their browser Cookie, and the CLI reads the real cart id
// and token the logged-in page carries, then drives the store's own cart API:
//
//	Add:    PUT /apiservices/retail/cartadd?Agent=GREENS&PostType=<cid>&Loc=<loc>&Eid=<eid>&cid=<cart>  body {partcode,quantity}
//	List:   GET /apiservices/retail/cartlist?…same params
//	Remove: GET /apiservices/retail/removeitem?Agent=GREENS&PostType=<cid>&Lid=<lineId>&Eid=<eid>
//
// The account is the user's own; the CLI never checks out. This path is built to
// the reverse-engineered shape but is UNTESTED — Greens has no guest cart to
// verify against, so the exact response shape may need a tweak on first real use.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jgalea/grocery-cli/internal/store"
)

type greensSession struct {
	Cookie string `json:"cookie"`
}

func (c *Client) authPath() string {
	dir := os.Getenv("GROCERY_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grocery")
	}
	return filepath.Join(dir, "auth-greens.json")
}

// SetCookie caches the raw Cookie header from a logged-in Greens browser session.
func (c *Client) SetCookie(cookieHeader string) error {
	cookieHeader = strings.TrimSpace(cookieHeader)
	if cookieHeader == "" {
		return fmt.Errorf("empty cookie header")
	}
	if err := os.MkdirAll(filepath.Dir(c.authPath()), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(greensSession{Cookie: cookieHeader}, "", "  ")
	return os.WriteFile(c.authPath(), b, 0o600)
}

func (c *Client) loadCookie() string {
	b, err := os.ReadFile(c.authPath())
	if err != nil {
		return ""
	}
	var s greensSession
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s.Cookie)
}

func (c *Client) LoggedIn() bool { return c.loadCookie() != "" }

// cartCtx is the per-session context the logged-in storefront carries in its
// getProductList(tk, location, eid, cid, cart, …) call.
type cartCtx struct {
	tk, loc, eid, cid, cart string
	cookie                  string
}

var glArgsRe = regexp.MustCompile(`getProductList\('([^']*)','([^']*)','([^']*)','([^']*)','([^']*)'`)

// cartCtx fetches a logged-in page and reads the real cart id + token from it.
func (c *Client) cartContext() (*cartCtx, error) {
	cookie := c.loadCookie()
	if cookie == "" {
		return nil, fmt.Errorf("not logged in; run `grocery --store greens login` and paste your Greens browser Cookie")
	}
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/products?cat=Beverages", nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Cookie", cookie)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	m := glArgsRe.FindSubmatch(body)
	if m == nil {
		return nil, fmt.Errorf("could not read cart session from Greens (is the cookie still valid?)")
	}
	return &cartCtx{
		tk: string(m[1]), loc: string(m[2]), eid: string(m[3]),
		cid: string(m[4]), cart: string(m[5]), cookie: cookie,
	}, nil
}

func (cx *cartCtx) params() url.Values {
	q := url.Values{}
	q.Set("Agent", "GREENS")
	q.Set("PostType", cx.cid)
	q.Set("Loc", cx.loc)
	q.Set("Eid", cx.eid)
	q.Set("cid", cx.cart)
	return q
}

func (c *Client) cartDo(method, action string, q url.Values, body io.Reader, cx *cartCtx, out any) error {
	req, _ := http.NewRequest(method, baseURL+"/apiservices/retail/"+action+"?"+q.Encode(), body)
	req.Header.Set("Authorization", "Bearer "+cx.tk)
	req.Header.Set("Cookie", cx.cookie)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := string(raw)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("%s: http %d: %s", action, resp.StatusCode, msg)
	}
	if out != nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, out) // tolerant: cart list shape is unverified
	}
	return nil
}

// findItems recursively locates the first array of product-like objects.
func findItems(v any) []map[string]any {
	switch t := v.(type) {
	case []any:
		var out []map[string]any
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		if len(out) > 0 {
			return out
		}
	case map[string]any:
		for _, val := range t {
			if got := findItems(val); got != nil {
				return got
			}
		}
	}
	return nil
}

func pick(m map[string]any, keys ...string) any {
	for _, k := range keys {
		for mk, val := range m {
			if strings.EqualFold(mk, k) {
				return val
			}
		}
	}
	return nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	}
	return ""
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		var f float64
		fmt.Sscanf(strings.TrimSpace(t), "%f", &f)
		return f
	}
	return 0
}

func (c *Client) CartGet() (*store.Cart, error) {
	cx, err := c.cartContext()
	if err != nil {
		return nil, err
	}
	var raw any
	if err := c.cartDo(http.MethodGet, "cartlist", cx.params(), nil, cx, &raw); err != nil {
		return nil, err
	}
	cart := &store.Cart{Currency: "EUR"}
	for _, it := range findItems(raw) {
		line := store.CartLine{
			ID:    asString(pick(it, "PART_NUMBER", "partcode", "partNumber", "Part")),
			Name:  strings.TrimSpace(asString(pick(it, "PART_DESCRIPTION", "description", "Name"))),
			Qty:   asFloat(pick(it, "QUANTITY", "quantity", "Qty")),
			Price: asFloat(pick(it, "SALES_PRICE", "price", "Price")),
		}
		if line.ID == "" {
			continue
		}
		if line.Qty == 0 {
			line.Qty = 1
		}
		cart.Lines = append(cart.Lines, line)
		cart.Total += line.Price * line.Qty
		cart.Count++
	}
	return cart, nil
}

func (c *Client) cartAddOnce(cx *cartCtx, partcode string, qty float64) error {
	body, _ := json.Marshal(map[string]any{"partcode": partcode, "quantity": qty})
	return c.cartDo(http.MethodPut, "cartadd", cx.params(), bytes.NewReader(body), cx, nil)
}

func (c *Client) CartAdd(productID string, qty float64) (*store.Cart, error) {
	cx, err := c.cartContext()
	if err != nil {
		return nil, err
	}
	if err := c.cartAddOnce(cx, productID, qty); err != nil {
		return nil, err
	}
	return c.CartGet()
}

// removeByPart finds a product's line id in the current cart and removes it.
func (c *Client) removeByPart(cx *cartCtx, productID string) error {
	var raw any
	if err := c.cartDo(http.MethodGet, "cartlist", cx.params(), nil, cx, &raw); err != nil {
		return err
	}
	for _, it := range findItems(raw) {
		if strings.EqualFold(asString(pick(it, "PART_NUMBER", "partcode", "partNumber", "Part")), productID) {
			lid := asString(pick(it, "Lid", "LINE_ID", "LineId", "lineId", "ID"))
			if lid == "" {
				return fmt.Errorf("found the line but no line id to remove it by")
			}
			q := url.Values{}
			q.Set("Agent", "GREENS")
			q.Set("PostType", cx.cid)
			q.Set("Lid", lid)
			q.Set("Eid", cx.eid)
			return c.cartDo(http.MethodGet, "removeitem", q, nil, cx, nil)
		}
	}
	return nil
}

// CartSet writes an absolute quantity: cartadd is additive, so it removes the
// existing line first, then adds the target (0 just removes).
func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	cx, err := c.cartContext()
	if err != nil {
		return nil, err
	}
	if err := c.removeByPart(cx, productID); err != nil {
		return nil, err
	}
	if qty > 0 {
		if err := c.cartAddOnce(cx, productID, qty); err != nil {
			return nil, err
		}
	}
	return c.CartGet()
}

func (c *Client) CartClear() (*store.Cart, error) {
	cx, err := c.cartContext()
	if err != nil {
		return nil, err
	}
	var raw any
	if err := c.cartDo(http.MethodGet, "cartlist", cx.params(), nil, cx, &raw); err != nil {
		return nil, err
	}
	for _, it := range findItems(raw) {
		lid := asString(pick(it, "Lid", "LINE_ID", "LineId", "lineId", "ID"))
		if lid == "" {
			continue
		}
		q := url.Values{}
		q.Set("Agent", "GREENS")
		q.Set("PostType", cx.cid)
		q.Set("Lid", lid)
		q.Set("Eid", cx.eid)
		_ = c.cartDo(http.MethodGet, "removeitem", q, nil, cx, nil)
	}
	return c.CartGet()
}
