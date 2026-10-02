# ut-plugin-tax-uk — notes

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing the UK's VAT
eat-in/takeaway rate switch (HMRC VAT Notice 709/1). `canonical_type: tax`,
two hooks: `tax.rate.ask` and `charge.policy.ask` (the UK's
service-charge/tip defaults, ADR-0061, ut-docs#975). Second plugin (after `ut-plugin-tax-de`)
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
  `type` field. Two cases: `tax.rate.ask` → `handleTaxRateAsk`, and
  `charge.policy.ask` → prints `chargepolicy.GB()` as JSON (whole-store
  ask, empty payload).
- `src/chargepolicy/` — the pure, host-testable `charge.policy.ask` answer
  (`Answer` wire struct + `GB()` with the UK's researched row from
  ut-docs#961). Mirrors `ut-plugin-tax-de`'s package of the same name. No
  `omitempty` on `Answer` — core reads an absent `service_charge_permitted`
  as permitted — and no `charges` field (the UK has no additive levy).
- `src/wasmrun/` — test-only. Compiles the current source to wasm and runs
  it under a real wazero runtime with stubbed `log_write`/`settings_get`,
  pinning the exact stdout for `charge.policy.ask` and `tax.rate.ask` and
  that `manifest.json` declares the hook. The only thing that exercises
  `main.go`'s dispatch at all (same shape as `ut-plugin-tax-de`'s
  `src/wasmrun/`). `wazero` in `go.mod` exists for this alone.
- Host functions imported from module `ut`: only `log_write` and
  `settings_get` (no `storage_get`/`storage_set`/`http_request` — this
  plugin is stateless, same reasoning as skipping the `net:` permission).
  If you add one, add its stub to `src/wasmrun` too or the module won't
  instantiate there.
- The actual switching rule (which tax codes get the eat-in override, and
  what rate) is merchant-configured via the `eatin_standard_rate_by_tax_code`
  setting, not hardcoded — a shop's own tax-code IDs aren't knowable in
  advance.

## Verification

- `GOOS=wasip1 GOARCH=wasm go vet ./...`, `gofmt -l .` and `go test ./...`
  (all run in CI, in that order, before the build). A host `go vet` skips
  `src/main.go` entirely (`//go:build wasip1`), hence the cross-compiled
  form. `go test` covers `src/chargepolicy` on the host and `src/wasmrun`,
  which builds and runs the real wasm module.
- `bash scripts/build.sh` (cross-compiles `GOOS=wasip1 GOARCH=wasm`).
- `bash scripts/validate.sh` (manifest shape: `canonical_type: tax`,
  `countries: ["GB"]`, one `tax`-type entry, no `net:` permission).
- Any change to `main.go`'s dispatch or to an answer's wire shape gets a
  `src/wasmrun` case — the README's "Verified" section lists what is
  pinned; keep it current if the logic changes.

## Before committing

- Keep README.md's status honest — this repo exists to demonstrate the
  plugin-hook architecture correctly, not to overclaim legal compliance.
- Standards & decisions live in the docs repo (`adr/`, ADR-0007
  document-first). Behaviour changes update the README in the same session.
