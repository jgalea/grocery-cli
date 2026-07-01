package bonpreu

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/jgalea/grocery-cli/internal/store"
)

// Product fetches one product by its short retailer id. The detail API is
// GraphQL behind the WAF JS-challenge, so this scrapes the server-rendered
// product page and pulls the embedded product node (and any info blocks) out of
// it — which works anonymously over the same uTLS read path.
func (c *Client) Product(id string) (*store.Product, error) {
	page, status, err := c.getText(c.baseURL + "/products/" + url.PathEscape(id))
	if err != nil {
		if status == 404 {
			return nil, fmt.Errorf("product %s: not found", id)
		}
		return nil, err
	}
	node := findProductNode(page, id)
	if node == nil {
		return nil, fmt.Errorf("product %s: not found in page (id may be invalid or out of catalog)", id)
	}
	b, _ := json.Marshal(node)
	var p bpProduct
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("decode product %s: %w", id, err)
	}
	prod := &store.Product{Hit: c.toHit(p)}
	prod.OnSale = len(p.Promotions) > 0
	applyDetails(prod, collectDetails(page))
	return prod, nil
}

type detail struct {
	title   string
	content string
}

// applyDetails routes the scraped info blocks onto the typed Product fields,
// keeping anything unrecognised out (the fields an adapter can't supply stay
// empty).
func applyDetails(p *store.Product, ds []detail) {
	for _, d := range ds {
		t := strings.ToLower(d.title)
		switch {
		case strings.Contains(t, "ingredient"):
			p.Ingredients = d.content
		case strings.Contains(t, "nutri") || strings.Contains(t, "informació nutricional") || strings.Contains(t, "información nutricional"):
			p.Nutrients = d.content
		case strings.Contains(t, "conserv"):
			p.Conservation = d.content
		case strings.Contains(t, "origen") || strings.Contains(t, "origin"):
			if p.Origin == "" {
				p.Origin = d.content
			}
		}
	}
}

var stateVarRE = regexp.MustCompile(`window\.(__URQL_DATA__|__INITIAL_STATE__|__QUERY_INITIAL_STATE__)\s*=\s*`)

// findProductNode walks every embedded state blob (and the double-encoded `data`
// strings inside URQL) for the richest object whose retailerProductId matches.
func findProductNode(page, id string) map[string]any {
	var best map[string]any
	bestLen := -1
	consider := func(m map[string]any) {
		if v, _ := m["retailerProductId"].(string); v == id {
			if _, ok := m["name"]; ok {
				if n := len(fmt.Sprint(m)); n > bestLen {
					best, bestLen = m, n
				}
			}
		}
	}
	for _, blob := range embeddedStates(page) {
		walkMaps(blob, consider)
	}
	return best
}

// collectDetails gathers every {title, content} info block on the page, in
// order, de-duplicated by title.
func collectDetails(page string) []detail {
	var out []detail
	seen := map[string]bool{}
	for _, blob := range embeddedStates(page) {
		walkMaps(blob, func(m map[string]any) {
			t, okT := m["title"].(string)
			ct, okC := m["content"].(string)
			if t == "brand" {
				return
			}
			if okT && okC && t != "" && !seen[t] {
				if content := stripHTML(ct); content != "" {
					seen[t] = true
					out = append(out, detail{title: t, content: content})
				}
			}
		})
	}
	return out
}

func appendJSON(dst []any, s string) []any {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return append(dst, v)
	}
	return dst
}

// embeddedStates returns each window.__…__ JSON blob, plus the JSON decoded from
// any nested double-encoded `data` strings (URQL stores query results that way).
func embeddedStates(page string) []any {
	var blobs []any
	for _, loc := range stateVarRE.FindAllStringIndex(page, -1) {
		if obj, ok := braceObject(page, loc[1]); ok {
			blobs = appendJSON(blobs, obj)
		}
	}
	var extra []any
	for _, b := range blobs {
		walkMaps(b, func(m map[string]any) {
			if s, ok := m["data"].(string); ok && strings.HasPrefix(s, "{") {
				extra = appendJSON(extra, s)
			}
		})
	}
	return append(blobs, extra...)
}

// braceObject extracts a balanced {...} starting at i, respecting strings/escapes.
func braceObject(s string, i int) (string, bool) {
	if i >= len(s) || s[i] != '{' {
		return "", false
	}
	depth, inStr, esc := 0, false, false
	for j := i; j < len(s); j++ {
		ch := s[j]
		switch {
		case esc:
			esc = false
		case ch == '\\':
			esc = true
		case ch == '"':
			inStr = !inStr
		case inStr:
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return s[i : j+1], true
			}
		}
	}
	return "", false
}

// walkMaps invokes fn on every map encountered in an arbitrary JSON value tree.
func walkMaps(v any, fn func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		fn(x)
		for _, vv := range x {
			walkMaps(vv, fn)
		}
	case []any:
		for _, vv := range x {
			walkMaps(vv, fn)
		}
	}
}

var (
	tagRE       = regexp.MustCompile(`<[^>]+>`)
	rowBreakRE  = regexp.MustCompile(`(?i)<br\s*/?>|</(tr|p|li|div|h[1-6])>`)
	cellBreakRE = regexp.MustCompile(`(?i)</(td|th)>`)
	wsRunRE     = regexp.MustCompile(`[ \t]{2,}`)
)

// stripHTML turns the light HTML in detail content into readable plain text.
func stripHTML(s string) string {
	s = rowBreakRE.ReplaceAllString(s, "\n")
	s = cellBreakRE.ReplaceAllString(s, "\t")
	s = tagRE.ReplaceAllString(s, "")
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		cols := make([]string, 0, 4)
		for _, cell := range strings.Split(line, "\t") {
			cell = html.UnescapeString(cell)
			if cell = strings.TrimSpace(cell); cell != "" {
				cols = append(cols, cell)
			}
		}
		if len(cols) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(wsRunRE.ReplaceAllString(strings.Join(cols, "  "), " "))
	}
	return b.String()
}
