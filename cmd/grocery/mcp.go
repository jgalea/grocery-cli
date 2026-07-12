package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jgalea/grocery-cli/internal/match"
	"github.com/jgalea/grocery-cli/internal/registry"
	"github.com/jgalea/grocery-cli/internal/store"
)

// cmdMCP runs the stdio MCP server. Protocol traffic goes to stdout; logs to stderr.
func cmdMCP(args []string) error {
	fs, _ := newCommonFlags("mcp")
	parseFlags(fs, args)

	srv := newMCPServer()
	stderrLogf("MCP server starting (stdio)")
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}

func newMCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "grocery", Version: version}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "stores",
		Description: "List supported supermarket stores with country, backend type, languages, and capabilities (search, batch, cart, etc.). Call this first to discover valid store keys.",
	}, toolStores)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search",
		Description: "Full-text product search at one store. Returns product hits with id, name, price, and unit price where available.",
	}, toolSearch)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "batch",
		Description: "Price a shopping list at one store: cheapest search hit per term, plus basket total. Good for generic items (milk, eggs); not exact-SKU matching across chains.",
	}, toolBatch)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "compare",
		Description: "Price the same shopping list across several stores and rank by basket total. Specify stores (list of keys) or country (ES|PT|UK|DE|MT) to compare all search-capable stores in that country.",
	}, toolCompare)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "product",
		Description: "Full product detail by id: price, brand, origin, ingredients, nutrition, URL.",
	}, toolProduct)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "categories",
		Description: "Category tree for a store (default depth 1), or products in one category when category_id is set.",
	}, toolCategories)

	cartDesc := "Only for stores with cart support (see stores). Fills the cart but never places an order — the user reviews and pays in the browser."
	mcp.AddTool(srv, &mcp.Tool{Name: "cart_get", Description: "Show the current cart. " + cartDesc}, toolCartGet)
	mcp.AddTool(srv, &mcp.Tool{Name: "cart_add", Description: "Add a product to the cart (optional qty, default 1; optional max EUR safety cap). " + cartDesc}, toolCartAdd)
	mcp.AddTool(srv, &mcp.Tool{Name: "cart_set", Description: "Set absolute quantity for a cart line (qty 0 removes it). " + cartDesc}, toolCartSet)
	mcp.AddTool(srv, &mcp.Tool{Name: "cart_clear", Description: "Empty the cart. " + cartDesc}, toolCartClear)

	return srv
}

func mcpJSON(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}, nil, nil
}

func mcpFail(err error) (*mcp.CallToolResult, any, error) {
	res := &mcp.CallToolResult{}
	res.SetError(err)
	return res, nil, nil
}

func mcpStore(key, lang string) (store.Store, error) {
	return registry.Get(key, lang, stderrLogf)
}

func toolStores(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	return mcpJSON(registry.List())
}

type mcpSearchArgs struct {
	Store string `json:"store" jsonschema:"store key (see stores)"`
	Term  string `json:"term" jsonschema:"search term"`
	Limit int    `json:"limit,omitempty" jsonschema:"max results (0 = backend default)"`
	Eco   bool   `json:"eco,omitempty" jsonschema:"only ecological products where supported"`
	Lang  string `json:"lang,omitempty" jsonschema:"catalog language (store-specific)"`
}

func toolSearch(_ context.Context, _ *mcp.CallToolRequest, in mcpSearchArgs) (*mcp.CallToolResult, any, error) {
	if in.Store == "" || in.Term == "" {
		return mcpFail(fmt.Errorf("store and term are required"))
	}
	st, err := mcpStore(in.Store, in.Lang)
	if err != nil {
		return mcpFail(err)
	}
	hits, err := st.Search(in.Term, in.Limit, in.Eco)
	if err != nil {
		return mcpFail(storeErr(st, err))
	}
	if in.Limit > 0 && len(hits) > in.Limit {
		hits = hits[:in.Limit]
	}
	return mcpJSON(hits)
}

type mcpBatchArgs struct {
	Store string   `json:"store" jsonschema:"store key"`
	Terms []string `json:"terms" jsonschema:"shopping list terms, one per item"`
	Eco   bool     `json:"eco,omitempty" jsonschema:"prefer ecological hits where supported"`
	Lang  string   `json:"lang,omitempty" jsonschema:"catalog language"`
}

func toolBatch(_ context.Context, _ *mcp.CallToolRequest, in mcpBatchArgs) (*mcp.CallToolResult, any, error) {
	if in.Store == "" {
		return mcpFail(fmt.Errorf("store is required"))
	}
	if len(in.Terms) == 0 {
		return mcpFail(fmt.Errorf("terms are required"))
	}
	st, err := mcpStore(in.Store, in.Lang)
	if err != nil {
		return mcpFail(err)
	}

	type row struct {
		Term    string     `json:"term"`
		Product *store.Hit `json:"product"`
	}
	out := make([]row, 0, len(in.Terms))
	var totalCents int64
	cur := "EUR"
	for _, t := range in.Terms {
		hits, serr := st.Search(t, 10, in.Eco)
		if serr != nil {
			return mcpFail(storeErr(st, serr))
		}
		if len(hits) == 0 {
			out = append(out, row{Term: t})
			continue
		}
		best, ok := match.Select(t, hits)
		if !ok {
			out = append(out, row{Term: t})
			continue
		}
		out = append(out, row{Term: t, Product: &best})
		totalCents += cents(best.Price)
		if best.Currency != "" {
			cur = best.Currency
		}
	}
	return mcpJSON(map[string]any{
		"items": out, "total": centsStr(totalCents), "currency": cur,
	})
}

type mcpCompareArgs struct {
	Terms   []string `json:"terms" jsonschema:"shopping list terms"`
	Stores  []string `json:"stores,omitempty" jsonschema:"store keys to compare"`
	Country string   `json:"country,omitempty" jsonschema:"compare all search-capable stores in a country (ES|PT|UK|DE|MT)"`
	Eco     bool     `json:"eco,omitempty"`
	Limit   int      `json:"limit,omitempty" jsonschema:"search depth per item (default 10)"`
	Lang    string   `json:"lang,omitempty" jsonschema:"catalog language"`
}

func toolCompare(_ context.Context, _ *mcp.CallToolRequest, in mcpCompareArgs) (*mcp.CallToolResult, any, error) {
	if len(in.Terms) == 0 {
		return mcpFail(fmt.Errorf("terms are required"))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	storesCSV := ""
	if len(in.Stores) > 0 {
		storesCSV = strings.Join(in.Stores, ",")
	}
	keys, err := resolveStores(storesCSV, in.Country)
	if err != nil {
		return mcpFail(err)
	}

	type itemResult struct {
		Term  string  `json:"term"`
		ID    string  `json:"id,omitempty"`
		Name  string  `json:"name,omitempty"`
		Price float64 `json:"price,omitempty"`
		Found bool    `json:"found"`
	}
	type storeResult struct {
		Store      string       `json:"store"`
		Label      string       `json:"label"`
		Currency   string       `json:"currency"`
		Total      string       `json:"total"`
		Found      int          `json:"found"`
		Items      int          `json:"items"`
		Detail     []itemResult `json:"detail"`
		Err        string       `json:"error,omitempty"`
		totalCents int64
	}

	results := make([]storeResult, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			r := storeResult{Store: key, Label: labelFor(key), Items: len(in.Terms), Currency: "EUR"}
			st, err := registry.Get(key, in.Lang, func(string, ...any) {})
			if err != nil {
				r.Err = err.Error()
				results[i] = r
				return
			}
			for _, t := range in.Terms {
				hits, e := st.Search(t, limit, in.Eco)
				if e != nil || len(hits) == 0 {
					r.Detail = append(r.Detail, itemResult{Term: t, Found: false})
					continue
				}
				h, ok := match.Select(t, hits)
				if !ok {
					r.Detail = append(r.Detail, itemResult{Term: t, Found: false})
					continue
				}
				if h.Currency != "" {
					r.Currency = h.Currency
				}
				r.totalCents += cents(h.Price)
				r.Found++
				r.Detail = append(r.Detail, itemResult{Term: t, ID: h.ID, Name: h.Name, Price: h.Price, Found: true})
			}
			r.Total = centsStr(r.totalCents)
			results[i] = r
		}(i, key)
	}
	wg.Wait()

	sort.SliceStable(results, func(a, b int) bool {
		ra, rb := results[a], results[b]
		if (ra.Found > 0) != (rb.Found > 0) {
			return ra.Found > 0
		}
		return ra.totalCents < rb.totalCents
	})
	return mcpJSON(results)
}

type mcpProductArgs struct {
	Store string `json:"store" jsonschema:"store key"`
	ID    string `json:"id" jsonschema:"product id"`
	Lang  string `json:"lang,omitempty" jsonschema:"catalog language"`
}

func toolProduct(_ context.Context, _ *mcp.CallToolRequest, in mcpProductArgs) (*mcp.CallToolResult, any, error) {
	if in.Store == "" || in.ID == "" {
		return mcpFail(fmt.Errorf("store and id are required"))
	}
	st, err := mcpStore(in.Store, in.Lang)
	if err != nil {
		return mcpFail(err)
	}
	pd, err := st.Product(in.ID)
	if err != nil {
		return mcpFail(storeErr(st, err))
	}
	return mcpJSON(pd)
}

type mcpCategoriesArgs struct {
	Store      string `json:"store" jsonschema:"store key"`
	CategoryID string `json:"category_id,omitempty" jsonschema:"if set, list products in this category instead of the tree"`
	Depth      int    `json:"depth,omitempty" jsonschema:"tree depth when listing categories (default 1)"`
	Limit      int    `json:"limit,omitempty" jsonschema:"max products with category_id"`
	Eco        bool   `json:"eco,omitempty" jsonschema:"with category_id: only ecological products"`
	Lang       string `json:"lang,omitempty" jsonschema:"catalog language"`
}

func toolCategories(_ context.Context, _ *mcp.CallToolRequest, in mcpCategoriesArgs) (*mcp.CallToolResult, any, error) {
	if in.Store == "" {
		return mcpFail(fmt.Errorf("store is required"))
	}
	st, err := mcpStore(in.Store, in.Lang)
	if err != nil {
		return mcpFail(err)
	}
	if in.CategoryID != "" {
		hits, err := st.CategoryProducts(in.CategoryID, in.Limit, in.Eco)
		if err != nil {
			return mcpFail(storeErr(st, err))
		}
		return mcpJSON(hits)
	}
	depth := in.Depth
	if depth <= 0 {
		depth = 1
	}
	cats, err := st.Categories(depth)
	if err != nil {
		return mcpFail(storeErr(st, err))
	}
	return mcpJSON(cats)
}

type mcpCartStoreArgs struct {
	Store string `json:"store" jsonschema:"store key (must support cart)"`
	Lang  string `json:"lang,omitempty" jsonschema:"catalog language"`
}

func mcpCarter(storeKey, lang string) (store.Carter, store.Store, error) {
	st, err := mcpStore(storeKey, lang)
	if err != nil {
		return nil, nil, err
	}
	ca, ok := st.(store.Carter)
	if !ok {
		return nil, st, fmt.Errorf("store %q has no cart — it's read-only (search/compare only)", st.Key())
	}
	if !ca.LoggedIn() {
		return nil, st, fmt.Errorf("not logged in; run `grocery --store %s login` first", st.Key())
	}
	return ca, st, nil
}

func toolCartGet(_ context.Context, _ *mcp.CallToolRequest, in mcpCartStoreArgs) (*mcp.CallToolResult, any, error) {
	if in.Store == "" {
		return mcpFail(fmt.Errorf("store is required"))
	}
	ca, _, err := mcpCarter(in.Store, in.Lang)
	if err != nil {
		return mcpFail(err)
	}
	cart, err := ca.CartGet()
	if err != nil {
		return mcpFail(err)
	}
	return mcpJSON(cart)
}

type mcpCartMutateArgs struct {
	Store string  `json:"store" jsonschema:"store key"`
	ID    string  `json:"id" jsonschema:"product id"`
	Qty   float64 `json:"qty,omitempty" jsonschema:"quantity (default 1 for add; 0 removes for set)"`
	Max   float64 `json:"max,omitempty" jsonschema:"refuse if line cost exceeds this EUR amount"`
	Lang  string  `json:"lang,omitempty" jsonschema:"catalog language"`
}

func toolCartAdd(_ context.Context, _ *mcp.CallToolRequest, in mcpCartMutateArgs) (*mcp.CallToolResult, any, error) {
	return mcpCartMutate(in, "add")
}

func toolCartSet(_ context.Context, _ *mcp.CallToolRequest, in mcpCartMutateArgs) (*mcp.CallToolResult, any, error) {
	return mcpCartMutate(in, "set")
}

func mcpCartMutate(in mcpCartMutateArgs, op string) (*mcp.CallToolResult, any, error) {
	if in.Store == "" || in.ID == "" {
		return mcpFail(fmt.Errorf("store and id are required"))
	}
	ca, st, err := mcpCarter(in.Store, in.Lang)
	if err != nil {
		return mcpFail(err)
	}
	qty := in.Qty
	if qty == 0 && op == "add" {
		qty = 1
	}
	if in.Max > 0 && qty > 0 {
		if pd, e := st.Product(in.ID); e == nil {
			if cost := pd.Price * qty; cost > in.Max {
				return mcpFail(fmt.Errorf("refused: %s × %s = %.2f exceeds max %.2f", pd.Name, fmtQty(qty), cost, in.Max))
			}
		}
	}
	var cart *store.Cart
	if op == "add" {
		cart, err = ca.CartAdd(in.ID, qty)
		if err == nil {
			recordCartAdd(st, in.ID, qty, cart)
		}
	} else {
		cart, err = ca.CartSet(in.ID, qty)
	}
	if err != nil {
		return mcpFail(err)
	}
	return mcpJSON(cart)
}

func toolCartClear(_ context.Context, _ *mcp.CallToolRequest, in mcpCartStoreArgs) (*mcp.CallToolResult, any, error) {
	if in.Store == "" {
		return mcpFail(fmt.Errorf("store is required"))
	}
	ca, _, err := mcpCarter(in.Store, in.Lang)
	if err != nil {
		return mcpFail(err)
	}
	cart, err := ca.CartClear()
	if err != nil {
		return mcpFail(err)
	}
	return mcpJSON(cart)
}
