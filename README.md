# UK VAT: Eat-in / Takeaway — Universal Till plugin

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing the UK's VAT
eat-in/takeaway rate switch (HMRC VAT Notice 709/1): food a shop sells
zero-rated when it's taken away becomes standard-rated the moment it's
consumed on the premises. Built per
[`ut-docs` ADR-0025](https://github.com/universaltill/ut-docs/blob/main/adr/0025-country-tax-and-fiscal-compliance.md)
and its follow-up refactor
(`universal-till`'s `docs/code-reviews/2026-07-28-tax-rate-plugin-hook-refactor.md`),
which moved every country's VAT-switching rule out of core and into plugins
answering a generic `tax.rate.ask` hook. This is the second such plugin
(after `ut-plugin-tax-de`) and deliberately much simpler: **no TSE, no
DSFinV-K, no external API of any kind** — the UK has no cash-register
signing mandate, so VAT-rate switching is pure local business logic. No
`net:` permission, no outbound calls, nothing to verify against a sandbox.

## What this does, precisely

Real UK law (VAT Notice 709/1), not invented:
- Most **cold food** a shop sells is **zero-rated** when the customer takes
  it away, but the same item becomes **standard-rated** if eaten on the
  premises. Classic example: a sandwich.
- **Hot food/drink** is a separate rule — near-universally standard-rated
  regardless of eat-in or takeaway. This plugin does **not** switch hot
  items; a merchant just assigns them a standard-rate tax code directly in
  the catalog, no plugin involvement needed (same as any item this plugin
  has no opinion on — it silently declines and the till uses the item's own
  rate).

This plugin only handles the *switch*. It answers `tax.rate.ask`:
- `order_type == "takeaway"` → declines (no override). The item's own tax
  code — typically zero-rated — already applies.
- `order_type == ""` (eat-in/dine-in) → looks up the item's `tax_code_id` in
  the `eatin_standard_rate_by_tax_code` setting; if present, answers with
  that standard rate. If not present (the tax code isn't in the map — e.g.
  it's a hot item or something else entirely), declines, same as takeaway.

## Configure (plugin settings)

- `eatin_standard_rate_by_tax_code` — a JSON object, `tax_code_id` →
  standard-rate basis points, e.g. `{"tax_zero": 2000}` maps a shop's
  zero-rated tax code to 20% for eat-in. Edited as raw JSON — there's no
  dedicated form for this yet (a merchant needs to know their own tax
  code's ID). Same pre-existing gap as `ut-plugin-tax-de`'s
  `takeaway_rate_overrides` setting — a real catalog-integrated settings UI
  is a follow-up, not built here.

## In-till documentation (Docs button)

The manifest registers a `type: "page"` entry with the reserved key `docs`
(ADR-0037), shipping `content/en-GB.json` — so once installed, the till's
Plugins manager shows a Docs button on this plugin's card that opens this
same eat-in/takeaway explanation and the `eatin_standard_rate_by_tax_code`
walkthrough directly inside the POS, aimed at the shop owner.

## Verified

Built (`scripts/build.sh`) and run through a real wazero runtime (the same
engine `universal-till` uses) with a minimal host-function stub, covering
all three cases: takeaway declines, eat-in with a configured override
answers the standard rate, eat-in with no override configured declines.
`scripts/validate.sh` passes. Not yet installed via a real marketplace
flow — first installed directly (DB + filesystem, bypassing marketplace
signing) for local dev testing, matching how this codebase's own tests
install plugins.

## What a human still needs to do before this could ever go live

1. Build a settings UI so a merchant can actually pick which of their own
   tax codes get the eat-in override, instead of hand-editing JSON.
2. Legal review — this plugin encodes a simplified reading of VAT Notice
   709/1 (the cold-food-only case); real UK VAT has more edge cases
   (mixed hot/cold combos, catering, delivery) not modeled here.
3. Publish through the real marketplace pipeline (`scripts/publish.sh` +
   `scripts/approve.sh`) once there's a marketplace listing for it — so far
   this has only been installed locally for testing.

## Build

```sh
bash scripts/build.sh   # -> bin/plugin.wasm (GOOS=wasip1 GOARCH=wasm)
```

`go build ./...` from the repo root matches every sibling plugin repo's
behavior: it prints `go: warning: "./..." matched no packages` and exits 0,
because `src/main.go` is gated `//go:build wasip1` — the real build check is
`scripts/build.sh`.
