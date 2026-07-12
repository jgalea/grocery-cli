package main

import (
	"fmt"
	"time"

	"github.com/jgalea/grocery-cli/internal/history"
	"github.com/jgalea/grocery-cli/internal/store"
)

func cmdUsuals(args []string) error {
	if len(args) > 0 && args[0] == "order" {
		return cmdUsualsOrder(args[1:])
	}
	return cmdUsualsList(args)
}

func cmdUsualsList(args []string) error {
	fs, cf := newCommonFlags("usuals")
	min := fs.Int("min", 2, "minimum times bought to count as a usual")
	parseFlags(fs, args)

	records, err := history.Load()
	if err != nil {
		return err
	}
	usuals := history.Aggregate(records, cf.store, *min)
	if done, err := emitStructured(cf, usuals); done {
		return err
	}
	if len(usuals) == 0 {
		fmt.Println("(no usuals yet — add items with `cart add` to build history)")
		return nil
	}
	for _, u := range usuals {
		fmt.Printf("  [%s] %s — %s ×%s  (%d×, last %s)\n",
			u.ProductID, u.ProductName, u.Store, fmtQty(u.TypicalQty), u.TimesBought, u.LastBought)
	}
	return nil
}

func cmdUsualsOrder(args []string) error {
	fs, cf := newCommonFlags("usuals order")
	min := fs.Int("min", 2, "minimum times bought to count as a usual")
	dryRun := fs.Bool("dry-run", false, "print what would be added without touching the cart")
	parseFlags(fs, args)

	if cf.store == "" {
		return fmt.Errorf("usage: grocery usuals order --store <key> [--dry-run] [--min N]")
	}
	ca, st, err := carterFor(cf)
	if err != nil {
		return err
	}
	if !*dryRun && !ca.LoggedIn() {
		return fmt.Errorf("not logged in; run `grocery --store %s login` first", st.Key())
	}

	records, err := history.Load()
	if err != nil {
		return err
	}
	usuals := history.Aggregate(records, cf.store, *min)
	if len(usuals) == 0 {
		return fmt.Errorf("no usuals for store %q (need at least %d cart adds per item)", cf.store, *min)
	}

	type planLine struct {
		ID    string  `json:"id"`
		Name  string  `json:"name"`
		Qty   float64 `json:"qty"`
		Error string  `json:"error,omitempty"`
	}
	plan := make([]planLine, 0, len(usuals))
	for _, u := range usuals {
		plan = append(plan, planLine{ID: u.ProductID, Name: u.ProductName, Qty: u.TypicalQty})
	}
	if *dryRun {
		if done, err := emitStructured(cf, plan); done {
			return err
		}
		for _, l := range plan {
			fmt.Printf("  would add [%s] %s ×%s\n", l.ID, l.Name, fmtQty(l.Qty))
		}
		fmt.Printf("  (%d lines — dry run, cart untouched)\n", len(plan))
		return nil
	}

	var cart *store.Cart
	for _, u := range usuals {
		cart, err = ca.CartAdd(u.ProductID, u.TypicalQty)
		if err != nil {
			return err
		}
		recordCartAdd(st, u.ProductID, u.TypicalQty, cart)
	}
	return showCart(cf, cart)
}

func recordCartAdd(st store.Store, id string, qty float64, cart *store.Cart) {
	name := ""
	price := 0.0
	for _, l := range cart.Lines {
		if l.ID == id {
			name, price = l.Name, l.Price
			break
		}
	}
	if name == "" {
		if pd, err := st.Product(id); err == nil {
			name, price = pd.Name, pd.Price
		}
	}
	history.Append(history.Record{
		Store:       st.Key(),
		ProductID:   id,
		ProductName: name,
		Qty:         qty,
		UnitPrice:   price,
		Timestamp:   time.Now().UTC(),
	}, stderrLogf)
}
