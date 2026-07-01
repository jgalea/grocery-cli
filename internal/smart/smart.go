// Package smart is an adapter for Smart Supermarket (smart.com.mt), a Maltese
// grocer running a server-rendered ASP.NET / DevExpress storefront. There is no
// JSON API, so this adapter scrapes the rendered HTML with the standard library.
// Prices are in EUR. The host's HTTPS is broken, so all requests go over plain
// http:// (the Go client hits http fine).
package smart

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jgalea/grocery-cli/internal/store"
)

const (
	baseURL   = "http://smart.com.mt"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36"
)

type Client struct {
	key  string
	http *http.Client
	logf func(string, ...any)
}

func New(key string, logf func(string, ...any)) *Client {
	return &Client{key: key, http: &http.Client{Timeout: 30 * time.Second}, logf: logf}
}

func (c *Client) Key() string { return c.key }

func (c *Client) log(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

func (c *Client) getHTML(u string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	return string(raw), nil
}

// parsePrice accepts "3.37" or "3,37" and returns a float. Empty/garbage -> 0.
func parsePrice(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", ".")
	// keep only the last decimal point if several separators slipped in
	if i := strings.LastIndex(s, "."); i >= 0 {
		intPart := strings.ReplaceAll(s[:i], ".", "")
		s = intPart + s[i:]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

var (
	// Homepage category links: href to forms/Products.aspx?=<code>, link text = name.
	reCatLink = regexp.MustCompile(`(?is)<a[^>]*href="[^"]*forms/Products\.aspx\?=([0-9-]+)"[^>]*>(.*?)</a>`)
	// DevExpress grid data rows; each product lives in its own DXDataRow block.
	reRowSplit = regexp.MustCompile(`id="[^"]*gvProducts_DXDataRow\d+"`)
	reName     = regexp.MustCompile(`(?is)lblProperDescription"[^>]*>(.*?)</span>`)
	rePID      = regexp.MustCompile(`ProductDetails\.aspx\?pid=(\d+)`)
	rePrice    = regexp.MustCompile(`€\s*([\d.,]+)`)
	reTags     = regexp.MustCompile(`(?is)<[^>]+>`)
	reWS       = regexp.MustCompile(`\s+`)
)

func cleanText(s string) string {
	s = reTags.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&amp;", "&", "&nbsp;", " ", "&#39;", "'", "&quot;", `"`, "&lt;", "<", "&gt;", ">").Replace(s)
	return strings.TrimSpace(reWS.ReplaceAllString(s, " "))
}

// Categories parses the homepage category-code links into a tree. Codes are
// hierarchical dash-joined segments (e.g. 10-10 > 10-10-1005 > 10-10-1005-100501),
// so the tree is nested by segment count and depth caps the returned levels.
func (c *Client) Categories(depth int) ([]store.Category, error) {
	html, err := c.getHTML(baseURL + "/")
	if err != nil {
		return nil, err
	}
	type node struct {
		cat  store.Category
		segs int
	}
	byID := map[string]*node{}
	var order []string
	for _, m := range reCatLink.FindAllStringSubmatch(html, -1) {
		code := m[1]
		name := cleanText(m[2])
		if code == "" || name == "" {
			continue
		}
		if _, ok := byID[code]; ok {
			continue
		}
		byID[code] = &node{cat: store.Category{ID: code, Name: name}, segs: strings.Count(code, "-") + 1}
		order = append(order, code)
	}
	if len(byID) == 0 {
		c.log("smart: no category links found on homepage")
		return []store.Category{}, nil
	}
	// parent of a code is the code with its last segment removed.
	parentOf := func(code string) string {
		i := strings.LastIndex(code, "-")
		if i < 0 {
			return ""
		}
		return code[:i]
	}
	// build children under a synthetic root, then flatten roots.
	children := map[string][]string{}
	var roots []string
	for _, code := range order {
		p := parentOf(code)
		if _, ok := byID[p]; ok && p != "" {
			children[p] = append(children[p], code)
		} else {
			roots = append(roots, code)
		}
	}
	var build func(code string, level int) store.Category
	build = func(code string, level int) store.Category {
		cat := byID[code].cat
		if depth > 0 && level >= depth {
			return cat
		}
		for _, ch := range children[code] {
			cat.Children = append(cat.Children, build(ch, level+1))
		}
		return cat
	}
	out := make([]store.Category, 0, len(roots))
	for _, r := range roots {
		out = append(out, build(r, 1))
	}
	return out, nil
}

// CategoryProducts fetches a category's Products.aspx grid and parses the
// first-page rows. This is the primary read path. Deeper pages need an ASPX
// postback (viewstate), which is out of scope.
func (c *Client) CategoryProducts(categoryID string, limit int, eco bool) ([]store.Hit, error) {
	categoryID = strings.TrimSpace(categoryID)
	if categoryID == "" {
		return []store.Hit{}, nil
	}
	html, err := c.getHTML(baseURL + "/forms/Products.aspx?=" + categoryID)
	if err != nil {
		return nil, err
	}
	blocks := reRowSplit.Split(html, -1)
	if len(blocks) > 0 {
		blocks = blocks[1:] // drop the pre-first-row preamble
	}
	out := make([]store.Hit, 0, len(blocks))
	seen := map[string]bool{}
	for _, b := range blocks {
		pm := rePID.FindStringSubmatch(b)
		nm := reName.FindStringSubmatch(b)
		if pm == nil || nm == nil {
			continue
		}
		pid := pm[1]
		name := cleanText(nm[1])
		if pid == "" || name == "" || seen[pid] {
			continue
		}
		seen[pid] = true
		var price float64
		if prm := rePrice.FindStringSubmatch(b); prm != nil {
			price = parsePrice(prm[1])
		}
		out = append(out, store.Hit{
			ID:        pid,
			Name:      name,
			Price:     price,
			Currency:  "EUR",
			Available: true,
			URL:       baseURL + "/forms/ProductDetails.aspx?pid=" + pid,
		})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		c.log("smart: no products parsed for category %q", categoryID)
	}
	return out, nil
}

// Product fetches the detail page for a pid and parses name + price. Fields the
// page doesn't cleanly expose stay empty.
func (c *Client) Product(id string) (*store.Product, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, store.ErrUnsupported
	}
	html, err := c.getHTML(baseURL + "/forms/ProductDetails.aspx?pid=" + id)
	if err != nil {
		return nil, err
	}
	nm := reName.FindStringSubmatch(html)
	if nm == nil {
		c.log("smart: product %q detail not parseable", id)
		return nil, store.ErrUnsupported
	}
	var price float64
	if prm := rePrice.FindStringSubmatch(html); prm != nil {
		price = parsePrice(prm[1])
	}
	return &store.Product{Hit: store.Hit{
		ID:        id,
		Name:      cleanText(nm[1]),
		Price:     price,
		Currency:  "EUR",
		Available: true,
		URL:       baseURL + "/forms/ProductDetails.aspx?pid=" + id,
	}}, nil
}

// Search would need an ASPX postback with viewstate to drive the site's search
// form, which this stdlib scraper doesn't do. Use CategoryProducts instead.
func (c *Client) Search(term string, limit int, eco bool) ([]store.Hit, error) {
	return nil, store.ErrUnsupported
}
