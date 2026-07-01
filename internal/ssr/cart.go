package ssr

// Authenticated cart for classic SFRA (Salesforce Commerce Cloud) storefronts:
// Continente, Pingo Doce and Auchan. They share one controller family under
//
//	/on/demandware.store/Sites-<SiteID>-Site/<locale>/Cart-*
//
// verified live against Continente (guest cart). The account is the user's own;
// the CLI fills the cart but never checks out — the user reviews and pays in the
// browser.
//
// Cart mechanics (as observed on the live storefront):
//
//   - Add:    POST Cart-AddProduct       form  pid=<id>&quantity=<n>   (additive)
//   - Read:   GET  Cart-MiniCartShow     → {basket:{itemsSortedByBrand[],totals}}
//   - Update: GET  Cart-UpdateQuantity   ?pid=&quantity=&step=&uuid=<UUID>&dimension=<unit>&isCart=false
//   - Remove: GET  Cart-RemoveProductLineItem ?pid=&uuid=<UUID>
//
// The line handle the write endpoints key on is the item's *upper-case* "UUID"
// field (the product-line-item UUID), not the lower-case "uuid"; passing the
// wrong one 500s. Add/Update/Remove are not CSRF-protected for these carts, so
// no CSRF token is fetched. Update with quantity=0 clamps to the line minimum
// rather than removing, so CartSet routes a zero quantity to Remove.
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

// cartState holds the per-store cart session. The ssr.Client struct carries no
// cookie/jar fields (and this file must not touch ssr.go), so session state
// lives here, keyed by store key, for the lifetime of the process.
type cartState struct {
	mu          sync.Mutex
	loaded      bool         // whether the on-disk cookie has been read
	cookie      string       // pasted browser Cookie header (authed); "" = guest
	authClient  *http.Client // no jar: replays the pasted Cookie header
	guestClient *http.Client // in-memory jar: guest session
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

// SetCookie caches the raw Cookie header copied from a logged-in browser session
// so cart reads/writes run against the user's own account.
// cartEnabled gates the cart to stores whose write path is verified. The SFRA
// code is shared, but Pingo Doce needs a postal-code precondition and Auchan
// needs CSRF, so only Continente is enabled until those are handled.
func (c *Client) cartEnabled() error {
	if !c.cfg.Cart {
		return fmt.Errorf("cart isn't wired for %s yet (%w)", c.cfg.Key, store.ErrUnsupported)
	}
	return nil
}

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
// account cart. Cart-disabled stores (unhandled preconditions) report false.
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

// --- cart JSON shapes ---

// miniCart is the Cart-MiniCartShow response: the cart nested under "basket",
// with line items grouped by brand.
type miniCart struct {
	Basket struct {
		ItemsSortedByBrand []struct {
			Items []miniItem `json:"items"`
		} `json:"itemsSortedByBrand"`
		Totals struct {
			GrandTotalNumber flexFloat `json:"grandTotalNumber"`
		} `json:"totals"`
	} `json:"basket"`
}

type miniItem struct {
	ID          string `json:"id"`
	UUID        string `json:"UUID"` // product-line-item UUID (write endpoints key on this)
	ProductName string `json:"productName"`
	// secondaryQuantity is the unit count the storefront stepper shows and the
	// quantity the update endpoint operates on.
	SecondaryQuantity flexFloat `json:"secondaryQuantity"`
	SelectedDimension string    `json:"selectedDimension"`
	Price             struct {
		Sales struct {
			Value    flexFloat `json:"value"`
			Currency string    `json:"currency"`
		} `json:"sales"`
	} `json:"price"`
	MeasurementInfo struct {
		QuantityConversionRates struct {
			StepQuantity flexFloat `json:"stepQuantity"`
		} `json:"quantityConversionRates"`
	} `json:"measurementInfo"`
}

// line is the internal per-item record; it carries the write handles (UUID,
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

func (mc *miniCart) lines() []line {
	var out []line
	for _, g := range mc.Basket.ItemsSortedByBrand {
		for _, it := range g.Items {
			out = append(out, line{
				id:        it.ID,
				uuid:      it.UUID,
				name:      strings.TrimSpace(it.ProductName),
				qty:       float64(it.SecondaryQuantity),
				price:     float64(it.Price.Sales.Value),
				step:      float64(it.MeasurementInfo.QuantityConversionRates.StepQuantity),
				dimension: it.SelectedDimension,
			})
		}
	}
	return out
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

// getMini fetches and decodes the current cart.
func (c *Client) getMini() (*miniCart, error) {
	var mc miniCart
	if err := c.cartDo(http.MethodGet, "Cart-MiniCartShow", nil, nil, &mc); err != nil {
		return nil, err
	}
	return &mc, nil
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
	mc, err := c.getMini()
	if err != nil {
		return nil, err
	}
	return c.toStore(mc.lines(), float64(mc.Basket.Totals.GrandTotalNumber)), nil
}

func fmtQty(q float64) string { return strconv.FormatFloat(q, 'f', -1, 64) }

// addProduct posts an additive add-to-cart.
func (c *Client) addProduct(productID string, qty float64) error {
	form := url.Values{}
	form.Set("pid", productID)
	form.Set("quantity", fmtQty(qty))
	return c.cartDo(http.MethodPost, "Cart-AddProduct", nil, form, nil)
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
	q := url.Values{}
	q.Set("pid", l.id)
	q.Set("uuid", l.uuid)
	return c.cartDo(http.MethodGet, "Cart-RemoveProductLineItem", q, nil, nil)
}

// updateLine sets a line to an absolute quantity via Cart-UpdateQuantity.
func (c *Client) updateLine(l *line, qty float64) error {
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

// CartSet sets a product's line to an absolute quantity. Update-quantity clamps
// a zero to the line minimum instead of removing, so a zero (or a not-yet-in-cart
// line) is routed to Remove / Add respectively.
func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	if err := c.cartEnabled(); err != nil {
		return nil, err
	}
	mc, err := c.getMini()
	if err != nil {
		return nil, err
	}
	existing := findLine(mc.lines(), productID)

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
		if err := c.updateLine(existing, qty); err != nil {
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
	mc, err := c.getMini()
	if err != nil {
		return nil, err
	}
	lines := mc.lines()
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
