package match

import (
	"regexp"
	"strconv"
	"strings"
)

// Size is a normalised package amount parsed from free text or hit metadata.
type Size struct {
	Grams  float64 // weight in grams; 0 if not a weight
	ML     float64 // volume in millilitres; 0 if not a volume
	Count  float64 // pack count when no unit (e.g. "12 eggs")
	HasQty bool
}

var sizeRE = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(kg|g|gr|ml|l|cl)\b`)

// ParseSize extracts the first quantity+unit from text. Hit.Unit (kg|L|u) and
// PricePerUnit are used only when the name carries no parseable size.
func ParseSize(text string, unit string, pricePerUnit float64) Size {
	text = strings.TrimSpace(text)
	if m := sizeRE.FindStringSubmatch(text); len(m) == 3 {
		return sizeFromMatch(m[1], m[2])
	}
	// Bare count: trailing integer with no unit (e.g. "eggs 12").
	if f := strings.Fields(text); len(f) > 0 {
		if n, err := strconv.ParseFloat(strings.ReplaceAll(f[len(f)-1], ",", "."), 64); err == nil && n > 0 {
			if !sizeRE.MatchString(f[len(f)-1]) {
				return Size{Count: n, HasQty: true}
			}
		}
	}
	if unit != "" && pricePerUnit > 0 {
		switch unit {
		case "kg":
			return Size{Grams: 1000, HasQty: true}
		case "L":
			return Size{ML: 1000, HasQty: true}
		case "u":
			return Size{Count: 1, HasQty: true}
		}
	}
	return Size{}
}

func sizeFromMatch(num, unit string) Size {
	n, _ := strconv.ParseFloat(strings.ReplaceAll(num, ",", "."), 64)
	u := strings.ToLower(strings.TrimSpace(unit))
	switch u {
	case "kg":
		return Size{Grams: n * 1000, HasQty: true}
	case "g", "gr":
		return Size{Grams: n, HasQty: true}
	case "l":
		return Size{ML: n * 1000, HasQty: true}
	case "ml":
		return Size{ML: n, HasQty: true}
	case "cl":
		return Size{ML: n * 10, HasQty: true}
	default:
		return Size{}
	}
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
func SizeCompatible(want, got Size) (ok bool, reason string) {
	if !want.HasQty {
		return true, ""
	}
	if !got.HasQty {
		return true, "" // no product size to compare
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