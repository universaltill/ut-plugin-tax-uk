# ut-plugin-tax-uk — notes

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing the UK's VAT
eat-in/takeaway rate switch (HMRC VAT Notice 709/1). `canonical_type: tax`,
hooks `tax.rate.ask` and `charge.policy.ask` (plus `export.requested.ask` for the accountant CSV) (the UK's
service-charge/tip defaults, ADR-0061, ut-docs#975). Second plugin (after `ut-plugin-tax-de`)
answering `universal-till`'s generic value-returning tax hook — see
`universal-till`'s `docs/code-reviews/2026-07-28-tax-rate-plugin-hook-
refactor.md` for why that hook exists and what it replaced (core used to
hardcode Germany's version of this same problem).

## Auto-install eligibility: no network, allow-listed permissions only

UK VAT-rate switching needs no TSE-equivalent signing and no third-party
API: it's pure local logic (given an item's own tax code and the sale's
order type, answer with an override rate or decline).

This plugin is auto-installed on a new UK till (ADR-0172, ut-docs#4837), so
its manifest must pass ADR-0172 §2.4: `markets: ["GB"]` (explicit),
`min_pos_version` at least `0.30.11` (first till release with manifest
`markets`, ut-docs#3176), id under `com.universaltill.`, no `fiscal.*` hook or
`provides`, and permissions only `events:receive`, `sales:read`, `storage`.
**Never add a `net:` host or any other permission here**: `scripts/validate.sh`
and `src/wasmrun/eligibility_test.go` refuse it. The MTD VAT return bridge
(HMRC sandbox, `net:test-api.service.hmrc.gov.uk`) lives in
`ut-plugin-export-mtd-uk` (ut-docs#6289); do not move it back. Do not seed
`eatin_standard_rate_by_tax_code` or any default mapping: the UK eat-in VAT
question is open (ut-docs#6290, ADR-0172 §7).

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
- `src/vatfigures/` — host-fn-free accountant export (ut-docs#5052): the
  `EODCloseExport` wire shape and `BuildFigures` (Box 1/6 from `eod_closes`,
  empty-cross-tab refusal). Mirrors `ut-plugin-tax-de`'s `datev` split.
- `src/wasmrun/` — test-only. Compiles the current source to wasm and runs
  it under a real wazero runtime with stubbed host functions, pinning the
  exact stdout for `charge.policy.ask`, `tax.rate.ask` and the
  `vat-figures-uk` export, the manifest hooks/entries, and the auto-install
  eligibility rules. The only thing that exercises `main.go`'s dispatch
  (same shape as `ut-plugin-tax-de`'s `src/wasmrun/`). `wazero` in `go.mod`
  exists for this alone.
- Host functions imported from module `ut`: `log_write`, `settings_get`
  only. `src/wasmrun` still stubs `http_request`/`storage_*`, harmlessly; a
  test pins that the module does not import `http_request`.
- The actual switching rule (which tax codes get the eat-in override, and
  what rate) is merchant-configured via the `eatin_standard_rate_by_tax_code`
  setting, not hardcoded — a shop's own tax-code IDs aren't knowable in
  advance.

## Verification

- `GOOS=wasip1 GOARCH=wasm go vet ./...`, `gofmt -l .` and `go test ./...`
  (all run in CI, in that order, before the build). A host `go vet` skips
  `src/main.go` entirely (`//go:build wasip1`), hence the cross-compiled
  form. `go test` covers `src/chargepolicy` and `src/vatfigures` on the host and `src/wasmrun`,
  which builds and runs the real wasm module.
- `bash scripts/build.sh` (cross-compiles `GOOS=wasip1 GOARCH=wasm`).
- `bash scripts/validate.sh` (manifest shape: `canonical_type: tax`,
  `countries`/`markets: ["GB"]`, `min_pos_version` >= 0.30.11, one `tax`-type
  entry, the `vat-figures-uk` export entry, the ADR-0172 permission allow-list
  (no `net:`), no MTD entry, and that
  no map/list-typed setting's `default_value` is a JSON-string-wrapped
  object/list instead of a real one — ut-docs#1255/#1270).
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
