package ssr

// Authenticated cart for classic SFRA (Salesforce Commerce Cloud) storefronts:
// Continente, Pingo Doce and Auchan. They share one controller family under
//
//	/on/demandware.store/Sites-<SiteID>-Site/<locale>/Cart-*
//
// verified live against all three (guest cart). The account is the user's own;
// the CLI fills the cart but never checks out — the user reviews and pays in the
// browser.
//
// Cart mechanics (as observed on the live storefronts):
//
//   - Add:    POST Cart-AddProduct           form  pid=<id>&quantity=<n>   (additive)
//   - Read:   GET  Cart-MiniCartShow         → {basket:{itemsSortedByBrand[],totals}}   (Continente)
//     or GET  Cart-Get                   → {items[],totals}                        (Auchan, Pingo Doce)
//   - Update: GET  Cart-UpdateQuantity       ?pid=&quantity=&step=&uuid=<UUID>&dimension=<unit>&isCart=false
//   - Remove: GET  Cart-RemoveProductLineItem ?pid=&uuid=<UUID>
//
// The line handle the write endpoints key on is the item's product-line-item
// UUID. Continente and Pingo Doce expose it as the upper-case "UUID" field;
// Auchan exposes it as "quantitySelector.uuid". Update with quantity=0 clamps to
// the line minimum rather than removing, so CartSet routes a zero quantity to
// Remove.
//
// Store-specific preconditions, handled from Config (see resolveWrite):
//
//   - Continente: none. No CSRF, no delivery gate.
//   - Auchan (NeedsCSRF): writes are CSRF-protected — a token scraped from the
//     cart page is sent as csrf_token. Add also refuses until a delivery area is
//     set (Delivery-SetPostalCode). Cart-MiniCartShow 404s, so the cart is read
//     from Cart-Get. Cart-UpdateQuantity needs the delivery store id, so CartSet
//     changes an existing line by remove+add rather than update.
//   - Pingo Doce (PostalCode): add refuses with {showPostalCode:true} until a
//     home-delivery postal code is set (Delivery-SetPostalCode with home=true).
//
// The read shape is auto-detected: Cart-MiniCartShow is tried first (Continente's
// basket JSON); if it 404s or isn't JSON, Cart-Get's flat shape is used, and the
// working route is cached for the session.
//
// Auth: a guest cart works from an in-memory cookie jar (dwsid/dwanonymous),
// which is what makes anonymous add-to-cart verifiable. For the user's own
// account cart, the CLI replays a Cookie header pasted from a logged-in browser
// session (SetCookie); no password is ever handled.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

// cartState holds the per-store cart session. The ssr.Client struct carries no
// cookie/jar fields (and this file must not touch that part of ssr.go), so
// session state lives here, keyed by store key, for the lifetime of the process.
type cartState struct {
	mu          sync.Mutex
	loaded      bool         // whether the on-disk cookie has been read
	cookie      string       // pasted browser Cookie header (authed); "" = guest
	authClient  *http.Client // no jar: replays the pasted Cookie header
	guestClient *http.Client // in-memory jar: guest session
	csrf        string       // cached CSRF token (NeedsCSRF stores)
	postalDone  bool         // delivery area established this session
	warmed      bool         // guest session cookies seeded (dwsid/dwanonymous)
	readRoute   string       // cached cart read route: "basket" | "flat"
}

var (
	cartStatesMu sync.Mutex
	cartStates   = map[string]*cartState{}
)

// state returns the shared cart state for this store, loading any cached cookie
// from disk on first use.
func (c *Client) state() *cartState {
	cartStatesMu.Lock()
	s := cartStates[c.cfg.Key]
	if s == nil {
		s = &cartState{}
		cartStates[c.cfg.Key] = s
	}
	cartStatesMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		s.loaded = true
		if b, err := os.ReadFile(c.authPath()); err == nil {
			var sess ssrSession
			if json.Unmarshal(b, &sess) == nil {
				s.cookie = strings.TrimSpace(sess.Cookie)
			}
		}
	}
	return s
}

func (c *Client) clog(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// --- authentication (pasted browser Cookie header) ---

type ssrSession struct {
	Cookie string `json:"cookie"`
}

func (c *Client) authPath() string {
	dir := os.Getenv("GROCERY_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grocery")
	}
	return filepath.Join(dir, "auth-"+c.cfg.Key+".json")
}

// cartEnabled gates the cart to stores whose write path is verified.
func (c *Client) cartEnabled() error {
	if !c.cfg.Cart {
		return fmt.Errorf("cart isn't wired for %s yet (%w)", c.cfg.Key, store.ErrUnsupported)
	}
	return nil
}

// SetCookie caches the raw Cookie header copied from a logged-in browser session
// so cart reads/writes run against the user's own account.
func (c *Client) SetCookie(cookieHeader string) error {
	if err := c.cartEnabled(); err != nil {
		return err
	}
	cookieHeader = strings.TrimSpace(cookieHeader)
	if cookieHeader == "" {
		return fmt.Errorf("empty cookie header")
	}
	s := c.state()
	s.mu.Lock()
	s.cookie = cookieHeader
	s.authClient = nil
	s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(c.authPath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ssrSession{Cookie: cookieHeader}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.authPath(), b, 0o600)
}

// LoggedIn reports whether the cart is usable. These stores support a guest cart,
// so any cart-enabled store is "ready"; a pasted cookie upgrades it to the user's
// account cart. Cart-disabled stores report false.
func (c *Client) LoggedIn() bool {
	return c.cfg.Cart
}

// httpClient returns the client for cart calls: a no-jar client that replays the
// pasted Cookie header when authenticated, or a jar-backed client that keeps the
// guest session (dwsid/dwanonymous) across calls otherwise.
func (s *cartState) httpClient() (*http.Client, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie != "" {
		if s.authClient == nil {
			s.authClient = &http.Client{Timeout: 30 * time.Second}
		}
		return s.authClient, s.cookie
	}
	if s.guestClient == nil {
		jar, _ := cookiejar.New(nil)
		s.guestClient = &http.Client{Timeout: 30 * time.Second, Jar: jar}
	}
	return s.guestClient, ""
}

// --- HTTP plumbing ---

func (c *Client) controllerURL(controller string, q url.Values) string {
	u := fmt.Sprintf("%s/on/demandware.store/Sites-%s-Site/%s/%s",
		c.cfg.BaseURL, c.cfg.SiteID, c.cfg.Locale, controller)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// cartDo issues a cart request (GET query, or POST form when form != nil),
// replaying the auth/guest cookie, and decodes the JSON body into out.
func (c *Client) cartDo(method, controller string, q, form url.Values, out any) error {
	client, cookie := c.state().httpClient()

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, c.controllerURL(controller, q), body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", c.cfg.BaseURL+"/")
	req.Header.Set("Origin", c.cfg.BaseURL)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s: http %d", controller, resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s: %w", controller, err)
		}
	}
	return nil
}

// --- CSRF (Auchan) ---

var csrfRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]+)"`)

// csrfToken returns a CSRF token for write requests, scraping it from the cart
// page (Cart-Show renders hidden csrf_token inputs). Tokens are session-scoped
// and reusable, so it's cached; refresh forces a re-fetch.
func (c *Client) csrfToken(refresh bool) (string, error) {
	s := c.state()
	s.mu.Lock()
	if !refresh && s.csrf != "" {
		tok := s.csrf
		s.mu.Unlock()
		return tok, nil
	}
	s.mu.Unlock()

	client, cookie := s.httpClient()
	req, err := http.NewRequest(http.MethodGet, c.controllerURL("Cart-Show", nil), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 24<<20))
	m := csrfRe.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("%s: no csrf token on cart page", c.cfg.Key)
	}
	tok := string(m[1])
	s.mu.Lock()
	s.csrf = tok
	s.mu.Unlock()
	return tok, nil
}

// warmup seeds a guest session's cookies (dwsid/dwanonymous) with one GET to the
// storefront root. The delivery-area POST is rejected (403) from a cold session
// that hasn't established an anonymous customer yet. Only the delivery-gated /
// CSRF stores need it, so Continente's path is left untouched; authed sessions
// already carry the cookies.
func (c *Client) warmup() error {
	if c.postalCode() == "" && !c.cfg.NeedsCSRF {
		return nil
	}
	s := c.state()
	s.mu.Lock()
	if s.warmed || s.cookie != "" {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	client, _ := s.httpClient()
	req, err := http.NewRequest(http.MethodGet, c.cfg.BaseURL+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	s.mu.Lock()
	s.warmed = true
	s.mu.Unlock()
	return nil
}

// --- delivery precondition (Auchan, Pingo Doce) ---

func (c *Client) postalCode() string {
	if v := strings.TrimSpace(os.Getenv("GROCERY_" + strings.ToUpper(strings.ReplaceAll(c.cfg.Key, "-", "")) + "_POSTAL")); v != "" {
		return v
	}
	return strings.TrimSpace(c.cfg.PostalCode)
}

// ensurePostal establishes the delivery area once per session, when the store is
// configured with a postal code. Delivery-SetPostalCode is shared by Auchan and
// Pingo Doce; the home=true&isConfirmed=true fields are what Pingo Doce needs and
// are harmless to Auchan.
func (c *Client) ensurePostal(force bool) error {
	pc := c.postalCode()
	if pc == "" {
		return nil
	}
	if err := c.warmup(); err != nil {
		return err
	}
	s := c.state()
	s.mu.Lock()
	done := s.postalDone
	s.mu.Unlock()
	if done && !force {
		return nil
	}

	form := url.Values{}
	form.Set("postalCode", pc)
	form.Set("home", "true")
	form.Set("isConfirmed", "true")
	if c.cfg.NeedsCSRF {
		tok, err := c.csrfToken(false)
		if err != nil {
			return err
		}
		form.Set("csrf_token", tok)
	}
	if err := c.cartDo(http.MethodPost, "Delivery-SetPostalCode", nil, form, nil); err != nil {
		return err
	}
	s.mu.Lock()
	s.postalDone = true
	s.mu.Unlock()
	return nil
}

// --- cart JSON shapes ---

// cartResp captures both cart read shapes: Continente's nested basket
// (Cart-MiniCartShow) and the flat items[] Auchan/Pingo Doce serve (Cart-Get).
type cartResp struct {
	Basket *struct {
		ItemsSortedByBrand []struct {
			Items []cartItem `json:"items"`
		} `json:"itemsSortedByBrand"`
		Totals cartTotals `json:"totals"`
	} `json:"basket"`
	Items  []cartItem `json:"items"`
	Totals cartTotals `json:"totals"`
}

type cartTotals struct {
	GrandTotalNumber flexFloat `json:"grandTotalNumber"` // Continente (numeric)
	GrandTotal       string    `json:"grandTotal"`       // Auchan / Pingo Doce (formatted "1,82 €")
}

type cartItem struct {
	ID          string `json:"id"`
	UUID        string `json:"UUID"` // Continente / Pingo Doce write handle
	ProductName string `json:"productName"`

	SecondaryQuantity flexFloat `json:"secondaryQuantity"` // Continente stepper qty
	Quantity          flexFloat `json:"quantity"`          // Pingo Doce qty
	SelectedDimension string    `json:"selectedDimension"`

	Price struct {
		Sales struct {
			Value flexFloat `json:"value"`
		} `json:"sales"`
	} `json:"price"`

	MeasurementInfo struct {
		QuantityConversionRates struct {
			StepQuantity flexFloat `json:"stepQuantity"`
		} `json:"quantityConversionRates"`
	} `json:"measurementInfo"` // Continente

	// Auchan
	QuantitySelector struct {
		UUID         string    `json:"uuid"` // Auchan write handle (product-line-item uuid)
		Value        flexFloat `json:"value"`
		StepQuantity flexFloat `json:"stepQuantity"`
	} `json:"quantitySelector"`
	MeasuresInfo struct {
		SalesUnit    string    `json:"salesUnit"`
		StepQuantity flexFloat `json:"stepQuantity"`
	} `json:"measuresInfo"`
}

func firstPos(vals ...float64) float64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func (it cartItem) toLine() line {
	step := firstPos(
		float64(it.MeasurementInfo.QuantityConversionRates.StepQuantity),
		float64(it.QuantitySelector.StepQuantity),
		float64(it.MeasuresInfo.StepQuantity),
	)
	if step <= 0 {
		step = 1
	}
	return line{
		id:        it.ID,
		uuid:      firstStr(it.QuantitySelector.UUID, it.UUID),
		name:      strings.TrimSpace(it.ProductName),
		qty:       firstPos(float64(it.SecondaryQuantity), float64(it.Quantity), float64(it.QuantitySelector.Value)),
		price:     float64(it.Price.Sales.Value),
		step:      step,
		dimension: firstStr(it.SelectedDimension, it.MeasuresInfo.SalesUnit),
	}
}

// line is the internal per-item record; it carries the write handles (uuid,
// step, dimension) that store.CartLine doesn't.
type line struct {
	id        string
	uuid      string
	name      string
	qty       float64
	price     float64
	step      float64
	dimension string
}

func (r *cartResp) lines() []line {
	var out []line
	if r.Basket != nil {
		for _, g := range r.Basket.ItemsSortedByBrand {
			for _, it := range g.Items {
				out = append(out, it.toLine())
			}
		}
		return out
	}
	for _, it := range r.Items {
		out = append(out, it.toLine())
	}
	return out
}

func (r *cartResp) grandTotal() float64 {
	if r.Basket != nil {
		if v := float64(r.Basket.Totals.GrandTotalNumber); v != 0 {
			return v
		}
		return parseEuro(r.Basket.Totals.GrandTotal)
	}
	if v := float64(r.Totals.GrandTotalNumber); v != 0 {
		return v
	}
	return parseEuro(r.Totals.GrandTotal)
}

// parseEuro reads a formatted Portuguese euro amount ("1.234,56 €" / "1,72 €")
// into a float. Portuguese uses "." for thousands and "," for decimals.
func parseEuro(s string) float64 {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == '.' || r == ',' {
			b.WriteRune(r)
		}
	}
	t := b.String()
	if t == "" {
		return 0
	}
	if strings.Contains(t, ",") {
		t = strings.ReplaceAll(t, ".", "")
		t = strings.ReplaceAll(t, ",", ".")
	}
	v, _ := strconv.ParseFloat(t, 64)
	return v
}

func (c *Client) toStore(lines []line, grandTotal float64) *store.Cart {
	out := &store.Cart{Currency: c.cfg.Currency}
	for _, l := range lines {
		out.Lines = append(out.Lines, store.CartLine{ID: l.id, Name: l.name, Qty: l.qty, Price: l.price})
		out.Count++
	}
	out.Total = grandTotal
	return out
}

// getCart fetches and decodes the current cart, auto-detecting the read route.
func (c *Client) getCart() (*cartResp, error) {
	s := c.state()
	s.mu.Lock()
	route := s.readRoute
	s.mu.Unlock()

	if route == "" || route == "basket" {
		var r cartResp
		if err := c.cartDo(http.MethodGet, "Cart-MiniCartShow", nil, nil, &r); err == nil && r.Basket != nil {
			s.mu.Lock()
			s.readRoute = "basket"
			s.mu.Unlock()
			return &r, nil
		}
		if route == "basket" {
			return nil, fmt.Errorf("%s: Cart-MiniCartShow read failed", c.cfg.Key)
		}
	}

	var r cartResp
	if err := c.cartDo(http.MethodGet, "Cart-Get", nil, nil, &r); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.readRoute = "flat"
	s.mu.Unlock()
	return &r, nil
}

func findLine(lines []line, productID string) *line {
	for i := range lines {
		if lines[i].id == productID {
			return &lines[i]
		}
	}
	return nil
}

// --- Carter ---

// CartGet returns the current cart snapshot.
func (c *Client) CartGet() (*store.Cart, error) {
	if err := c.cartEnabled(); err != nil {
		return nil, err
	}
	r, err := c.getCart()
	if err != nil {
		return nil, err
	}
	return c.toStore(r.lines(), r.grandTotal()), nil
}

func fmtQty(q float64) string { return strconv.FormatFloat(q, 'f', -1, 64) }

// addResp is the subset of Cart-AddProduct's JSON that flags an unmet
// precondition (delivery area not set) so add can self-heal.
type addResp struct {
	Error                    bool `json:"error"`
	ShowPostalCode           bool `json:"showPostalCode"`           // Pingo Doce
	ShowDeliveryOptionsModal bool `json:"showDeliveryOptionsModal"` // Auchan
}

func (a addResp) needsDelivery() bool {
	return a.ShowPostalCode || a.ShowDeliveryOptionsModal
}

// addProduct posts an additive add-to-cart, establishing the delivery area first
// when the store needs one and retrying once if the store still asks for it.
func (c *Client) addProduct(productID string, qty float64) error {
	if err := c.ensurePostal(false); err != nil {
		return err
	}
	res, err := c.postAdd(productID, qty)
	if err != nil {
		return err
	}
	if res.needsDelivery() {
		if err := c.ensurePostal(true); err != nil {
			return err
		}
		res, err = c.postAdd(productID, qty)
		if err != nil {
			return err
		}
	}
	if res.needsDelivery() {
		return fmt.Errorf("%s: add rejected — delivery area not accepted (postal %q)", c.cfg.Key, c.postalCode())
	}
	return nil
}

func (c *Client) postAdd(productID string, qty float64) (addResp, error) {
	form := url.Values{}
	form.Set("pid", productID)
	form.Set("quantity", fmtQty(qty))
	if err := c.addCSRF(form); err != nil {
		return addResp{}, err
	}
	var res addResp
	if err := c.cartDo(http.MethodPost, "Cart-AddProduct", nil, form, &res); err != nil {
		return addResp{}, err
	}
	return res, nil
}

// addCSRF injects a fresh-enough csrf_token into a write form for CSRF stores.
func (c *Client) addCSRF(form url.Values) error {
	if !c.cfg.NeedsCSRF {
		return nil
	}
	tok, err := c.csrfToken(false)
	if err != nil {
		return err
	}
	form.Set("csrf_token", tok)
	return nil
}

// CartAdd adds qty of a product to the cart (Cart-AddProduct is additive).
func (c *Client) CartAdd(productID string, qty float64) (*store.Cart, error) {
	if err := c.cartEnabled(); err != nil {
		return nil, err
	}
	if qty <= 0 {
		return c.CartGet()
	}
	if err := c.addProduct(productID, qty); err != nil {
		return nil, err
	}
	return c.CartGet()
}

// removeLine removes one line item by its UUID.
func (c *Client) removeLine(l *line) error {
	if err := c.warmup(); err != nil {
		return err
	}
	q := url.Values{}
	q.Set("pid", l.id)
	q.Set("uuid", l.uuid)
	if c.cfg.NeedsCSRF {
		tok, err := c.csrfToken(false)
		if err != nil {
			return err
		}
		q.Set("csrf_token", tok)
	}
	return c.cartDo(http.MethodGet, "Cart-RemoveProductLineItem", q, nil, nil)
}

// updateLine sets a line to an absolute quantity via Cart-UpdateQuantity.
func (c *Client) updateLine(l *line, qty float64) error {
	if err := c.warmup(); err != nil {
		return err
	}
	step := l.step
	if step <= 0 {
		step = 1
	}
	dimension := l.dimension
	if dimension == "" {
		dimension = "un"
	}
	q := url.Values{}
	q.Set("pid", l.id)
	q.Set("quantity", fmtQty(qty))
	q.Set("step", fmtQty(step))
	q.Set("uuid", l.uuid)
	q.Set("dimension", dimension)
	q.Set("isCart", "false")
	return c.cartDo(http.MethodGet, "Cart-UpdateQuantity", q, nil, nil)
}

// setQuantity moves an existing line to an absolute quantity. Continente and
// Pingo Doce use Cart-UpdateQuantity directly. Auchan's update also requires the
// delivery store id, so CSRF stores set the quantity by remove+add (additive add
// from zero yields the exact quantity), which needs no store-context params.
func (c *Client) setQuantity(l *line, qty float64) error {
	if c.cfg.NeedsCSRF {
		if err := c.removeLine(l); err != nil {
			return err
		}
		return c.addProduct(l.id, qty)
	}
	return c.updateLine(l, qty)
}

// CartSet sets a product's line to an absolute quantity. Update-quantity clamps
// a zero to the line minimum instead of removing, so a zero (or a not-yet-in-cart
// line) is routed to Remove / Add respectively.
func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	if err := c.cartEnabled(); err != nil {
		return nil, err
	}
	r, err := c.getCart()
	if err != nil {
		return nil, err
	}
	existing := findLine(r.lines(), productID)

	switch {
	case qty <= 0:
		if existing != nil {
			if err := c.removeLine(existing); err != nil {
				return nil, err
			}
		}
	case existing == nil:
		if err := c.addProduct(productID, qty); err != nil {
			return nil, err
		}
	default:
		if err := c.setQuantity(existing, qty); err != nil {
			return nil, err
		}
	}
	return c.CartGet()
}

// CartClear removes every line item from the cart.
func (c *Client) CartClear() (*store.Cart, error) {
	if err := c.cartEnabled(); err != nil {
		return nil, err
	}
	r, err := c.getCart()
	if err != nil {
		return nil, err
	}
	lines := r.lines()
	var firstErr error
	for i := range lines {
		if err := c.removeLine(&lines[i]); err != nil {
			c.clog("%s: could not remove line %s (%s): %v", c.cfg.Key, lines[i].id, lines[i].name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return c.CartGet()
}
