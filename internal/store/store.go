// Package store defines the store-agnostic types and the Store interface that
// every retailer adapter implements. The CLI and every command speak only these
// types, so adding a supermarket means adding an adapter, not touching commands.
package store

import "errors"

// ErrUnsupported is returned by an adapter for a command it does not implement
// (e.g. an SSR-scraped store without a product-detail path yet).
var ErrUnsupported = errors.New("not supported for this store yet")

// Hit is one product in a search or category listing.
type Hit struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Price        float64 `json:"price"`
	PricePerUnit float64 `json:"pricePerUnit,omitempty"`
	Unit         string  `json:"unit,omitempty"` // kg | L | u (normalised)
	Currency     string  `json:"currency,omitempty"`
	Brand        string  `json:"brand,omitempty"`
	Category     string  `json:"category,omitempty"`
	Eco          bool    `json:"eco,omitempty"`
	Available    bool    `json:"available"`
	URL          string  `json:"url,omitempty"`
}

// Product is full product detail. Fields an adapter can't supply stay empty.
type Product struct {
	Hit
	EAN          string `json:"ean,omitempty"`
	Origin       string `json:"origin,omitempty"`
	Ingredients  string `json:"ingredients,omitempty"`
	Nutrients    string `json:"nutrients,omitempty"`
	Conservation string `json:"conservation,omitempty"`
	OnSale       bool   `json:"onSale,omitempty"`
}

// Category is a node in the store's category tree.
type Category struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Children []Category `json:"children,omitempty"`
}

// Store is one supermarket backend. limit 0 means the backend default; eco keeps
// only ecological products where the store supports that facet.
type Store interface {
	Key() string
	Search(term string, limit int, eco bool) ([]Hit, error)
	CategoryProducts(categoryID string, limit int, eco bool) ([]Hit, error)
	Product(id string) (*Product, error)
	Categories(depth int) ([]Category, error)
}
