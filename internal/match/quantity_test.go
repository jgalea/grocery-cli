package match

import "testing"

func TestParseSize(t *testing.T) {
	tests := []struct {
		text string
		unit string
		ppu  float64
		want Size
	}{
		{"rice 1kg", "", 0, Size{Grams: 1000, Packs: 1, HasQty: true}},
		{"500g", "", 0, Size{Grams: 500, Packs: 1, HasQty: true}},
		{"500 gr", "", 0, Size{Grams: 500, Packs: 1, HasQty: true}},
		{"pasta 500g", "", 0, Size{Grams: 500, Packs: 1, HasQty: true}},
		{"1l milk", "", 0, Size{ML: 1000, Packs: 1, HasQty: true}},
		{"1 l milk", "", 0, Size{ML: 1000, Packs: 1, HasQty: true}},
		{"330ml", "", 0, Size{ML: 330, Packs: 1, HasQty: true}},
		{"33 cl", "", 0, Size{ML: 330, Packs: 1, HasQty: true}},
		{"eggs 12", "", 0, Size{Count: 12, Packs: 1, HasQty: true}},
		{"butter", "kg", 8.5, Size{Grams: 1000, Packs: 1, HasQty: true}},
		{"no size here", "", 0, Size{}},

		// Multipacks: Grams/ML/Count are pack TOTALS, Packs is the pack count.
		{"San Michel Water 6x1.5L", "", 0, Size{ML: 9000, Packs: 6, HasQty: true}},
		{"Cola 6x330ml", "", 0, Size{ML: 1980, Packs: 6, HasQty: true}},
		{"Sparkling Water 4 x 500 ml", "", 0, Size{ML: 2000, Packs: 4, HasQty: true}},
		{"Tuna 3x80g", "", 0, Size{Grams: 240, Packs: 3, HasQty: true}},
		{"Yogurt 12x125g", "", 0, Size{Grams: 1500, Packs: 12, HasQty: true}},
		{"Long-life Milk 6x1L", "", 0, Size{ML: 6000, Packs: 6, HasQty: true}},
	}
	for _, tc := range tests {
		got := ParseSize(tc.text, tc.unit, tc.ppu)
		if got != tc.want {
			t.Errorf("ParseSize(%q, %q, %v) = %+v want %+v", tc.text, tc.unit, tc.ppu, got, tc.want)
		}
	}
}

// A single-bottle request should still match a multipack of that bottle: the
// shopper asking for "water 1.5L" is happy with a 6x1.5L pack. Compatibility
// therefore compares per-item size, while pricing uses the pack total.
func TestSizeCompatibleMultipack(t *testing.T) {
	want := Size{ML: 1500, Packs: 1, HasQty: true}
	sixPack := Size{ML: 9000, Packs: 6, HasQty: true}
	if ok, reason := SizeCompatible(want, sixPack); !ok {
		t.Errorf("6x1.5L should satisfy a 1.5L request, got reject: %s", reason)
	}
	wrongBottle := Size{ML: 3000, Packs: 6, HasQty: true} // 6x500ml
	if ok, _ := SizeCompatible(Size{ML: 2000, Packs: 1, HasQty: true}, wrongBottle); ok {
		t.Error("6x500ml should not satisfy a 2L request")
	}
}

func TestPerUnit(t *testing.T) {
	// €3.60 for 6x1.5L = 9L -> €0.40/L, not €2.40/L off a mis-parsed 1.5L.
	six := Size{ML: 9000, Packs: 6, HasQty: true}
	got, unit, ok := PerUnit(3.60, six)
	if !ok || unit != "L" {
		t.Fatalf("PerUnit unit = %q ok=%v, want L/true", unit, ok)
	}
	if got < 0.399 || got > 0.401 {
		t.Errorf("PerUnit = %v, want 0.40/L", got)
	}
	// 750ml at €5.00 is dearer per litre than 1L at €5.60.
	small, _, _ := PerUnit(5.00, Size{ML: 750, Packs: 1, HasQty: true})
	big, _, _ := PerUnit(5.60, Size{ML: 1000, Packs: 1, HasQty: true})
	if !(small > big) {
		t.Errorf("750ml@5.00 (%v/L) should be dearer than 1L@5.60 (%v/L)", small, big)
	}
	if _, _, ok := PerUnit(2.00, Size{}); ok {
		t.Error("PerUnit should not report a figure with no parsed size")
	}
}

func TestSizeCompatible(t *testing.T) {
	kg := Size{Grams: 1000, HasQty: true}
	half := Size{Grams: 500, HasQty: true}
	tiny := Size{Grams: 45, HasQty: true}
	triple := Size{Grams: 3000, HasQty: true}

	if ok, _ := SizeCompatible(kg, half); !ok {
		t.Error("500g should match 1kg request")
	}
	if ok, _ := SizeCompatible(kg, tiny); ok {
		t.Error("45g should not match 1kg request")
	}
	if ok, _ := SizeCompatible(kg, triple); ok {
		t.Error("3kg should not match 1kg request")
	}
	if ok, _ := SizeCompatible(Size{}, half); !ok {
		t.Error("no requested size should not reject")
	}
}