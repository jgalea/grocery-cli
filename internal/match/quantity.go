package match

import (
	"regexp"
	"strconv"
	"strings"
)

// Size is a normalised package amount parsed from free text or hit metadata.
// Grams, ML and Count are pack TOTALS: a 6x1.5L pack is ML 9000, Packs 6. Per-item
// size is the total divided by Packs. Pricing wants the total; matching a shopper's
// "1.5L" against that pack wants the per-item figure (see SizeCompatible).
type Size struct {
	Grams  float64 // total weight in grams; 0 if not a weight
	ML     float64 // total volume in millilitres; 0 if not a volume
	Count  float64 // pack count when no unit (e.g. "12 eggs")
	Packs  float64 // items in the pack; 1 for a single
	HasQty bool
}

var (
	sizeRE = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(kg|g|gr|ml|l|cl)\b`)
	// "6x1.5L", "4 x 500 ml", "3×80g"
	multiRE = regexp.MustCompile(`(?i)(\d+)\s*[x×]\s*(\d+(?:[.,]\d+)?)\s*(kg|g|gr|ml|l|cl)\b`)
)

// ParseSize extracts the pack quantity from text, preferring an explicit multipack
// (NxM) over a bare size. Hit.Unit (kg|L|u) and PricePerUnit are used only when the
// name carries no parseable size.
func ParseSize(text string, unit string, pricePerUnit float64) Size {
	text = strings.TrimSpace(text)
	if m := multiRE.FindStringSubmatch(text); len(m) == 4 {
		packs, _ := strconv.ParseFloat(m[1], 64)
		if packs > 0 {
			s := sizeFromMatch(m[2], m[3])
			if s.HasQty {
				s.Grams *= packs
				s.ML *= packs
				s.Packs = packs
				return s
			}
		}
	}
	if m := sizeRE.FindStringSubmatch(text); len(m) == 3 {
		return sizeFromMatch(m[1], m[2])
	}
	// Bare count: trailing integer with no unit (e.g. "eggs 12").
	if f := strings.Fields(text); len(f) > 0 {
		if n, err := strconv.ParseFloat(strings.ReplaceAll(f[len(f)-1], ",", "."), 64); err == nil && n > 0 {
			if !sizeRE.MatchString(f[len(f)-1]) {
				return Size{Count: n, Packs: 1, HasQty: true}
			}
		}
	}
	if unit != "" && pricePerUnit > 0 {
		switch unit {
		case "kg":
			return Size{Grams: 1000, Packs: 1, HasQty: true}
		case "L":
			return Size{ML: 1000, Packs: 1, HasQty: true}
		case "u":
			return Size{Count: 1, Packs: 1, HasQty: true}
		}
	}
	return Size{}
}

func sizeFromMatch(num, unit string) Size {
	n, _ := strconv.ParseFloat(strings.ReplaceAll(num, ",", "."), 64)
	u := strings.ToLower(strings.TrimSpace(unit))
	switch u {
	case "kg":
		return Size{Grams: n * 1000, Packs: 1, HasQty: true}
	case "g", "gr":
		return Size{Grams: n, Packs: 1, HasQty: true}
	case "l":
		return Size{ML: n * 1000, Packs: 1, HasQty: true}
	case "ml":
		return Size{ML: n, Packs: 1, HasQty: true}
	case "cl":
		return Size{ML: n * 10, Packs: 1, HasQty: true}
	default:
		return Size{}
	}
}

// PerItem is the size of one item in the pack: 6x1.5L -> 1.5L.
func (s Size) PerItem() Size {
	if s.Packs <= 1 {
		return s
	}
	s.Grams /= s.Packs
	s.ML /= s.Packs
	s.Packs = 1
	return s
}

// PerUnit converts a pack price into a comparable €/kg, €/L or €/item figure. This
// is what makes a 750ml bottle at €5.00 rank behind a 1L at €5.60. Returns false
// when the pack size could not be parsed and no honest figure exists.
func PerUnit(price float64, s Size) (value float64, unit string, ok bool) {
	if price <= 0 || !s.HasQty {
		return 0, "", false
	}
	switch {
	case s.Grams > 0:
		return price / (s.Grams / 1000), "kg", true
	case s.ML > 0:
		return price / (s.ML / 1000), "L", true
	case s.Count > 0:
		return price / s.Count, "each", true
	}
	return 0, "", false
}

// StripQuantity removes parsed quantity tokens so core product words remain.
func StripQuantity(text string) string {
	out := sizeRE.ReplaceAllString(text, " ")
	f := strings.Fields(out)
	if len(f) > 0 {
		last := f[len(f)-1]
		if n, err := strconv.ParseFloat(strings.ReplaceAll(last, ",", "."), 64); err == nil && n > 0 {
			f = f[:len(f)-1]
			_ = n
		}
	}
	return strings.Join(f, " ")
}

// SizeCompatible reports whether product size is within 2× of the requested size.
// A single-item request is compared against the pack's per-item size, so asking for
// "water 1.5L" still matches a 6x1.5L pack rather than being rejected as 9L.
func SizeCompatible(want, got Size) (ok bool, reason string) {
	if !want.HasQty {
		return true, ""
	}
	if !got.HasQty {
		return true, "" // no product size to compare
	}
	if want.Packs <= 1 && got.Packs > 1 {
		got = got.PerItem()
	}
	if want.Grams > 0 {
		if got.Grams <= 0 {
			return true, ""
		}
		r := got.Grams / want.Grams
		if r < 0.5 || r > 2 {
			return false, "size mismatch"
		}
		return true, ""
	}
	if want.ML > 0 {
		if got.ML <= 0 {
			return true, ""
		}
		r := got.ML / want.ML
		if r < 0.5 || r > 2 {
			return false, "size mismatch"
		}
		return true, ""
	}
	if want.Count > 0 && got.Count > 0 {
		r := got.Count / want.Count
		if r < 0.5 || r > 2 {
			return false, "size mismatch"
		}
	}
	return true, ""
}