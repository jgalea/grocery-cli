package aldi

import "testing"

func TestNameFoldsSalesUnit(t *testing.T) {
	cases := []struct {
		name, salesUnit, want string
	}{
		{"Leche evaporada", "340 g unidad", "Leche evaporada 340 g"},
		{"Leche entera", "1 l unidad", "Leche entera 1 l"},
		{"Yogur natural", "4 x 125 g unidad", "Yogur natural 4 x 125 g"},
		{"Sorbete de mango ecológico", "", "Sorbete de mango ecológico"},
		// Already carries the size: not repeated.
		{"Coca Cola Zero azúcar 1,25 l", "1,25 l unidad", "Coca Cola Zero azúcar 1,25 l"},
	}
	for _, c := range cases {
		it := aldiItem{Name: c.name, SalesUnit: c.salesUnit}
		if got := it.name(); got != c.want {
			t.Errorf("name(%q, %q) = %q, want %q", c.name, c.salesUnit, got, c.want)
		}
	}
}

func TestEco(t *testing.T) {
	cases := []struct {
		it   aldiItem
		want bool
	}{
		{aldiItem{Name: "Sorbete de mango ecológico", BrandName: "GUTBIO®"}, true},
		{aldiItem{Name: "Semillas de lino", CategoryIDs: []string{"bio"}}, true},
		{aldiItem{Name: "Espaguetis de soja ecológicos", BrandName: "MUCCI®"}, true},
		{aldiItem{Name: "Leche entera", BrandName: "MILSANI®", CategoryIDs: []string{"leche-y-bebidas-vegetales"}}, false},
	}
	for _, c := range cases {
		if got := c.it.eco(); got != c.want {
			t.Errorf("eco(%q/%q) = %v, want %v", c.it.Name, c.it.BrandName, got, c.want)
		}
	}
}

func TestCategoryDropsBrandTree(t *testing.T) {
	it := aldiItem{}
	it.HierarchicalCategories.Lvl1 = []string{"Marcas > Milsani", "Lácteos y huevos > Leche y bebidas vegetales"}
	if got, want := it.category(), "Leche y bebidas vegetales"; got != want {
		t.Errorf("category() = %q, want %q", got, want)
	}
}

func TestCategoryFilter(t *testing.T) {
	cases := []struct{ in, want string }{
		{"yogures-y-postres-lacteos", "categoryIDs:yogures-y-postres-lacteos"},
		{"Congelados > Helados y postres", `hierarchicalCategories.lvl1:"Congelados > Helados y postres"`},
		{"Bebidas alcohólicas", `hierarchicalCategories.lvl0:"Bebidas alcohólicas"`},
	}
	for _, c := range cases {
		if got := categoryFilter(c.in); got != c.want {
			t.Errorf("categoryFilter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
