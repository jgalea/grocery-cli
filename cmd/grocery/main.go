// Command grocery is an unofficial, agent-friendly CLI for several online
// supermarkets behind one interface. Pick a store with --store (or GROCERY_STORE)
// and run search / batch / total / product / categories; each store is an adapter
// (Salesforce Commerce Cloud guest API, or a server-rendered storefront). Every
// command supports --json and --toon for scripts and agents.
package main

import (
	"fmt"
	"os"
	"strings"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	args = hoistGlobalFlags(args)
	if len(args) < 1 {
		usage()
		return 2
	}
	var err error
	switch args[0] {
	case "search":
		err = cmdSearch(args[1:])
	case "batch":
		err = cmdBatch(args[1:])
	case "compare":
		err = cmdCompare(args[1:])
	case "total":
		err = cmdTotal(args[1:])
	case "product":
		err = cmdProduct(args[1:])
	case "categories":
		err = cmdCategories(args[1:])
	case "login":
		err = cmdLogin(args[1:])
	case "cart":
		err = cmdCart(args[1:])
	case "usuals":
		err = cmdUsuals(args[1:])
	case "stores":
		err = cmdStores(args[1:])
	case "version", "--version", "-v":
		fmt.Println(versionString())
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// hoistGlobalFlags lets global flags precede the command
// (`grocery --store continente search leite`) by moving any leading flags to
// after the command token, where the per-command flag set parses them.
func hoistGlobalFlags(args []string) []string {
	var lead []string
	i := 0
	for i < len(args) {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			break
		}
		lead = append(lead, a)
		name := strings.TrimLeft(a, "-")
		if !strings.Contains(name, "=") && (name == "store" || name == "lang") && i+1 < len(args) {
			i++
			lead = append(lead, args[i])
		}
		i++
	}
	if i >= len(args) {
		return args // no command token (e.g. bare --help/--version)
	}
	out := make([]string, 0, len(args))
	out = append(out, args[i])       // command
	out = append(out, args[i+1:]...) // its args
	return append(out, lead...)      // hoisted global flags
}

func versionString() string {
	if commit == "" {
		return version
	}
	if date != "" {
		return fmt.Sprintf("%s (%s, %s)", version, commit, date)
	}
	return fmt.Sprintf("%s (%s)", version, commit)
}

func usage() {
	fmt.Fprint(os.Stderr, `grocery — unofficial CLI for online supermarkets (pick one with --store)

USAGE:
  grocery [--store <key>] <command> [flags]

STORES:
  grocery stores            list supported stores and what each supports
  --store <key>             choose a store (or set GROCERY_STORE); default: ametller

COMMANDS:
  search <term...>          full-text product search
                            --limit N     cap results
                            --eco         only ecological products (where supported)
                            --cheapest    rank by unit price (€/kg·L·u), mark the best per unit
  batch [-f file]           cheapest hit per term (one per line, '#' comments ok; or positional)
                            --eco
  compare [-f file]         price one shopping list across several stores and rank them
                            --stores a,b,c   stores to compare
                            --country ES     or: all search-capable stores in a country
                            --detail         show the matched product per item
                            --eco
  total [-f file]           deterministic basket total from '<id> [qty]' lines
  product <id>              product detail (price, brand, origin, ingredients, nutrition)
  categories [--id N]       category tree, or one category's products with --id
                            --depth N, --limit N, --eco, --cheapest

CART (stores with account support, e.g. mercadona — you log in yourself):
  login [--user email]      log in with your OWN account (password read hidden, never stored)
  cart [get]                show your cart
  cart add <id> [qty]       add to cart (--max <eur> refuses an over-cap line)
  cart set <id> [qty]       set absolute qty (0 removes)
  cart clear                empty the cart
                            (the CLI fills the cart; you review and pay in the browser)
  usuals                    list regularly-bought items from local purchase history
                            --store <key>   filter to one store
                            --min N         minimum times bought (default 2)
  usuals order --store <key>  refill the cart with usual items for that store
                            --dry-run       print plan without adding
                            --min N

COMMON FLAGS (anywhere after the command):
  --store <key>             store to query
  --lang <code>             catalog language (store-specific, e.g. ca|es|pt)
  --json                    emit raw JSON (data → stdout, logs → stderr)
  --toon                    emit TOON instead of JSON (fewer tokens; for agents)

ENV:
  GROCERY_STORE             default store key
  GROCERY_CONFIG_DIR        override ~/.grocery (session cache)

  version | help

Unofficial. These talk to the same endpoints the stores' own sites use. Read
commands need no account; cart commands use your own login (see CART) and never
place an order. Use at a sane request rate.
`)
}
