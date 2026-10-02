# ut-plugin-tax-uk — notes

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing the UK's VAT
eat-in/takeaway rate switch (HMRC VAT Notice 709/1). `canonical_type: tax`,
two hooks: `tax.rate.ask` and `charge.policy.ask` (the UK's
service-charge/tip defaults, ADR-0061, ut-docs#975). Second plugin (after `ut-plugin-tax-de`)
answering `universal-till`'s generic value-returning tax hook — see
`universal-till`'s `docs/code-reviews/2026-07-28-tax-rate-plugin-hook-
refactor.md` for why that hook exists and what it replaced (core used to
hardcode Germany's version of this same problem).

## Much simpler than ut-plugin-tax-de — one sandbox-only network feature

UK VAT-rate switching needs no TSE-equivalent signing and no third-party
API: it's pure local logic (given an item's own tax code and the sale's
order type, answer with an override rate or decline).

The ONE network feature is the `mtd-vat-return-uk` export entry
(ut-docs#1475): a Making Tax Digital VAT return bridge to HMRC's **sandbox**
API, on the manual Data/Export path only (never checkout). Its only `net:`
permission is `net:test-api.service.hmrc.gov.uk`; `scripts/validate.sh`
refuses any other host, and `hmrcvat.Settings.Validate` refuses HMRC
production. Never fabricate: Boxes 2/4/7/8/9 come only from merchant
settings, and the `Gov-Client-*` Fraud Prevention Headers are omitted (no
host function exposes device metadata), never invented.

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
- `src/hmrcvat/` — the host-fn-free MTD half (ut-docs#1475): HMRC request/
  response shapes, `Settings.Validate` (refuse by setting name),
  `BuildSubmitRequest` (Box 1/6 from `eod_closes`, 2/4/7/8/9 verbatim,
  3/5 derived), pence→pounds formatting, refresh-token rotation
  bookkeeping, exact-period obligation matching. Mirrors
  `ut-plugin-tax-de`'s `fiskalyparse`/`datev` split. `VendorVersion` must
  equal `manifest.json`'s version (a test pins it) — bump both together.
  `time.Now()` in the guest is wazero's FAKE clock under core (no
  `WithSysWalltime`), so `CachedToken.ExpiresAt` is advisory only; the
  401-then-refresh-once path in `main.go` is the real expiry check — keep
  it (and its `src/wasmrun` case) if the token flow is ever reworked.
- `src/wasmrun/` — test-only. Compiles the current source to wasm and runs
  it under a real wazero runtime with stubbed `log_write`/`settings_get`,
  pinning the exact stdout for `charge.policy.ask` and `tax.rate.ask` and
  that `manifest.json` declares the hook; `mtd_test.go` pins the MTD
  export's HTTP call sequence/headers/bodies against a scripted HTTP stub
  (never a real network call). The only thing that exercises
  `main.go`'s dispatch at all (same shape as `ut-plugin-tax-de`'s
  `src/wasmrun/`). `wazero` in `go.mod` exists for this alone.
- Host functions imported from module `ut`: `log_write`, `settings_get`,
  and (since ut-docs#1475, for the MTD export) `http_request`,
  `storage_get`, `storage_set` — same ABI and helpers as
  `ut-plugin-tax-de`. Every import has a stub in `src/wasmrun`; add one
  there for any new import or the module won't instantiate.
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
  `countries: ["GB"]`, one `tax`-type entry, the MTD export entry, and
  `net:test-api.service.hmrc.gov.uk` as the only `net:` permission).
- `bash scripts/guard-plugin-i18n.sh` (locale drift; key-shaped manifest
  labels resolve in `locales/en.json`). `scripts/package.sh` must keep
  shipping `locales/` (ut-docs#1883).
- Any change to `main.go`'s dispatch or to an answer's wire shape gets a
  `src/wasmrun` case — the README's "Verified" section lists what is
  pinned; keep it current if the logic changes.

## Before committing

- Keep README.md's status honest — this repo exists to demonstrate the
  plugin-hook architecture correctly, not to overclaim legal compliance.
- Standards & decisions live in the docs repo (`adr/`, ADR-0007
  document-first). Behaviour changes update the README in the same session.
