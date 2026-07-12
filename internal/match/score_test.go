package match

import (
	"testing"

	"github.com/jgalea/grocery-cli/internal/store"
)

func hit(id, name string, price float64, unit string, ppu float64) store.Hit {
	return store.Hit{ID: id, Name: name, Price: price, Unit: unit, PricePerUnit: ppu, Available: true}
}

func TestSelectRejectsJunkMatches(t *testing.T) {
	cases := []struct {
		term string
		bad  store.Hit
		good store.Hit
	}{
		{
			"onions",
			hit("1", "Rosemary & Onions Crackers 125g", 1.29, "", 0),
			hit("2", "Brown Onions 1kg", 1.49, "kg", 1.49),
		},
		{
			"butter",
			hit("1", "Peanut Butter Snack 45g", 0.89, "", 0),
			hit("2", "Butter 250g", 2.19, "kg", 8.76),
		},
		{
			"chicken breast",
			hit("1", "Frozen Chicken Nuggets 450g", 3.99, "", 0),
			hit("2", "Fresh Chicken Breast Fillets 500g", 5.49, "kg", 10.98),
		},
		{
			"rice 1kg",
			hit("1", "Plain Flour 1kg", 0.99, "kg", 0.99),
			hit("2", "White Rice 1kg", 1.79, "kg", 1.79),
		},
		{
			"pasta 500g",
			hit("1", "Potato Gnocchi 500g", 1.49, "", 0),
			hit("2", "Penne Pasta 500g", 0.89, "kg", 1.78),
		},
	}

	for _, tc := range cases {
		t.Run(tc.term, func(t *testing.T) {
			if _, ok := Select(tc.term, []store.Hit{tc.bad}); ok {
				t.Fatalf("should reject junk hit %q for term %q", tc.bad.Name, tc.term)
			}
			got, ok := Select(tc.term, []store.Hit{tc.bad, tc.good})
			if !ok {
				t.Fatalf("expected a match for term %q", tc.term)
			}
			if got.ID != tc.good.ID {
				t.Fatalf("Select() = %q (%s) want %q (%s)", got.ID, got.Name, tc.good.ID, tc.good.Name)
			}
		})
	}
}

func TestSelectNoMatch(t *testing.T) {
	hits := []store.Hit{
		hit("1", "Rosemary & Onions Crackers 125g", 1.29, "", 0),
		hit("2", "Ryvita Thins Caramel Onions 125g", 3.65, "", 0),
	}
	if _, ok := Select("onions", hits); ok {
		t.Fatal("expected no passing hit for processed-food-only results")
	}
}

func TestScoreAllRejectReasons(t *testing.T) {
	r := ScoreAll("butter", []store.Hit{
		hit("1", "Peanut Butter Snack 45g", 0.89, "", 0),
	})[0]
	if r.Passed {
		t.Fatal("expected rejection")
	}
	if r.RejectReason == "" {
		t.Fatal("expected reject reason")
	}
}
