package main

import (
	"reflect"
	"testing"

	"github.com/jgalea/grocery-cli/internal/store"
)

func TestCentsStr(t *testing.T) {
	for _, tc := range []struct {
		c    int64
		want string
	}{{149, "1.49"}, {500, "5.00"}, {7, "0.07"}, {1230, "12.30"}} {
		if got := centsStr(tc.c); got != tc.want {
			t.Errorf("centsStr(%d)=%q want %q", tc.c, got, tc.want)
		}
	}
}

func TestLineCents(t *testing.T) {
	if got := lineCents(149, 2); got != 298 {
		t.Errorf("lineCents(149,2)=%d want 298", got)
	}
	if got := lineCents(100, 1.5); got != 150 {
		t.Errorf("lineCents(100,1.5)=%d want 150", got)
	}
}

func TestMoney(t *testing.T) {
	if got := money(1.49, "EUR"); got != "1.49€" {
		t.Errorf("money EUR = %q", got)
	}
	if got := money(2, "USD"); got != "2.00 USD" {
		t.Errorf("money USD = %q", got)
	}
}

func TestHoistGlobalFlags(t *testing.T) {
	// `grocery --store continente search leite` -> command first, flags hoisted after
	got := hoistGlobalFlags([]string{"--store", "continente", "search", "leite"})
	want := []string{"search", "leite", "--store", "continente"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hoist = %v want %v", got, want)
	}
	// bare flag with no command is left as-is (help/version)
	if got := hoistGlobalFlags([]string{"--help"}); !reflect.DeepEqual(got, []string{"--help"}) {
		t.Errorf("hoist(--help) = %v", got)
	}
}

func TestRankByUnit(t *testing.T) {
	hits := []store.Hit{
		{ID: "a", PricePerUnit: 2.0, Unit: "L"},
		{ID: "b", PricePerUnit: 1.0, Unit: "L"},
		{ID: "c", Price: 9, Unit: ""}, // not comparable, sorts last
	}
	sorted, winners := rankByUnit(hits)
	if sorted[0].ID != "b" {
		t.Errorf("cheapest per L should sort first, got %s", sorted[0].ID)
	}
	if !winners["b"] || winners["a"] {
		t.Errorf("winner should be b only: %v", winners)
	}
	if sorted[len(sorted)-1].ID != "c" {
		t.Errorf("non-comparable should sort last, got %s", sorted[len(sorted)-1].ID)
	}
}
