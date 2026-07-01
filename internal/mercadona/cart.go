package mercadona

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jgalea/grocery-cli/internal/store"
)

// authSession is the cached authenticated session (the user's own account).
// It is stored separately from the guest read token.
type authSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	CustomerID   string `json:"customer_id"`
	Warehouse    string `json:"warehouse"`
}

func (c *Client) authPath() string {
	dir := os.Getenv("GROCERY_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grocery")
	}
	return filepath.Join(dir, "auth-"+c.cfg.Key+".json")
}

func (c *Client) loadAuth() *authSession {
	if c.auth != nil {
		return c.auth
	}
	b, err := os.ReadFile(c.authPath())
	if err != nil {
		return nil
	}
	var s authSession
	if json.Unmarshal(b, &s) != nil || s.AccessToken == "" {
		return nil
	}
	c.auth = &s
	return c.auth
}

func (c *Client) saveAuth(s *authSession) {
	c.auth = s
	_ = os.MkdirAll(filepath.Dir(c.authPath()), 0o700)
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		_ = os.WriteFile(c.authPath(), b, 0o600)
	}
}

func (c *Client) LoggedIn() bool { return c.loadAuth() != nil }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Login exchanges the user's own credentials for access + refresh tokens and
// caches them. The CLI never keeps the password.
func (c *Client) Login(username, password string) error {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req, _ := http.NewRequest(http.MethodPost, c.cfg.BaseURL+"/api/auth/tokens/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("login failed (http %d) — check the email/password, or Mercadona may require a browser login first", resp.StatusCode)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return err
	}
	if tr.AccessToken == "" {
		return fmt.Errorf("login response had no access_token")
	}
	s := &authSession{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		CustomerID:   customerFromJWT(tr.AccessToken),
		Warehouse:    c.cfg.Warehouse,
	}
	if s.CustomerID == "" {
		return fmt.Errorf("logged in but could not read customer id from token")
	}
	c.saveAuth(s)
	c.log("%s: logged in (customer %s)", c.cfg.Key, s.CustomerID)
	return nil
}

func (c *Client) refresh() error {
	s := c.loadAuth()
	if s == nil || s.RefreshToken == "" {
		return fmt.Errorf("no session; run `grocery --store %s login`", c.cfg.Key)
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": s.RefreshToken})
	req, _ := http.NewRequest(http.MethodPost, c.cfg.BaseURL+"/api/auth/tokens/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("session expired; run `grocery --store %s login` again", c.cfg.Key)
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(raw, &tr) != nil || tr.AccessToken == "" {
		return fmt.Errorf("session expired; run `grocery --store %s login` again", c.cfg.Key)
	}
	s.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		s.RefreshToken = tr.RefreshToken
	}
	c.saveAuth(s)
	return nil
}

// customerFromJWT reads the customer_uuid claim from a SimpleJWT access token
// (no signature check — the server already trusts it).
func customerFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		CustomerUUID string `json:"customer_uuid"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.CustomerUUID
}

// authedJSON performs an authenticated request, refreshing the token once on 401.
func (c *Client) authedJSON(method, url string, body, out any) error {
	if c.loadAuth() == nil {
		return fmt.Errorf("not logged in; run `grocery --store %s login`", c.cfg.Key)
	}
	do := func() (int, []byte, error) {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, url, rdr)
		req.Header.Set("Authorization", "Bearer "+c.auth.AccessToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return resp.StatusCode, raw, nil
	}
	status, raw, err := do()
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		if rerr := c.refresh(); rerr != nil {
			return rerr
		}
		status, raw, err = do()
		if err != nil {
			return err
		}
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("http %d: %s", status, truncate(string(raw), 200))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) cartURL() string {
	wh := c.auth.Warehouse
	if wh == "" {
		wh = c.cfg.Warehouse
	}
	return fmt.Sprintf("%s/api/customers/%s/cart/?lang=%s&wh=%s", c.cfg.BaseURL, c.auth.CustomerID, c.cfg.Lang, wh)
}

// rawCart mirrors the GET cart shape (product nested per line).
type rawCart struct {
	ID    string `json:"id"`
	Lines []struct {
		Quantity  float64 `json:"quantity"`
		ProductID string  `json:"product_id"`
		Product   struct {
			ID    flexStr `json:"id"`
			Name  string  `json:"display_name"`
			Price struct {
				UnitPrice flexFloat `json:"unit_price"`
			} `json:"price_instructions"`
		} `json:"product"`
	} `json:"lines"`
}

func (rc *rawCart) toStore() *store.Cart {
	out := &store.Cart{Currency: "EUR"}
	for _, l := range rc.Lines {
		id := l.ProductID
		if id == "" {
			id = string(l.Product.ID)
		}
		price := float64(l.Product.Price.UnitPrice)
		out.Lines = append(out.Lines, store.CartLine{ID: id, Name: l.Product.Name, Qty: l.Quantity, Price: price})
		out.Total += price * l.Quantity
		out.Count++
	}
	return out
}

func (c *Client) getRawCart() (*rawCart, error) {
	var rc rawCart
	if err := c.authedJSON(http.MethodGet, c.cartURL(), nil, &rc); err != nil {
		return nil, err
	}
	return &rc, nil
}

func (c *Client) CartGet() (*store.Cart, error) {
	rc, err := c.getRawCart()
	if err != nil {
		return nil, err
	}
	return rc.toStore(), nil
}

// putLine is the flat write shape.
type putLine struct {
	Quantity  float64 `json:"quantity"`
	ProductID string  `json:"product_id"`
	Sources   []any   `json:"sources"`
}

// writeCart folds the change into the current lines and PUTs the whole basket.
func (c *Client) writeCart(productID string, qty float64, add bool) (*store.Cart, error) {
	rc, err := c.getRawCart()
	if err != nil {
		return nil, err
	}
	lines := []putLine{}
	found := false
	for _, l := range rc.Lines {
		id := l.ProductID
		if id == "" {
			id = string(l.Product.ID)
		}
		if id == productID {
			found = true
			q := qty
			if add {
				q = l.Quantity + qty
			}
			if q > 0 {
				lines = append(lines, putLine{Quantity: q, ProductID: id, Sources: []any{}})
			}
			continue
		}
		lines = append(lines, putLine{Quantity: l.Quantity, ProductID: id, Sources: []any{}})
	}
	if !found && qty > 0 {
		lines = append(lines, putLine{Quantity: qty, ProductID: productID, Sources: []any{}})
	}
	body := map[string]any{"id": rc.ID, "lines": lines}
	if err := c.authedJSON(http.MethodPut, c.cartURL(), body, nil); err != nil {
		return nil, err
	}
	return c.CartGet()
}

func (c *Client) CartAdd(productID string, qty float64) (*store.Cart, error) {
	return c.writeCart(productID, qty, true)
}

func (c *Client) CartSet(productID string, qty float64) (*store.Cart, error) {
	return c.writeCart(productID, qty, false)
}

func (c *Client) CartClear() (*store.Cart, error) {
	rc, err := c.getRawCart()
	if err != nil {
		return nil, err
	}
	body := map[string]any{"id": rc.ID, "lines": []putLine{}}
	if err := c.authedJSON(http.MethodPut, c.cartURL(), body, nil); err != nil {
		return nil, err
	}
	return c.CartGet()
}
