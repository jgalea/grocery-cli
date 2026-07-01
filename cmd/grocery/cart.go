package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/jgalea/grocery-cli/internal/store"
)

// carterFor resolves the selected store and asserts it supports an authenticated
// cart, with a friendly error otherwise.
func carterFor(cf *common) (store.Carter, store.Store, error) {
	st, err := newStore(cf)
	if err != nil {
		return nil, nil, err
	}
	ca, ok := st.(store.Carter)
	if !ok {
		return nil, st, fmt.Errorf("store %q has no cart — it's read-only (search/compare only)", st.Key())
	}
	return ca, st, nil
}

// cmdLogin authenticates with the user's OWN credentials. The password is read
// without echo and never stored (only the resulting token is cached).
func cmdLogin(args []string) error {
	fs, cf := newCommonFlags("login")
	user := fs.String("user", "", "account email/username (prompted if omitted)")
	parseFlags(fs, args)
	ca, st, err := carterFor(cf)
	if err != nil {
		return err
	}

	username := *user
	if username == "" {
		fmt.Fprint(os.Stderr, "Email: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		username = strings.TrimSpace(line)
	}
	if username == "" {
		return fmt.Errorf("no email provided")
	}
	fmt.Fprintf(os.Stderr, "Password for %s at %s: ", username, st.Key())
	pwBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return fmt.Errorf("reading password: %w", err)
	}
	if err := ca.Login(username, strings.TrimSpace(string(pwBytes))); err != nil {
		return err
	}
	fmt.Printf("Logged in to %s. The session is cached; the CLI never stores your password.\n", st.Key())
	return nil
}

// cmdCart handles: cart [get] | cart add <id> <qty> | cart set <id> <qty> | cart clear.
func cmdCart(args []string) error {
	fs, cf := newCommonFlags("cart")
	maxEUR := fs.Float64("max", 0, "refuse a line whose cost exceeds this amount (safety cap)")
	parseFlags(fs, args)
	rest := fs.Args()
	sub := "get"
	if len(rest) > 0 {
		sub = rest[0]
		rest = rest[1:]
	}
	ca, st, err := carterFor(cf)
	if err != nil {
		return err
	}
	if !ca.LoggedIn() {
		return fmt.Errorf("not logged in; run `grocery --store %s login` first", st.Key())
	}

	switch sub {
	case "get":
		cart, err := ca.CartGet()
		if err != nil {
			return err
		}
		return showCart(cf, cart)
	case "add", "set":
		if len(rest) < 1 {
			return fmt.Errorf("usage: grocery --store %s cart %s <id> [qty]", st.Key(), sub)
		}
		id := rest[0]
		qty := 1.0
		if len(rest) > 1 {
			if q, e := strconv.ParseFloat(rest[1], 64); e == nil {
				qty = q
			}
		}
		if *maxEUR > 0 && qty > 0 {
			if pd, e := st.Product(id); e == nil {
				if cost := pd.Price * qty; cost > *maxEUR {
					return fmt.Errorf("refused: %s × %s = %.2f exceeds --max %.2f", pd.Name, fmtQty(qty), cost, *maxEUR)
				}
			}
		}
		var cart *store.Cart
		if sub == "add" {
			cart, err = ca.CartAdd(id, qty)
		} else {
			cart, err = ca.CartSet(id, qty)
		}
		if err != nil {
			return err
		}
		return showCart(cf, cart)
	case "clear":
		cart, err := ca.CartClear()
		if err != nil {
			return err
		}
		return showCart(cf, cart)
	default:
		return fmt.Errorf("unknown cart subcommand %q (get|add|set|clear)", sub)
	}
}

func showCart(cf *common, cart *store.Cart) error {
	if done, err := emitStructured(cf, cart); done {
		return err
	}
	if cart.Count == 0 {
		fmt.Println("(cart is empty)")
		return nil
	}
	sym := currencySymbol(cart.Currency)
	for _, l := range cart.Lines {
		fmt.Printf("  [%s] %s — %s × %s%s\n", l.ID, l.Name, fmtQty(l.Qty), centsStr(cents(l.Price)), sym)
	}
	fmt.Printf("  total: %s%s  (%s)\n", centsStr(cents(cart.Total)), sym, plural(cart.Count, "line", "lines"))
	fmt.Println("  (review and pay in the browser — the CLI never places the order)")
	return nil
}
