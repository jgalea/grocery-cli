// Package registry maps a store key (--store) to a live adapter, and holds the
// per-store configuration. Add a supermarket here; commands never change.
package registry

import (
	"fmt"
	"sort"

	"github.com/jgalea/grocery-cli/internal/consum"
	"github.com/jgalea/grocery-cli/internal/dia"
	"github.com/jgalea/grocery-cli/internal/mercadona"
	"github.com/jgalea/grocery-cli/internal/scapi"
	"github.com/jgalea/grocery-cli/internal/ssr"
	"github.com/jgalea/grocery-cli/internal/store"
)

// Meta describes a store for the registry and `stores` listing.
type Meta struct {
	Key     string
	Label   string
	Country string   // ES | PT
	Langs   []string // selectable --lang values
	Backend string   // scapi | ssr
	Caps    []string // supported commands
	new     func(lang string, logf func(string, ...any)) store.Store
}

// Default is the store used when --store is omitted.
const Default = "ametller"

var metas = []Meta{
	{
		Key: "ametller", Label: "Ametller Origen", Country: "ES",
		Langs: []string{"ca", "es"}, Backend: "scapi",
		Caps: []string{"search", "batch", "total", "product", "categories"},
		new: func(lang string, logf func(string, ...any)) store.Store {
			return scapi.New(scapi.Config{
				Key: "ametller", ShortCode: "4jppt37a", Org: "f_ecom_blzv_prd",
				ClientID: "fd3c9db8-2a0d-4f4b-9e74-294e068f9ae4", SiteID: "ametller",
				RedirectURI: "https://www.ametllerorigen.com/callback",
				EcoRefine:   "c_ao_preferencias=28",
				Locales:     map[string]string{"ca": "ca", "es": "es"}, DefaultLang: "ca",
			}, lang, logf)
		},
	},
	{
		Key: "mercadona", Label: "Mercadona", Country: "ES",
		Langs: []string{"es"}, Backend: "algolia+rest",
		Caps: []string{"search", "batch", "total", "product", "categories"},
		new: func(lang string, logf func(string, ...any)) store.Store {
			return mercadona.New(mercadona.Config{
				Key: "mercadona", BaseURL: "https://tienda.mercadona.es",
				// Public search-only Algolia creds embedded in Mercadona's web app.
				AlgoliaApp: "7UZJKL1DJ0", AlgoliaKey: "9d8f2e39e90df472b4f2e559a116fe17",
				IndexBase: "products_prod", Warehouse: "bcn1", Lang: "es",
			}, logf)
		},
	},
	{
		Key: "dia", Label: "DIA", Country: "ES",
		Langs: []string{"es"}, Backend: "rest",
		Caps: []string{"search", "batch"},
		new: func(lang string, logf func(string, ...any)) store.Store {
			return dia.New("dia", logf)
		},
	},
	{
		Key: "consum", Label: "Consum", Country: "ES",
		Langs: []string{"es"}, Backend: "rest",
		Caps: []string{"search", "batch", "total", "product"},
		new: func(lang string, logf func(string, ...any)) store.Store {
			return consum.New("consum", logf)
		},
	},
	{
		Key: "continente", Label: "Continente", Country: "PT",
		Langs: []string{"pt"}, Backend: "ssr",
		Caps: []string{"search", "batch"},
		new: func(lang string, logf func(string, ...any)) store.Store {
			return ssr.New(ssr.Config{
				Key: "continente", BaseURL: "https://www.continente.pt",
				SiteID: "continente", Locale: "pt_PT", Currency: "EUR",
			}, logf)
		},
	},
	{
		Key: "auchan", Label: "Auchan", Country: "PT",
		Langs: []string{"pt"}, Backend: "ssr",
		Caps: []string{"search", "batch"},
		new: func(lang string, logf func(string, ...any)) store.Store {
			return ssr.New(ssr.Config{
				Key: "auchan", BaseURL: "https://www.auchan.pt",
				SiteID: "AuchanPT", Locale: "pt", Currency: "EUR",
			}, logf)
		},
	},
}

var byKey = func() map[string]Meta {
	m := map[string]Meta{}
	for _, meta := range metas {
		m[meta.Key] = meta
	}
	return m
}()

// Get builds the adapter for key, or errors with the list of valid keys.
func Get(key, lang string, logf func(string, ...any)) (store.Store, error) {
	meta, ok := byKey[key]
	if !ok {
		return nil, fmt.Errorf("unknown store %q (see `grocery stores`)", key)
	}
	return meta.new(lang, logf), nil
}

// List returns store metadata sorted by key, for the `stores` command.
func List() []Meta {
	out := append([]Meta(nil), metas...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
