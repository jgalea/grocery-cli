---
name: grocery-shop
description: Fill a supermarket cart from a natural-language list or a photo, using the grocery CLI. Use when the user says something like "add these to my Mercadona basket", "order the usual from Mercadona", "put milk, eggs and coffee in my cart", or shares a photo of a shopping list / fridge / recipe to shop from. Only works for stores with an account/cart (e.g. mercadona); for others, price or compare instead.
---

# grocery-shop

Turn "here's what I want from <store>" into items in the user's real cart, using the `grocery` CLI. You resolve each item to a real product, show a priced plan, get approval, then fill the cart. You never place the order — the user reviews and pays in the browser.

## Preconditions

- `grocery` is on PATH. Check `grocery stores` — the target store must list `cart` in its capabilities (today: `mercadona`). If it doesn't, tell the user this store is read-only and offer to price/compare instead.
- The user must be logged in: `grocery --store <store> cart` should not error with "not logged in". If it does, tell them to run `grocery --store <store> login` themselves (they enter their own email + password; you never handle credentials). Do not attempt to log in for them.

## Usuals shortcut

If the user asks for "the usual" / "my regular shop" / "order what I normally get", check purchase history first:

```bash
grocery usuals --store <store> --json
```

If there are matches, show the list and ask for approval, then either:

```bash
grocery usuals order --store <store> --dry-run   # show plan
grocery usuals order --store <store>             # fill cart after yes
```

History builds automatically from successful `cart add` calls. `--min` (default 2) controls how many past buys count as a usual.

## Flow

1. **Parse the request into an item list.** From text, or by reading a photo of a list/fridge/receipt into concrete items. Show the user the parsed list and let them correct it before you touch anything.
2. **Resolve each item to a product.** For each line, run `grocery --store <store> search "<item>" --json --limit 5` and pick the best match (prefer the cheapest sensible hit; respect any brand/size the user specified). Collect the product `id`, name and price.
3. **Show the plan with a code-computed total.** Present a table: item → chosen product → price × qty → line total, and the basket total. This is your estimate from search prices.
4. **Get explicit approval.** Wait for a clear yes before writing anything to the cart.
5. **Fill the cart.** For each approved line: `grocery --store <store> cart add <id> <qty> --max <cap>`. Always pass `--max` with a per-line cap so a mis-parse can't add something absurd. If a line is refused by `--max`, stop and ask.
6. **Report the real cart.** Run `grocery --store <store> cart` and show the actual cart total. Tell the user to open the store's site/app to review and pay — the CLI does not place the order.

## Rules

- Never place an order or check out. Filling the cart is the end of your job.
- Never enter or handle the user's password. Login is theirs to run.
- Always confirm the parsed list before searching, and the priced plan before adding.
- Use `--json` for machine steps; show humans a clean table.
- If an item has no good match, flag it and skip rather than guessing wildly.
- Keep a running total as you go so the user isn't surprised.
