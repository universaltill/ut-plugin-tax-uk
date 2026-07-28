# ut-plugin-tax-uk — notes

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing the UK's VAT
eat-in/takeaway rate switch (HMRC VAT Notice 709/1). `canonical_type: tax`,
single hook: `tax.rate.ask`. Second plugin (after `ut-plugin-tax-de`)
answering `universal-till`'s generic value-returning tax hook — see
`universal-till`'s `docs/code-reviews/2026-07-28-tax-rate-plugin-hook-
refactor.md` for why that hook exists and what it replaced (core used to
hardcode Germany's version of this same problem).

## Much simpler than ut-plugin-tax-de — no external API at all

UK VAT-rate switching needs no TSE-equivalent signing, no DSFinV-K-style
export, no third-party API. It's pure local logic: given an item's own tax
code and the sale's order type, answer with an override rate or decline.
No `net:` permission requested, no HTTP calls anywhere in `src/main.go`.

## Code layout

- `src/main.go` — single WASI command, dispatches on the event JSON's
  `type` field. Only one real case: `tax.rate.ask` → `handleTaxRateAsk`.
- Host functions imported from module `ut`: only `log_write` and
  `settings_get` (no `storage_get`/`storage_set`/`http_request` — this
  plugin is stateless, same reasoning as skipping the `net:` permission).
- The actual switching rule (which tax codes get the eat-in override, and
  what rate) is merchant-configured via the `eatin_standard_rate_by_tax_code`
  setting, not hardcoded — a shop's own tax-code IDs aren't knowable in
  advance.

## Verification

- `bash scripts/build.sh` (cross-compiles `GOOS=wasip1 GOARCH=wasm`; plain
  `go build ./...` matches no packages by design, same as every sibling
  plugin, because `src/main.go` is gated `//go:build wasip1`).
- `bash scripts/validate.sh` (manifest shape: `canonical_type: tax`,
  `countries: ["GB"]`, one `tax`-type entry, no `net:` permission).
- Run against a real wazero runtime before trusting any change to
  `handleTaxRateAsk` — the README's "Verified" section describes exactly
  what was tested; keep it current if the logic changes.

## Before committing

- Keep README.md's status honest — this repo exists to demonstrate the
  plugin-hook architecture correctly, not to overclaim legal compliance.
- Standards & decisions live in the docs repo (`adr/`, ADR-0007
  document-first). Behaviour changes update the README in the same session.
