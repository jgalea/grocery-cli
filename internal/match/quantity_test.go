package match

import "testing"

func TestParseSize(t *testing.T) {
	tests := []struct {
		text string
		unit string
		ppu  float64
		want Size
	}{
		{"rice 1kg", "", 0, Size{Grams: 1000, HasQty: true}},
		{"500g", "", 0, Size{Grams: 500, HasQty: true}},
		{"500 gr", "", 0, Size{Grams: 500, HasQty: true}},
		{"pasta 500g", "", 0, Size{Grams: 500, HasQty: true}},
		{"1l milk", "", 0, Size{ML: 1000, HasQty: true}},
		{"1 l milk", "", 0, Size{ML: 1000, HasQty: true}},
		{"330ml", "", 0, Size{ML: 330, HasQty: true}},
		{"33 cl", "", 0, Size{ML: 330, HasQty: true}},
		{"eggs 12", "", 0, Size{Count: 12, HasQty: true}},
		{"butter", "kg", 8.5, Size{Grams: 1000, HasQty: true}},
		{"no size here", "", 0, Size{}},
	}
	for _, tc := range tests {
		got := ParseSize(tc.text, tc.unit, tc.ppu)
		if got != tc.want {
			t.Errorf("ParseSize(%q, %q, %v) = %+v want %+v", tc.text, tc.unit, tc.ppu, got, tc.want)
		}
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