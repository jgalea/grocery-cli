package main

import (
	"fmt"
	"strings"

	"github.com/jgalea/grocery-cli/internal/registry"
)

// cmdStores lists the supported stores and what each one can do.
func cmdStores(args []string) error {
	fs, cf := newCommonFlags("stores")
	parseFlags(fs, args)
	metas := registry.List()
	if done, err := emitStructured(cf, metas); done {
		return err
	}
	fmt.Printf("Supported stores (default: %s):\n\n", registry.Default)
	for _, m := range metas {
		fmt.Printf("  %-12s %s [%s, %s]\n", m.Key, m.Label, m.Country, m.Backend)
		fmt.Printf("               langs: %s | supports: %s\n",
			strings.Join(m.Langs, ", "), strings.Join(m.Caps, ", "))
	}
	fmt.Print("\nSelect a store with --store <key> or GROCERY_STORE.\n")
	return nil
}
