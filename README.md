# UK VAT: Eat-in / Takeaway — Universal Till plugin

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing the UK's VAT
eat-in/takeaway rate switch (HMRC VAT Notice 709/1): food a shop sells
zero-rated when it's taken away becomes standard-rated the moment it's
consumed on the premises. Built per
[`ut-docs` ADR-0025](https://github.com/universaltill/ut-docs/blob/main/adr/0025-country-tax-and-fiscal-compliance.md)
and its follow-up refactor
(`universal-till`'s `docs/code-reviews/2026-07-28-tax-rate-plugin-hook-refactor.md`),
which moved every country's VAT-switching rule out of core and into plugins
answering a generic `tax.rate.ask` hook. It also answers
`charge.policy.ask` with the UK's service-charge/tip defaults
([`ut-docs` ADR-0061](https://github.com/universaltill/ut-docs/blob/main/adr/0061-service-charge-tax-and-tip-recipient-country-policy.md)).
This is the second such plugin
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

### Service-charge and tip policy (`charge.policy.ask`)

The plugin also answers core's whole-store `charge.policy.ask` hook (ADR-0061
Decision 1, `ut-docs#975`) with the UK's researched row from `ut-docs#961` —
a constant, offline answer (`src/chargepolicy/`):

- `service_charge_permitted: true` — a discretionary service charge is
  lawful and common in UK hospitality.
- `service_charge_default_rate_bp: 1250` — 12.5%, the UK market norm, as an
  informational *suggested* rate only. Whether a service charge is switched
  on at all, and the rate actually charged, stay the merchant's own till
  settings; core never applies this default.
- `service_charge_tax_basis_bp: 0` — "apportion", not "no tax": a service
  charge is further consideration for the supply (VAT Notice 709/1 §2.3),
  so its VAT is apportioned across the sale's own per-line rates.
- `tip_default_recipient: "employee"` — the Employment (Allocation of Tips)
  Act 2023 requires tips and service charges to reach workers in full.
- `fiscal_business_case: ""` — unused; the UK has no DSFinV-K-style export
  needing a business-case mapping (unlike `ut-plugin-tax-de`).
- No `charges` key: the UK has no additive statutory levy (ADR-0062).

## In-till documentation

This plugin ships a `docs` page entry (`ut-docs` ADR-0037 — plugin docs
ride the existing plugin-page mechanism, `content/index.html`), so
`/plugins` shows a **Docs** button for it once installed. The button opens
the same explanation as this README's "What this does" / "Configuring"
sections, rendered inside the till — works with the network down, no
separate viewer. Keep `content/index.html` in sync with this README
whenever the switching logic or the settings key changes.

## Configure (plugin settings)

- `eatin_standard_rate_by_tax_code` — a JSON object, `tax_code_id` →
  standard-rate basis points, e.g. `{"tax_zero": 2000}` maps a shop's
  zero-rated tax code to 20% for eat-in. Edited as raw JSON — there's no
  dedicated form for this yet (a merchant needs to know their own tax
  code's ID). Same pre-existing gap as `ut-plugin-tax-de`'s
  `takeaway_rate_overrides` setting — a real catalog-integrated settings UI
  is a follow-up, not built here.

## Verified

Built (`scripts/build.sh`) and run through a real wazero runtime (the same
engine `universal-till` uses) with a minimal host-function stub, covering
all three cases: takeaway declines, eat-in with a configured override
answers the standard rate, eat-in with no override configured declines.
Since `ut-docs#975` that run is a **committed test suite**, `src/wasmrun/`
(`go test ./...` runs it in CI): it compiles the current source to wasm,
executes it under wazero with stubbed `log_write`/`settings_get`, and pins
the exact stdout for `charge.policy.ask` (decoded strictly against a
mirror of core's `chargePolicyAskResponse`), the eat-in and takeaway
`tax.rate.ask` cases, an unhandled event answering nothing, and that
`manifest.json` declares the `charge.policy.ask` hook. Proven to bite:
renaming the `main.go` case, changing `GB()`'s rate, or dropping the
manifest hook each fail it. Not proven: `universal-till`'s real
host-function implementations or a real installed-plugin run through the
till UI. `scripts/validate.sh` passes. Not yet installed via a real marketplace
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

```sh
go test ./...           # src/chargepolicy (host) + src/wasmrun (builds and runs the real wasm)
```

`src/main.go` is gated `//go:build wasip1`, so a plain host `go build ./...`
skips it — the build check for the module is `scripts/build.sh`, and the
only test that exercises `main.go`'s dispatch is `src/wasmrun` (which builds
its own wasm from the current source; `wazero` is a test-only dependency,
nothing in `bin/plugin.wasm` imports it). CI runs `GOOS=wasip1 GOARCH=wasm
go vet ./...`, a `gofmt -l .` check, `go test ./...`, then
build/validate/package.
