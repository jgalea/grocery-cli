package match

import (
	"strings"
	"unicode"
)

var accentFold = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o", "ö", "o",
	"ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ý", "y", "ÿ", "y",
	"ñ", "n", "ç", "c",
	"À", "a", "Á", "a", "Â", "a", "Ã", "a", "Ä", "a", "Å", "a",
	"È", "e", "É", "e", "Ê", "e", "Ë", "e",
	"Ì", "i", "Í", "i", "Î", "i", "Ï", "i",
	"Ò", "o", "Ó", "o", "Ô", "o", "Õ", "o", "Ö", "o",
	"Ù", "u", "Ú", "u", "Û", "u", "Ü", "u",
	"Ý", "y", "Ñ", "n", "Ç", "c",
)

func normalize(s string) string {
	return accentFold.Replace(strings.ToLower(strings.TrimSpace(s)))
}

func tokenize(s string) []string {
	s = normalize(StripQuantity(s))
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func termVariants(tok string) []string {
	tok = normalize(tok)
	if tok == "" {
		return nil
	}
	seen := map[string]bool{tok: true}
	variants := []string{tok}
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			variants = append(variants, v)
		}
	}
	if strings.HasSuffix(tok, "ies") && len(tok) > 4 {
		add(tok[:len(tok)-3] + "y")
	}
	if strings.HasSuffix(tok, "es") && len(tok) > 3 {
		add(tok[:len(tok)-2])
		add(tok[:len(tok)-1])
	}
	if strings.HasSuffix(tok, "s") && len(tok) > 2 {
		add(tok[:len(tok)-1])
	}
	if !strings.HasSuffix(tok, "s") {
		add(tok + "s")
	}
	return variants
}

func tokenInList(tok string, list []string) bool {
	for _, v := range termVariants(tok) {
		for _, t := range list {
			for _, tv := range termVariants(t) {
				if v == tv {
					return true
				}
			}
		}
	}
	return false
}

func missingTermTokens(termTokens, productTokens []string) []string {
	var missing []string
	for _, tt := range termTokens {
		if !tokenInList(tt, productTokens) {
			missing = append(missing, tt)
		}
	}
	return missing
}

func extraTokens(termTokens, productTokens []string) []string {
	var extra []string
	for _, pt := range productTokens {
		if tokenInList(pt, termTokens) {
			continue
		}
		extra = append(extra, pt)
	}
	return extra
}

