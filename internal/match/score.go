package match

import (
	"math"
	"sort"
	"strings"

	"github.com/jgalea/grocery-cli/internal/store"
)

// Result is one scored search hit for a term.
type Result struct {
	Hit          store.Hit `json:"hit"`
	Score        int       `json:"score"`
	Passed       bool      `json:"passed"`
	RejectReason string    `json:"rejectReason,omitempty"`
}

// ScoreAll scores every hit against term. Lower score is better among passing hits.
func ScoreAll(term string, hits []store.Hit) []Result {
	termSize := ParseSize(term, "", 0)
	termTokens := tokenize(StripQuantity(term))
	if len(termTokens) == 0 {
		return nil
	}

	out := make([]Result, len(hits))
	for i, h := range hits {
		out[i] = scoreOne(term, termTokens, termSize, h)
	}
	return out
}

func scoreOne(term string, termTokens []string, termSize Size, h store.Hit) Result {
	r := Result{Hit: h, Score: math.MaxInt32}

	prodTokens := tokenize(h.Name)
	if len(prodTokens) == 0 {
		r.RejectReason = "no product tokens"
		return r
	}

	if missing := missingTermTokens(termTokens, prodTokens); len(missing) > 0 {
		r.RejectReason = "missing term token: " + strings.Join(missing, ", ")
		return r
	}

	extra := extraTokens(termTokens, prodTokens)
	if len(termTokens) == 1 {
		if !tokenInList(prodTokens[0], termTokens) {
			last := prodTokens[len(prodTokens)-1]
			if !tokenInList(last, termTokens) {
				r.RejectReason = "product type mismatch"
				return r
			}
			if len(extra) >= 3 {
				r.RejectReason = "extra category tokens"
				return r
			}
		}
	} else if len(extra) > len(termTokens) {
		r.RejectReason = "extra category tokens"
		return r
	}

	prodSize := ParseSize(h.Name, h.Unit, h.PricePerUnit)
	if ok, reason := SizeCompatible(termSize, prodSize); !ok {
		r.RejectReason = reason
		return r
	}
	if len(termTokens) == 1 && !termSize.HasQty && prodSize.Grams > 0 && prodSize.Grams < 100 {
		r.RejectReason = "package too small"
		return r
	}

	r.Passed = true
	r.Score = len(extra)*10 + sizeDistance(termSize, prodSize)
	if h.Unit != "" && h.PricePerUnit > 0 {
		r.Score += 0 // prefer unit-priced hits via Select
	}
	_ = term
	return r
}

func sizeDistance(want, got Size) int {
	if !want.HasQty || !got.HasQty {
		return 0
	}
	dist := func(a, b float64) int {
		if a <= 0 || b <= 0 {
			return 0
		}
		r := a / b
		if r < 1 {
			r = 1 / r
		}
		return int((r - 1) * 10)
	}
	if want.Grams > 0 && got.Grams > 0 {
		return dist(want.Grams, got.Grams)
	}
	if want.ML > 0 && got.ML > 0 {
		return dist(want.ML, got.ML)
	}
	if want.Count > 0 && got.Count > 0 {
		return dist(want.Count, got.Count)
	}
	return 0
}

// Select picks the best passing hit, or false when none pass the gate.
func Select(term string, hits []store.Hit) (store.Hit, bool) {
	scored := ScoreAll(term, hits)
	var passing []Result
	for _, r := range scored {
		if r.Passed {
			passing = append(passing, r)
		}
	}
	if len(passing) == 0 {
		return store.Hit{}, false
	}

	termSize := ParseSize(term, "", 0)
	sort.SliceStable(passing, func(i, j int) bool {
		a, b := passing[i], passing[j]
		pa := priceKey(a.Hit, termSize)
		pb := priceKey(b.Hit, termSize)
		if pa != pb {
			return pa < pb
		}
		if a.Score != b.Score {
			return a.Score < b.Score
		}
		return a.Hit.ID < b.Hit.ID
	})
	return passing[0].Hit, true
}

func priceKey(h store.Hit, termSize Size) int64 {
	if termSize.HasQty && h.Unit != "" && h.PricePerUnit > 0 {
		return int64(math.Round(h.PricePerUnit * 100))
	}
	if h.Unit != "" && h.PricePerUnit > 0 {
		return int64(math.Round(h.PricePerUnit * 100))
	}
	return int64(math.Round(h.Price * 100))
}

// TopCandidates returns up to n results sorted by pass status, then score, then price.
func TopCandidates(term string, hits []store.Hit, n int) []Result {
	if n <= 0 {
		return nil
	}
	scored := ScoreAll(term, hits)
	sort.SliceStable(scored, func(i, j int) bool {
		a, b := scored[i], scored[j]
		if a.Passed != b.Passed {
			return a.Passed
		}
		if a.Passed && b.Passed {
			pa := priceKey(a.Hit, ParseSize(term, "", 0))
			pb := priceKey(b.Hit, ParseSize(term, "", 0))
			if pa != pb {
				return pa < pb
			}
		}
		if a.Score != b.Score {
			return a.Score < b.Score
		}
		return a.Hit.ID < b.Hit.ID
	})
	if len(scored) > n {
		scored = scored[:n]
	}
	return scored
}