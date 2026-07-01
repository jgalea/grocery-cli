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

It's not a scraper for "any supermarket on earth" — it's an explicit registry of stores that each have a verified adapter. Two backends cover a lot of European retail today:

- **SCAPI** — Salesforce Commerce Cloud headless stores that expose a guest shopper API (anonymous SLAS token, no account).
- **SSR** — classic server-rendered Salesforce Commerce Cloud stores, read from the product grid the site itself renders.

Run `grocery stores` to see what's supported right now.

## Supported stores

| Key | Store | Country | Backend | Supports |
|-----|-------|---------|---------|----------|
| `ametller` | Ametller Origen | ES | SCAPI | search, batch, total, product, categories |
| `continente` | Continente | PT | SSR | search, batch |

Roadmap (each is another adapter, not a rewrite): Mercadona (ES, open API), Bonpreu i Esclat (ES, AWS WAF), Auchan (PT, SSR), Aldi (PT), Pingo Doce via Mercadão (PT).

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
```

## Commands

| Command | Description |
|---------|-------------|
| `grocery stores` | List supported stores, their country, backend and capabilities |
| `grocery search <term…>` | Full-text search. `--limit N`, `--eco` (where supported), `--cheapest` (rank by €/kg·L·u) |
| `grocery batch [-f file]` | Cheapest hit per term (one per line, `#` comments ok; or positional) |
| `grocery total [-f file]` | Deterministic basket total from `<id> [qty]` lines, summed in integer cents |
| `grocery product <id>` | Product detail (price, brand, origin, ingredients, nutrition) |
| `grocery categories [--id N]` | Category tree, or one category's products with `--id` |

Common flags (before or after the command): `--store <key>`, `--lang <code>`, `--json`, `--toon`.

Not every store supports every command yet — an SSR store without a product-detail path returns a clear "not supported for this store yet", and `grocery stores` lists each store's capabilities.

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

Inspired by [bonpreu-cli](https://github.com/seifreed/bonpreu-cli) by Marc Rivero López ([@seifreed](https://github.com/seifreed)) and [mercadona-cli](https://github.com/ivorpad/mercadona-cli).
