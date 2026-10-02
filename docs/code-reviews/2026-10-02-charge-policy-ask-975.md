# Code review — answer `charge.policy.ask` with the UK's service-charge/tip policy

**Date:** 2026-10-02
**Card:** universaltill/ut-docs#975
**Branch:** `feat/975-charge-policy-ask`
**Complexity:** medium
**Dev:** Opus 5.5, subagent (cloud routine lane `:41b`)
**Reviewer:** Fable, one independent subagent with a fresh context (worktree-isolated; ran the full gate itself)

## What shipped

- `src/chargepolicy/` (new, host-testable): `GB()` returns ut-docs#961's UK
  row: `service_charge_permitted: true`, `service_charge_default_rate_bp: 1250`
  (12.5%, the UK market norm — an informational suggested rate only; the
  merchant's own configured rate and whether the charge is even on stay the
  till's own setting), `service_charge_tax_basis_bp: 0` (apportion at the
  sale's own per-line VAT Notice 709/1 §2.3 rates), `tip_default_recipient:
  "employee"` (Employment (Allocation of Tips) Act 2023), `fiscal_business_case:
  ""` (the UK has no DSFinV-K-style export needing one). No `omitempty`, no
  `charges`.
- `src/main.go`: dispatches `charge.policy.ask` to that constant via
  `json.Marshal` + `os.Stdout.Write` (this repo's existing pattern — no
  `mustJSON` helper here, unlike `ut-plugin-tax-de`).
- `manifest.json`: new `hooks[]` entry, version `1.0.0` → `1.1.0` (confirmed
  this repo's `auto-tag-release.yml` will tag and release it — `origin` has
  no `v1.1.0` tag yet).
- `content/index.html` + `README.md` + `CLAUDE.md`: the new hook documented
  for merchants (in-till docs page) and for future contributors.
- `.github/workflows/ci.yml`: this repo's CI previously ran no `go test` at
  all. Added `gofmt check`, a `GOOS=wasip1 GOARCH=wasm go vet ./...` step,
  and `go test ./...`, all before the build step.

## Design decisions

- **Package/constructor naming:** mirrors `ut-plugin-tax-de`'s `chargepolicy`
  package shape exactly, with `GB()` in place of `DE()` — same `Answer`
  struct, same field order/tags, same "no omitempty, no Charges field"
  invariants.
- **`tip_default_recipient` as a literal `"employee"`**, not an imported
  constant: this plugin has no `fiscalsign` package (no TSE), unlike DE.
- **`fiscal_business_case: ""`**: confirmed against core
  (`internal/pages/charge_hook.go` / `internal/pos/charge_policy.go`) that
  core copies this field verbatim with no consumer depending on it being
  non-empty — harmless.
- **No behaviour change to sales with no plugin installed**: core's
  `BuildCharges`/`validateChargePolicy` give this answer the same shape as
  the no-plugin path for every field except the ones ut-docs#961 researched
  differently for the UK (12.5% suggested rate, apportioned tax basis,
  employee tip default — all already core's existing fail-closed defaults
  per ADR-0061 Decision 2, so installing this plugin changes nothing a till
  wasn't already doing correctly; its value is that core now holds this as
  a declared UK **answer** rather than a fallback).

## Verification beyond unit tests

- **Compiled-module (wazero) tests added** (`src/wasmrun/`, new): this
  plugin's `main.go` is `//go:build wasip1`-only, so neither `go test ./...`
  nor a host `go vet` ever exercised the dispatch — nothing proved the new
  `case "charge.policy.ask"` was wired, or that `manifest.json` declared the
  hook, before this review. Added the same harness shape as
  `ut-plugin-tax-de`'s `src/wasmrun/` (ut-docs#818/#974): a real wazero
  runtime, stub `ut.log_write`/`ut.settings_get` host functions, builds and
  runs the actual `./src` source (not a committed artefact, so the test
  can't pass against stale wasm). Five tests: the UK policy answer (exact
  stdout + strict `DisallowUnknownFields` decode against a mirror of core's
  `chargePolicyAskResponse`), the manifest declares the hook, eat-in/takeaway
  `tax.rate.ask` (previously only verified by an uncommitted ad-hoc run per
  the original README), and an unknown event answers nothing.
- **Mutation-tested**, restored byte-identical afterwards:
  - `src/chargepolicy`: wrong rate, flipped `permitted`, and an added
    `omitempty` on `ServiceChargePermitted` each fail the relevant test with
    a clear message.
  - `src/wasmrun`: renaming the `main.go` case fails
    `TestChargePolicyAsk_AnswersUKPolicy` (empty stdout, logged as an
    unhandled event); removing the manifest hook fails
    `TestManifestSubscribesChargePolicyAsk`.
- **Wire contract vs core**, read directly (not assumed): every JSON tag in
  `chargepolicy.Answer` matches `internal/pages/charge_hook.go`'s
  `chargePolicyAskResponse` field-for-field; `service_charge_permitted` is a
  `*bool` on core's side (absent reads as permitted) and this plugin's
  non-pointer `bool` with no `omitempty` can never vanish from the wire;
  `tip_default_recipient: "employee"` matches `pos.TipRecipientEmployee`
  exactly, so `validateChargePolicy`'s recipient switch accepts it rather
  than silently treating it as no-opinion.
- **Packaging:** `scripts/package.sh`'s tarball contains only
  `manifest.json`, `README.md`, `bin/plugin.wasm`, `LICENSE`,
  `content/index.html` — no test files, no `go.sum`/`go.mod` leak.
- **`go mod tidy -diff`** (with `GOTOOLCHAIN=local`, to avoid a CI-breaking
  toolchain pin): clean.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | `main.go`'s new dispatch case and the manifest hook had no test that could ever fail if either were deleted (`go test`/host `go vet` can't see `//go:build wasip1` code) — this repo repeated the ad-hoc-uncommitted-verification pattern `ut-plugin-tax-de` had already moved past for the identical sibling card | **Fixed:** added `src/wasmrun/wasmrun_test.go` (5 tests, see above) |
| 2 | minor | CI gained `gofmt`/`go test` but no `GOOS=wasip1 GOARCH=wasm go vet ./...` — a vet-level bug in `main.go` would reach `main` unnoticed | **Fixed:** added the vet step, before gofmt/test |
| 3 | minor | `content/index.html`'s new section read as if a settings UI already shows the 12.5% suggestion | **Fixed:** reworded to say plainly the till doesn't apply it for you |
| 4 | minor | `go mod tidy` (local Go 1.24.7) had added a `toolchain go1.24.7` line, which would make CI (pinned to 1.23) auto-download a toolchain on every run | **Fixed:** removed; `go 1.22.0` only |
| 5 | nit | README/CLAUDE.md still described verification as uncommitted/ad-hoc after the wasmrun harness landed | **Fixed** |
| 6 | nit (accepted) | `json.Marshal(chargepolicy.GB())` discards its error | Accepted: a struct of bool/int/string literals cannot fail to marshal, same as the DE sibling's `mustJSON` |
| 7 | process | Review record missing | This file |

## Not verified

- No settings UI anywhere in core surfaces `service_charge_default_rate_bp`
  (the 12.5% suggestion) yet — same gap the DE sibling's review recorded.
  The hook answer is correct regardless; a UI consuming it is a separate,
  uncarded feature.
- No real installed-plugin run through a till UI.

## Separate-card candidates (not blocking this one)

- This repo has no `version-bump` CI guard (`ut-plugin-tax-de` has
  `scripts/check-version-bump.sh`) — a future shipped-file change without a
  manifest bump would still merge here silently. Cross-plugin consistency
  gap, out of #975's scope.

## Verdict

Safe to merge. Gate: wasip1 vet, gofmt, `go test ./...` (host +
wazero-compiled-module tests), build, validate, package — all green.
