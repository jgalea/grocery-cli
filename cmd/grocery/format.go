package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jgalea/grocery-cli/internal/store"
)

// money formats an amount with its currency symbol (€ for EUR, else the code).
func money(amount float64, currency string) string {
	s := strconv.FormatFloat(amount, 'f', 2, 64)
	switch strings.ToUpper(currency) {
	case "EUR", "":
		return s + "€"
	default:
		return s + " " + currency
	}
}

func unitSuffix(u string) string {
	switch u {
	case "kg", "L", "u":
		return "/" + u
	default:
		return ""
	}
}

// hitLine renders a one-line product summary.
func hitLine(h store.Hit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s — %s", h.ID, strings.TrimSpace(h.Name), money(h.Price, h.Currency))
	if suf := unitSuffix(h.Unit); suf != "" && h.PricePerUnit > 0 {
		fmt.Fprintf(&b, " (%s%s)", money(h.PricePerUnit, h.Currency), suf)
	}
	if !h.Available {
		b.WriteString("  [no disponible]")
	}
	if h.Eco {
		b.WriteString("  🌱")
	}
	return b.String()
}
