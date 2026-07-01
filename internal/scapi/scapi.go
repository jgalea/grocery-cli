// Package scapi is a Salesforce Commerce Cloud (SCAPI) Shopper adapter for
// headless-PWA stores that expose a guest client id. It authenticates as an
// anonymous guest (SLAS PKCE) and reads the Shopper Search / Products APIs.
// One Config per store; the same code drives any SCAPI headless retailer.
package scapi

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

// Config is the per-store SCAPI wiring, lifted from the store's own PWA.
type Config struct {
	Key         string            // store key, e.g. "ametller"
	ShortCode   string            // SCAPI short code
	Org         string            // organizationId
	ClientID    string            // public guest client id
	SiteID      string            // siteId / channel_id
	RedirectURI string            // any URI the guest flow accepts
	EcoRefine   string            // e.g. "c_ao_preferencias=28" ("" = no eco facet)
	Locales     map[string]string // lang short -> SCAPI locale (e.g. "ca":"ca")
	DefaultLang string
}

const userAgent = "grocery-cli (+https://github.com/jgalea/grocery-cli)"

// Client is a live SCAPI adapter for one store.
type Client struct {
	cfg  Config
	lang string
	http *http.Client
	logf func(string, ...any)
	sess *session
}

// New builds an adapter for cfg in the given lang (falling back to the config default).
func New(cfg Config, lang string, logf func(string, ...any)) *Client {
	if lang == "" {
		lang = cfg.DefaultLang
	}
	return &Client{cfg: cfg, lang: lang, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.cfg.Key }

func (c *Client) log(f string, a ...any) {
	if c.logf != nil {
		c.logf(f, a...)
	}
}

func (c *Client) locale() string {
	if l, ok := c.cfg.Locales[c.lang]; ok {
		return l
	}
	if l, ok := c.cfg.Locales[c.cfg.DefaultLang]; ok {
		return l
	}
	return c.lang
}

func (c *Client) base() string {
	if u := os.Getenv("GROCERY_SCAPI_BASE"); u != "" {
		return u
	}
	return "https://" + c.cfg.ShortCode + ".api.commercecloud.salesforce.com"
}

// ---- auth (SLAS guest PKCE) ----

type session struct {
	AccessToken string    `json:"access_token"`
	Expiry      time.Time `json:"expiry"`
}

func (c *Client) sessionPath() string {
	dir := os.Getenv("GROCERY_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".grocery")
	}
	return filepath.Join(dir, "session-"+c.cfg.Key+".json")
}

func (c *Client) token() (string, error) {
	if c.sess == nil {
		if b, err := os.ReadFile(c.sessionPath()); err == nil {
			var s session
			if json.Unmarshal(b, &s) == nil {
				c.sess = &s
			}
		}
	}
	if c.sess != nil && c.sess.AccessToken != "" && time.Now().Before(c.sess.Expiry) {
		return c.sess.AccessToken, nil
	}
	s, err := c.guestLogin()
	if err != nil {
		return "", fmt.Errorf("guest login: %w", err)
	}
	c.sess = s
	c.saveSession(s)
	return s.AccessToken, nil
}

func (c *Client) saveSession(s *session) {
	_ = os.MkdirAll(filepath.Dir(c.sessionPath()), 0o700)
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		_ = os.WriteFile(c.sessionPath(), b, 0o600)
	}
}

func pkce() (verifier, challenge string) {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func (c *Client) guestLogin() (*session, error) {
	verifier, challenge := pkce()
	authClient := &http.Client{
		Timeout:       c.http.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	aq := url.Values{}
	aq.Set("client_id", c.cfg.ClientID)
	aq.Set("response_type", "code")
	aq.Set("redirect_uri", c.cfg.RedirectURI)
	aq.Set("code_challenge", challenge)
	aq.Set("hint", "guest")
	authURL := c.base() + "/shopper/auth/v1/organizations/" + c.cfg.Org + "/oauth2/authorize?" + aq.Encode()

	req, _ := http.NewRequest(http.MethodGet, authURL, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := authClient.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil, fmt.Errorf("authorize returned no redirect (http %d)", resp.StatusCode)
	}
	lu, err := url.Parse(loc)
	if err != nil {
		return nil, err
	}
	if e := lu.Query().Get("error"); e != "" {
		return nil, fmt.Errorf("authorize: %s (%s)", e, lu.Query().Get("error_description"))
	}
	code := lu.Query().Get("code")
	usid := lu.Query().Get("usid")
	if code == "" {
		return nil, errors.New("authorize: no code in redirect")
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code_pkce")
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("client_id", c.cfg.ClientID)
	form.Set("channel_id", c.cfg.SiteID)
	form.Set("redirect_uri", c.cfg.RedirectURI)
	form.Set("usid", usid)
	tokenURL := c.base() + "/shopper/auth/v1/organizations/" + c.cfg.Org + "/oauth2/token"

	treq, _ := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	treq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	treq.Header.Set("User-Agent", userAgent)
	tresp, err := c.http.Do(treq)
	if err != nil {
		return nil, err
	}
	defer tresp.Body.Close()
	body, _ := io.ReadAll(tresp.Body)
	if tresp.StatusCode < 200 || tresp.StatusCode >= 300 {
		return nil, fmt.Errorf("token http %d: %s", tresp.StatusCode, truncate(string(body), 200))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, err
	}
	if tr.AccessToken == "" {
		return nil, errors.New("token response had no access_token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl == 0 {
		ttl = 25 * time.Minute
	}
	exp := time.Now().Add(ttl - 60*time.Second)
	c.log("%s: acquired guest session (expires ~%s)", c.cfg.Key, exp.Format(time.Kitchen))
	return &session{AccessToken: tr.AccessToken, Expiry: exp}, nil
}

// ---- reads ----

func (c *Client) doJSON(path string, q url.Values, out any) error {
	tok, err := c.token()
	if err != nil {
		return err
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("siteId", c.cfg.SiteID)
	q.Set("locale", c.locale())
	req, _ := http.NewRequest(http.MethodGet, c.base()+path+"?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return json.Unmarshal(body, out)
}

// raw SCAPI shapes (only the fields we use)
type scHit struct {
	ProductID    string  `json:"productId"`
	ProductName  string  `json:"productName"`
	Price        float64 `json:"price"`
	PricePerUnit float64 `json:"pricePerUnit"`
	Currency     string  `json:"currency"`
	Orderable    bool    `json:"orderable"`
	Instaleap    struct {
		Unit         string   `json:"unit"`
		Preferencias []string `json:"ao_preferencias"`
	} `json:"c_instaleapHit"`
}

type scSearch struct {
	Total       int     `json:"total"`
	Hits        []scHit `json:"hits"`
	Suggestions struct {
		SuggestedPhrases []struct {
			Phrase     string `json:"phrase"`
			ExactMatch bool   `json:"exactMatch"`
		} `json:"suggestedPhrases"`
	} `json:"searchPhraseSuggestions"`
}

func (c *Client) rawSearch(term string, limit int, eco bool) (*scSearch, error) {
	q := url.Values{}
	if term != "" {
		q.Set("q", term)
	}
	if eco && c.cfg.EcoRefine != "" {
		q.Add("refine", c.cfg.EcoRefine)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var sr scSearch
	err := c.doJSON("/search/shopper-search/v1/organizations/"+c.cfg.Org+"/product-search", q, &sr)
	return &sr, err
}

func (c *Client) toHit(h scHit) store.Hit {
	eco := false
	for _, p := range h.Instaleap.Preferencias {
		if strings.Contains(strings.ToLower(p), "ecol") {
			eco = true
		}
	}
	return store.Hit{
		ID: h.ProductID, Name: strings.TrimSpace(h.ProductName), Price: h.Price,
		PricePerUnit: h.PricePerUnit, Unit: normUnit(h.Instaleap.Unit), Currency: h.Currency,
		Eco: eco, Available: h.Orderable,
	}
}

// Search implements store.Store, retrying once with the server's spelling
// suggestion when a query returns nothing.
func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	sr, err := c.rawSearch(term, limit, eco)
	if err != nil {
		return nil, err
	}
	if len(sr.Hits) == 0 {
		for _, p := range sr.Suggestions.SuggestedPhrases {
			if !p.ExactMatch && strings.TrimSpace(p.Phrase) != "" && !strings.EqualFold(p.Phrase, term) {
				if sr2, e := c.rawSearch(p.Phrase, limit, eco); e == nil && len(sr2.Hits) > 0 {
					c.log("%s: cap resultat per «%s» — mostrant «%s»", c.cfg.Key, term, p.Phrase)
					sr = sr2
				}
				break
			}
		}
	}
	out := make([]store.Hit, 0, len(sr.Hits))
	for _, h := range sr.Hits {
		out = append(out, c.toHit(h))
	}
	return out, nil
}

// CategoryProducts lists a category's products via a cgid refine.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	q := url.Values{}
	q.Add("refine", "cgid="+categoryID)
	if eco && c.cfg.EcoRefine != "" {
		q.Add("refine", c.cfg.EcoRefine)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var sr scSearch
	if err := c.doJSON("/search/shopper-search/v1/organizations/"+c.cfg.Org+"/product-search", q, &sr); err != nil {
		return nil, err
	}
	out := make([]store.Hit, 0, len(sr.Hits))
	for _, h := range sr.Hits {
		out = append(out, c.toHit(h))
	}
	return out, nil
}

type scProduct struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Brand        string  `json:"brand"`
	Price        float64 `json:"price"`
	PricePerUnit float64 `json:"pricePerUnit"`
	Currency     string  `json:"currency"`
	EAN          string  `json:"ean"`
	UnitMeasure  string  `json:"unitMeasure"`
	SlugURL      string  `json:"slugUrl"`
	Ingredients  string  `json:"c_ao_ingredientes"`
	Nutrients    string  `json:"c_ao_nutrients"`
	Conservation string  `json:"c_ao_conservation"`
	Origen       string  `json:"c_ao_origen"`
	IsSale       bool    `json:"c_isSale"`
}

func (c *Client) Product(id string) (*store.Product, error) {
	var p scProduct
	err := c.doJSON("/product/shopper-products/v1/organizations/"+c.cfg.Org+"/products/"+url.PathEscape(id), nil, &p)
	if err != nil {
		return nil, err
	}
	return &store.Product{
		Hit: store.Hit{
			ID: p.ID, Name: p.Name, Price: p.Price, PricePerUnit: p.PricePerUnit,
			Unit: normUnit(p.UnitMeasure), Currency: p.Currency, Brand: p.Brand,
			Available: true, URL: p.SlugURL,
		},
		EAN: p.EAN, Origin: p.Origen, Ingredients: p.Ingredients, Nutrients: p.Nutrients,
		Conservation: strings.TrimSpace(strings.TrimPrefix(p.Conservation, ".")), OnSale: p.IsSale,
	}, nil
}

type scCategory struct {
	ID         string       `json:"id"`
	Name       string       `json:"name"`
	Categories []scCategory `json:"categories"`
}

func (c *Client) Categories(depth int) ([]store.Category, error) {
	if depth < 1 {
		depth = 1
	}
	q := url.Values{}
	q.Set("levels", strconv.Itoa(depth))
	var root scCategory
	if err := c.doJSON("/product/shopper-products/v1/organizations/"+c.cfg.Org+"/categories/root", q, &root); err != nil {
		return nil, err
	}
	return mapCats(root.Categories), nil
}

func mapCats(in []scCategory) []store.Category {
	out := make([]store.Category, 0, len(in))
	for _, c := range in {
		out = append(out, store.Category{ID: c.ID, Name: c.Name, Children: mapCats(c.Categories)})
	}
	return out
}

func normUnit(u string) string {
	switch strings.ToLower(strings.TrimSpace(u)) {
	case "kg", "g":
		return "kg"
	case "l", "ml", "cl":
		return "L"
	case "u", "un", "ud", "uds", "unidad", "unitat", "pack":
		return "u"
	default:
		return ""
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
