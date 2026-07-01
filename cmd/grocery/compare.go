package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/jgalea/grocery-cli/internal/registry"
)

// cmdCompare prices one shopping list across several stores and ranks them by
// basket total. Stores are queried concurrently.
func cmdCompare(args []string) error {
	fs, cf := newCommonFlags("compare")
	file := fs.String("f", "", "file with one item per line ('-' for stdin); else items are positional")
	storesCSV := fs.String("stores", "", "comma-separated store keys to compare")
	country := fs.String("country", "", "compare all search-capable stores in a country (ES|PT|UK|DE|MT)")
	eco := fs.Bool("eco", false, "prefer ecological hits where supported")
	limit := fs.Int("limit", 10, "search depth per item")
	detail := fs.Bool("detail", false, "show the matched product per item for each store")
	parseFlags(fs, args)

	terms, err := collectLines(*file, fs.Args())
	if err != nil {
		return err
	}
	if len(terms) == 0 {
		return fmt.Errorf("no items (use -f file, stdin, or '<item>...' args)")
	}
	keys, err := resolveStores(*storesCSV, *country)
	if err != nil {
		return err
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
		Detail     []itemResult `json:"detail,omitempty"`
		Err        string       `json:"error,omitempty"`
		totalCents int64
	}

	results := make([]storeResult, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Add(1)
		go func(i int, key string) {
			defer wg.Done()
			r := storeResult{Store: key, Label: labelFor(key), Items: len(terms), Currency: "EUR"}
			st, err := registry.Get(key, cf.lang, func(string, ...any) {}) // silence per-store logs
			if err != nil {
				r.Err = err.Error()
				results[i] = r
				return
			}
			for _, t := range terms {
				hits, e := st.Search(t, *limit, *eco)
				if e != nil || len(hits) == 0 {
					r.Detail = append(r.Detail, itemResult{Term: t, Found: false})
					continue
				}
				h := pickCheapest(hits)
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

	// Rank: stores that priced at least one item first, cheapest total first.
	sort.SliceStable(results, func(a, b int) bool {
		ra, rb := results[a], results[b]
		if (ra.Found > 0) != (rb.Found > 0) {
			return ra.Found > 0
		}
		return ra.totalCents < rb.totalCents
	})

	if done, err := emitStructured(cf, results); done {
		return err
	}

	fmt.Printf("Basket of %s across %d stores:\n\n", plural(len(terms), "item", "items"), len(keys))
	for _, r := range results {
		if r.Err != "" {
			fmt.Printf("  %-16s error: %s\n", r.Store, r.Err)
			continue
		}
		flag := ""
		if r.Found < r.Items {
			flag = fmt.Sprintf("  (missing %d)", r.Items-r.Found)
		}
		fmt.Printf("  %-16s %8s%s   %d/%d items%s\n", r.Store, r.Total, currencySymbol(r.Currency), r.Found, r.Items, flag)
		if *detail {
			for _, it := range r.Detail {
				if it.Found {
					fmt.Printf("      %-14s → %s (%s%s)\n", it.Term, it.Name, centsStr(cents(it.Price)), currencySymbol(r.Currency))
				} else {
					fmt.Printf("      %-14s → (not found)\n", it.Term)
				}
			}
		}
	}
	curset := map[string]bool{}
	for _, r := range results {
		if r.Found > 0 {
			curset[r.Currency] = true
		}
	}
	if len(curset) > 1 {
		fmt.Println("\nnote: stores use different currencies, so totals aren't directly comparable.")
	}
	return nil
}

func resolveStores(storesCSV, country string) ([]string, error) {
	metas := registry.List()
	if storesCSV != "" {
		valid := map[string]bool{}
		for _, m := range metas {
			valid[m.Key] = true
		}
		var keys []string
		for _, k := range strings.Split(storesCSV, ",") {
			if k = strings.TrimSpace(k); k == "" {
				continue
			}
			if !valid[k] {
				return nil, fmt.Errorf("unknown store %q (see `grocery stores`)", k)
			}
			keys = append(keys, k)
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("no valid stores in --stores")
		}
		return keys, nil
	}
	if country != "" {
		cc := strings.ToUpper(country)
		var keys []string
		for _, m := range metas {
			if strings.EqualFold(m.Country, cc) && capHas(m.Caps, "search") {
				keys = append(keys, m.Key)
			}
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("no search-capable stores for country %q", country)
		}
		return keys, nil
	}
	return nil, fmt.Errorf("specify --stores a,b,c or --country ES")
}

func capHas(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

func labelFor(key string) string {
	for _, m := range registry.List() {
		if m.Key == key {
			return m.Label
		}
	}
	return key
}
