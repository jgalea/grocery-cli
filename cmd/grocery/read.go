package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jgalea/grocery-cli/internal/match"
	"github.com/jgalea/grocery-cli/internal/store"
)

func cmdSearch(args []string) error {
	fs, cf := newCommonFlags("search")
	limit := fs.Int("limit", 0, "max products to return")
	eco := fs.Bool("eco", false, "only ecological products (where supported)")
	cheapest := fs.Bool("cheapest", false, "rank by unit price (€/kg·L·u); mark the cheapest per unit")
	parseFlags(fs, args)

	term := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if term == "" {
		return fmt.Errorf("usage: grocery search <term...>")
	}
	st, err := newStore(cf)
	if err != nil {
		return err
	}
	hits, err := st.Search(term, *limit, *eco)
	if err != nil {
		return storeErr(st, err)
	}
	if *limit > 0 && len(hits) > *limit {
		hits = hits[:*limit]
	}
	var winners map[string]bool
	if *cheapest {
		hits, winners = rankByUnit(hits)
	}
	if done, err := emitStructured(cf, hits); done {
		return err
	}
	if len(hits) == 0 {
		fmt.Println("(no results)")
		return nil
	}
	printHits(hits, winners, *cheapest)
	return nil
}

func printHits(hits []store.Hit, winners map[string]bool, cheapest bool) {
	for _, h := range hits {
		if winners[h.ID] {
			fmt.Println("  ★ " + hitLine(h) + "  ← best " + strings.TrimPrefix(unitSuffix(h.Unit), "/"))
			continue
		}
		indent := "  "
		if cheapest {
			indent = "    "
		}
		fmt.Println(indent + hitLine(h))
	}
}

func rankByUnit(hits []store.Hit) ([]store.Hit, map[string]bool) {
	comparable := func(h store.Hit) (int64, string, bool) {
		if h.Unit == "" || h.PricePerUnit <= 0 {
			return 0, "", false
		}
		return cents(h.PricePerUnit), h.Unit, true
	}
	min := map[string]int64{}
	for _, h := range hits {
		if c, u, ok := comparable(h); ok {
			if m, seen := min[u]; !seen || c < m {
				min[u] = c
			}
		}
	}
	winners := map[string]bool{}
	for _, h := range hits {
		if c, u, ok := comparable(h); ok && c == min[u] {
			winners[h.ID] = true
		}
	}
	sorted := append([]store.Hit(nil), hits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ci, _, oki := comparable(sorted[i])
		cj, _, okj := comparable(sorted[j])
		if oki != okj {
			return oki
		}
		if !oki {
			return false
		}
		return ci < cj
	})
	return sorted, winners
}

func cmdBatch(args []string) error {
	fs, cf := newCommonFlags("batch")
	file := fs.String("f", "", "file with one term per line ('-' for stdin); else terms are positional")
	eco := fs.Bool("eco", false, "prefer ecological hits (where supported)")
	candidates := fs.Int("candidates", 0, "with --json/--toon: include top N scored hits per term")
	parseFlags(fs, args)
	if *candidates > 0 && !cf.jsonOut && !cf.toon {
		return fmt.Errorf("--candidates requires --json or --toon")
	}

	terms, err := collectLines(*file, fs.Args())
	if err != nil {
		return err
	}
	if len(terms) == 0 {
		return fmt.Errorf("no terms (use -f file, stdin, or '<term>...' args)")
	}
	st, err := newStore(cf)
	if err != nil {
		return err
	}

	type candidateRow struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		Price        float64 `json:"price,omitempty"`
		Unit         string  `json:"unit,omitempty"`
		PricePerUnit float64 `json:"pricePerUnit,omitempty"`
		Score        int     `json:"score"`
		Passed       bool    `json:"passed"`
		RejectReason string  `json:"rejectReason,omitempty"`
	}
	type row struct {
		Term       string         `json:"term"`
		Product    *store.Hit     `json:"product"`
		Candidates []candidateRow `json:"candidates,omitempty"`
	}
	out := make([]row, 0, len(terms))
	missing := 0
	for _, t := range terms {
		hits, serr := st.Search(t, 10, *eco)
		if serr != nil {
			return storeErr(st, serr)
		}
		r := row{Term: t}
		if *candidates > 0 {
			for _, c := range match.TopCandidates(t, hits, *candidates) {
				r.Candidates = append(r.Candidates, candidateRow{
					ID: c.Hit.ID, Name: c.Hit.Name, Price: c.Hit.Price,
					Unit: c.Hit.Unit, PricePerUnit: c.Hit.PricePerUnit,
					Score: c.Score, Passed: c.Passed, RejectReason: c.RejectReason,
				})
			}
		}
		if len(hits) == 0 {
			out = append(out, r)
			missing++
			continue
		}
		if best, ok := match.Select(t, hits); ok {
			r.Product = &best
		} else {
			missing++
		}
		out = append(out, r)
	}
	if done, err := emitStructured(cf, out); done {
		return err
	}
	w := 0
	for _, r := range out {
		if len(r.Term) > w {
			w = len(r.Term)
		}
	}
	for _, r := range out {
		if r.Product == nil {
			fmt.Printf("• %-*s → (no results)\n", w, r.Term)
			continue
		}
		fmt.Printf("• %-*s → %s\n", w, r.Term, hitLine(*r.Product))
	}
	if missing > 0 {
		return fmt.Errorf("%d of %d terms returned no product", missing, len(terms))
	}
	return nil
}

func cmdTotal(args []string) error {
	fs, cf := newCommonFlags("total")
	file := fs.String("f", "", "file with one '<id> [qty]' per line ('-' for stdin); else ids are positional")
	parseFlags(fs, args)

	lines, err := collectBasket(*file, fs.Args())
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		return fmt.Errorf("no basket lines (use -f file, stdin, or '<id> [<id>...]' args)")
	}
	st, err := newStore(cf)
	if err != nil {
		return err
	}

	type lineResult struct {
		ID       string  `json:"id"`
		Name     string  `json:"name,omitempty"`
		Qty      float64 `json:"qty"`
		Price    string  `json:"price,omitempty"`
		Subtotal string  `json:"subtotal,omitempty"`
		Error    string  `json:"error,omitempty"`
	}
	results := make([]lineResult, 0, len(lines))
	var totalCents int64
	failed := 0
	cur := "EUR"
	for _, bl := range lines {
		pd, perr := st.Product(bl.id)
		if perr != nil {
			if errors.Is(perr, store.ErrUnsupported) {
				return storeErr(st, perr)
			}
			results = append(results, lineResult{ID: bl.id, Qty: bl.qty, Error: perr.Error()})
			failed++
			continue
		}
		if pd.Currency != "" {
			cur = pd.Currency
		}
		sub := lineCents(cents(pd.Price), bl.qty)
		totalCents += sub
		results = append(results, lineResult{
			ID: bl.id, Name: pd.Name, Qty: bl.qty,
			Price: centsStr(cents(pd.Price)), Subtotal: centsStr(sub),
		})
	}
	if done, err := emitStructured(cf, map[string]any{
		"lines": results, "total": centsStr(totalCents), "count": len(lines), "complete": failed == 0,
	}); done {
		return err
	}
	sym := currencySymbol(cur)
	for _, r := range results {
		if r.Error != "" {
			fmt.Printf("  [%s] %s  ERROR: %s\n", r.ID, firstNonEmpty(r.Name, "?"), r.Error)
			continue
		}
		fmt.Printf("  [%s] %s — %s × %s%s = %s%s\n", r.ID, r.Name, fmtQty(r.Qty), r.Price, sym, r.Subtotal, sym)
	}
	fmt.Printf("  total: %s%s  (%s)\n", centsStr(totalCents), sym, plural(len(lines), "line", "lines"))
	if failed > 0 {
		return fmt.Errorf("%d of %d lines could not be priced — check the ids exist", failed, len(lines))
	}
	return nil
}

func currencySymbol(cur string) string {
	if strings.ToUpper(cur) == "EUR" || cur == "" {
		return "€"
	}
	return " " + cur
}

func cmdProduct(args []string) error {
	fs, cf := newCommonFlags("product")
	parseFlags(fs, args)
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: grocery product <id>")
	}
	st, err := newStore(cf)
	if err != nil {
		return err
	}
	pd, err := st.Product(fs.Arg(0))
	if err != nil {
		return storeErr(st, err)
	}
	if done, err := emitStructured(cf, pd); done {
		return err
	}
	fmt.Printf("[%s] %s\n", pd.ID, pd.Name)
	if pd.Brand != "" {
		fmt.Printf("  brand: %s\n", pd.Brand)
	}
	fmt.Printf("  price: %s", money(pd.Price, pd.Currency))
	if suf := unitSuffix(pd.Unit); suf != "" && pd.PricePerUnit > 0 {
		fmt.Printf("  (%s%s)", money(pd.PricePerUnit, pd.Currency), suf)
	}
	if pd.OnSale {
		fmt.Print("  ⟨on sale⟩")
	}
	fmt.Println()
	if pd.Origin != "" {
		fmt.Printf("  origin: %s\n", pd.Origin)
	}
	if pd.EAN != "" {
		fmt.Printf("  EAN: %s\n", pd.EAN)
	}
	if pd.Conservation != "" {
		fmt.Printf("  storage: %s\n", pd.Conservation)
	}
	if pd.Ingredients != "" {
		fmt.Printf("  ingredients: %s\n", pd.Ingredients)
	}
	if pd.Nutrients != "" {
		fmt.Println("  nutrition:")
		for _, n := range strings.Split(pd.Nutrients, ",") {
			if n = strings.TrimSpace(n); n != "" {
				fmt.Printf("    %s\n", n)
			}
		}
	}
	if pd.URL != "" {
		fmt.Printf("  url: %s\n", pd.URL)
	}
	return nil
}

func cmdCategories(args []string) error {
	fs, cf := newCommonFlags("categories")
	id := fs.String("id", "", "category id → list that category's products")
	depth := fs.Int("depth", 1, "tree depth when listing the tree")
	limit := fs.Int("limit", 0, "max products with --id")
	eco := fs.Bool("eco", false, "with --id: only ecological products")
	cheapest := fs.Bool("cheapest", false, "with --id: rank by unit price")
	parseFlags(fs, args)
	st, err := newStore(cf)
	if err != nil {
		return err
	}

	if *id != "" {
		hits, err := st.CategoryProducts(*id, *limit, *eco)
		if err != nil {
			return storeErr(st, err)
		}
		var winners map[string]bool
		if *cheapest {
			hits, winners = rankByUnit(hits)
		}
		if done, err := emitStructured(cf, hits); done {
			return err
		}
		if len(hits) == 0 {
			fmt.Println("(no products)")
			return nil
		}
		printHits(hits, winners, *cheapest)
		return nil
	}
	cats, err := st.Categories(*depth)
	if err != nil {
		return storeErr(st, err)
	}
	if done, err := emitStructured(cf, cats); done {
		return err
	}
	printCats(cats, 0)
	return nil
}

func printCats(cats []store.Category, indent int) {
	pad := strings.Repeat("  ", indent)
	for _, c := range cats {
		fmt.Printf("%s%s  %s\n", pad, c.ID, c.Name)
		printCats(c.Children, indent+1)
	}
}

// storeErr turns an adapter error into a friendly message, especially for
// commands a store doesn't support yet.
func storeErr(st store.Store, err error) error {
	if errors.Is(err, store.ErrUnsupported) {
		return fmt.Errorf("store %q doesn't support this command yet (see `grocery stores`)", st.Key())
	}
	return err
}
