package makro

import (
	"math"
	"testing"
)

func TestPackName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"aro leche entera 1 L contiene 6 unidades", "aro leche entera 6x1 L"},
		{"PRÉSIDENT leche entera 1,5 L contiene 6 botellas", "PRÉSIDENT leche entera 6x1,5 L"},
		{"Rio leche entera 1 L contiene 6 briks (total 6 L)", "Rio leche entera 6x1 L"},
		{"aro Azúcar blanco 10 ud. x 1 Kg (total 10 Kg)", "aro Azúcar blanco 10x1 Kg"},
		{"METRO Chef Arroz largo en paquete 10 ud x 1 Kg (10 Kg)", "METRO Chef Arroz largo en paquete 10x1 Kg"},
		// Already shelf notation, or a plain single pack: left alone.
		{"aro arroz 10x1kg", "aro arroz 10x1kg"},
		{"METRO Chef Arroz largo en bolsa 5 Kg", "METRO Chef Arroz largo en bolsa 5 Kg"},
		// A pack count with no size to attach it to keeps the original wording.
		{"aro pinza azúcar contiene 3 unidades", "aro pinza azúcar contiene 3 unidades"},
	}
	for _, c := range cases {
		if got := packName(c.in); got != c.want {
			t.Errorf("packName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPerUnitUsesPackTotal(t *testing.T) {
	cases := []struct {
		name  string
		price float64
		want  float64
		unit  string
	}{
		{"aro leche entera 6x1 L", 5.50, 0.9167, "L"},
		{"PRÉSIDENT leche entera 6x1,5 L", 10.08, 1.12, "L"},
		{"aro Azúcar blanco 10x1 Kg", 8.68, 0.868, "kg"},
		{"METRO Chef Arroz largo en bolsa 5 Kg", 6.44, 1.288, "kg"},
		{"METRO Chef azucar avainillado 900 g", 6.55, 7.2778, "kg"},
		{"RIOBA aceite de oliva virgen 250 ml", 2.13, 8.52, "L"},
	}
	for _, c := range cases {
		got, unit, ok := perUnit(c.price, c.name)
		if !ok {
			t.Errorf("perUnit(%v, %q): no size found", c.price, c.name)
			continue
		}
		if unit != c.unit {
			t.Errorf("perUnit(%q) unit = %q, want %q", c.name, unit, c.unit)
		}
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("perUnit(%q) = %.4f, want %.4f", c.name, got, c.want)
		}
	}
}

func TestPerUnitNoSize(t *testing.T) {
	if _, _, ok := perUnit(20.30, "METRO Chef Morcillo ternera al vacío precio kg"); ok {
		t.Error("expected no size from a description with no quantity")
	}
	if _, _, ok := perUnit(0, "aro leche entera 6x1 L"); ok {
		t.Error("expected no per-unit price without a price")
	}
}
