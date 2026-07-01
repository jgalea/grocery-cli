<h1 align="center">grocery-cli</h1>

<p align="center">
  <a href="go.mod"><img src="https://img.shields.io/badge/GO-1.26%2B-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/LICENSE-MIT-5C9E31?style=for-the-badge" alt="License"></a>
  <img src="https://img.shields.io/badge/SINGLE%20BINARY-no%20runtime%20deps-brightgreen?style=for-the-badge" alt="Single binary">
</p>

<p align="center"><strong>Unofficial, agent-friendly CLI for online supermarkets, behind one interface. Pick a store, search the catalog, read prices, price a shopping list.</strong></p>

---

## Overview

`grocery` is a single Go binary that talks to several online supermarkets through one set of commands. You choose a store with `--store` (or `GROCERY_STORE`), and every store is an adapter behind a common interface, so the commands never change. Every command supports `--json` (data to stdout, logs to stderr) and `--toon` (fewer tokens, for LLMs).

It's not a scraper for "any supermarket on earth" — it's an explicit registry of stores that each have a verified adapter, spanning several backend types (guest APIs, server-rendered catalogs, Algolia search, WooCommerce, and a few bespoke REST/HTML sources). Run `grocery stores` to see what's supported right now.

## Why use it

For a one-off shop, the store's own app is easier. `grocery` earns its place when you want to do things an app can't:

- **Compare prices across stores.** One query, same basket, across every chain in your area — "who's cheapest for my weekly list, Mercadona or Consum or DIA?" No single app shows you that; each only knows its own prices.
- **Give an agent a clean primitive.** The `--json` / `--toon` output is there so an LLM can drive the shop: hand it a shopping list (or a photo of your fridge), have it price the items across your stores and, for stores with an account, fill your cart (see [Shopping cart](#shopping-cart)).
- **Track prices over time.** Run it on a schedule, log what your regular items cost, and watch how prices move.

Reads need no account and work for every store. Several stores also support filling your own cart (Mercadona, Bonpreu, Continente, Scotts, PAVI/PAMA); the CLI never places the order. Matching "the same product" across chains is fuzzy — `batch` picks the cheapest hit per term, which works for generic items ("leche", "café") but isn't exact-SKU matching.

## Supported stores

Run `grocery stores` for the live list. Currently 21 stores across 5 countries:

| Key | Store | Country | Backend | Supports |
|-----|-------|---------|---------|----------|
| `mercadona` | Mercadona | ES | Algolia + REST | search, batch, total, product, categories, **cart** |
| `bonpreu` | Bonpreu i Esclat | ES | uTLS | search, batch, total, product, categories, **cart** |
| `ametller` | Ametller Origen | ES | SCAPI | search, batch, total, product, categories |
| `consum` | Consum | ES | REST | search, batch, total, product |
| `dia` | DIA | ES | REST | search, batch |
| `eroski` | Eroski | ES | HTML | search, batch |
| `alcampo` | Alcampo | ES | SSR | search, batch |
| `lidl-es` | Lidl España | ES | REST | search, batch |
| `continente` | Continente | PT | SSR | search, batch, **cart** |
| `pingodoce` | Pingo Doce | PT | SSR | search, batch |
| `auchan` | Auchan | PT | SSR | search, batch |
| `lidl-pt` | Lidl Portugal | PT | REST | search, batch |
| `morrisons` | Morrisons | UK | REST | search, batch |
| `iceland` | Iceland | UK | Algolia | search, batch, total, product |
| `edeka24` | Edeka24 | DE | HTML | search, batch |
| `scotts` | Scotts | MT | WooCommerce | search, batch, total, product, categories, **cart** |
| `pavipama` | PAVI/PAMA | MT | REST | search, batch, categories, **cart** |
| `welbees` | Welbee's | MT | HTML | search, batch |
| `convenienceshop` | The Convenience Shop | MT | REST | search, batch |
| `greens` | Greens | MT | REST | categories |
| `smart` | Smart Supermarket | MT | HTML | categories |

Not included: some chains sit behind bot-management (Cloudflare/Akamai/DataDome) that a plain HTTP client can't clear — Carrefour, El Corte Inglés (ES/PT), Condis, Intermarché, Tesco, Sainsbury's, ASDA, Waitrose, Ocado, REWE, Kaufland, Netto. Their catalogs would need a headed stealth browser, which is out of scope for this CLI. Aldi and Lidl in some markets have no shoppable priced online catalog (weekly-flyer only).

## Install

```bash
go install github.com/jgalea/grocery-cli/cmd/grocery@latest
```

Or from source:

```bash
git clone https://github.com/jgalea/grocery-cli.git
cd grocery-cli
go build -o grocery ./cmd/grocery
./grocery version
```

## Quick start

```bash
# List stores and what each supports
grocery stores

# Ametller Origen (ES) — the default store
grocery search llet --cheapest --limit 5
grocery product 1251

# Continente (PT) — pick it with --store
grocery --store continente search leite --limit 5
printf 'leite\npão\novos\n' | grocery --store continente batch -f -

# Set a default store for the session
export GROCERY_STORE=continente
grocery search arroz --limit 3

# Compare a shopping list across stores and rank by basket total
printf 'leche\naceite\nhuevos\ncafe\npan\n' | grocery compare -f - --stores mercadona,consum,dia,alcampo
printf 'leite\novos\ncafe\n' | grocery compare -f - --country PT --detail
```

## Commands

| Command | Description |
|---------|-------------|
| `grocery stores` | List supported stores, their country, backend and capabilities |
| `grocery search <term…>` | Full-text search. `--limit N`, `--eco` (where supported), `--cheapest` (rank by €/kg·L·u) |
| `grocery batch [-f file]` | Cheapest hit per term (one per line, `#` comments ok; or positional) |
| `grocery compare [-f file]` | Price one shopping list across several stores and rank them (`--stores a,b,c` or `--country ES`, `--detail`) |
| `grocery total [-f file]` | Deterministic basket total from `<id> [qty]` lines, summed in integer cents |
| `grocery product <id>` | Product detail (price, brand, origin, ingredients, nutrition) |
| `grocery categories [--id N]` | Category tree, or one category's products with `--id` |

Common flags (before or after the command): `--store <key>`, `--lang <code>`, `--json`, `--toon`.

Not every store supports every command yet — an SSR store without a product-detail path returns a clear "not supported for this store yet", and `grocery stores` lists each store's capabilities.

## Shopping cart

`grocery` can fill your own cart on stores that support it (`grocery stores` lists `cart`). It adds items and stops there — it never places the order; you review and pay in the browser.

Cart stores today, by login style:
- **Mercadona** (ES) — email + password: `grocery --store mercadona login` (password read hidden, never stored; only the token is cached).
- **Bonpreu** (ES) — paste your browser Cookie header (SSO/cookie login): `grocery --store bonpreu login`, then paste it from DevTools → Network → any request → Cookie.
- **PAVI/PAMA** (MT) — paste your login token from a logged-in session: `grocery --store pavipama login`.
- **Continente** (PT) and **Scotts** (MT) — work as a guest cart with no login at all (verified live); paste a cookie only if you want items in your account cart.

```bash
grocery --store mercadona cart add 4240 2    # add 2× a product by id
grocery --store mercadona cart               # show the cart
grocery --store mercadona cart set 4240 0    # remove it
grocery --store mercadona cart clear         # empty the cart
```

`cart add`/`set` take a `--max <eur>` cap that refuses an over-budget line before writing.

The point of this is the agent flow: with the bundled **`grocery-shop`** Claude skill, you can say "add milk, eggs and coffee to my Mercadona basket" (or share a photo of a list) and the agent resolves each item, shows a priced plan, and fills the cart after you approve. Install it by copying `.claude/skills/grocery-shop` into your Claude skills directory. Only stores that list `cart` in `grocery stores` support this.

## Adding a store

Add an entry to `internal/registry`, pointing at either the SCAPI adapter (with the store's short code, org, guest client id and site id) or the SSR adapter (base URL, site id, locale). A store on a different platform (a custom API, a different commerce backend) gets a small new adapter implementing the `store.Store` interface.

## Configuration

| Env | Purpose |
|-----|---------|
| `GROCERY_STORE` | Default store key |
| `GROCERY_CONFIG_DIR` | Override `~/.grocery` (per-store session cache) |
| `GROCERY_SCAPI_BASE` | Override the SCAPI host (debugging) |

## License

MIT. See [LICENSE](LICENSE).

Author: Jean Galea ([@jgalea](https://github.com/jgalea)). Repository: [github.com/jgalea/grocery-cli](https://github.com/jgalea/grocery-cli).

Inspired by [bonpreu-cli](https://github.com/seifreed/bonpreu-cli) and [mercadona-cli](https://github.com/ivorpad/mercadona-cli).
