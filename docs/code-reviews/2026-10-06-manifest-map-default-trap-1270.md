# Review — normalize string-typed JSON-object manifest default (ut-docs#1270)

Date: 2026-10-06 · Lane: lane:cloud-41

## Change

The double-encoding trap ut-docs#1255 fixed for the sibling repo
`ut-plugin-tax-de`'s `takeaway_rate_overrides` (a manifest `default_value`
declared as the JSON *string* `"{}"` instead of a real JSON *object* `{}`,
which core's install-time `json.Marshal(s.DefaultValue)` then
double-encodes) had an armed-but-dormant mirror here:
`eatin_standard_rate_by_tax_code` — the UK equivalent of the feature #1255
fixed for Germany. Dormant because the import handler is still hardcoded
to tax-de's plugin id, so no current caller exercises it; it would hit the
identical bug the moment UK import wiring lands, unless fixed first.

- Fixed `eatin_standard_rate_by_tax_code`'s `default_value` to a real JSON
  object (`{}`).
- Added a general check to `scripts/validate.sh`, byte-for-byte the same
  logic added to `ut-plugin-tax-de`'s `validate.sh`: fails on any
  `settings[]` entry whose `default_value` is a JSON string that itself
  parses to a `dict`/`list`.
- Bumped `manifest.json`'s version 1.2.0→1.2.1. This repo has no
  `check-version-bump.sh` CI enforcement (out of scope to add here, tracked
  separately), but the bump is still needed so `auto-tag-release.yml`
  actually re-publishes the fix to the marketplace rather than silently
  never shipping it.
- Bumped `src/hmrcvat/client.go`'s `VendorVersion` and
  `src/wasmrun/mtd_test.go`'s `wantVendorVersion` to match — not scope
  creep, a hard dependency of the version bump itself:
  `TestVendorVersionMatchesManifest` requires `VendorVersion` to equal
  `manifest.json`'s version (an HMRC Fraud Prevention Header requirement;
  this repo's own CLAUDE.md says "bump both together"), and
  `wantVendorVersion` pins the resulting HTTP header string in a wasm
  integration test.
- `CLAUDE.md` updated to document the new `validate.sh` check.

## Dev

Opus 5.5. TDD-first: added the `validate.sh` check before touching
`manifest.json`, confirmed it failed naming exactly the one affected
setting, then fixed the manifest and confirmed it passed. Full local CI
step list green: `go vet` (wasip1/wasm cross-compile), `gofmt -l .`,
`scripts/guard-plugin-i18n.sh`, `go test ./...` (3 packages),
`scripts/build.sh`, `scripts/validate.sh`, `scripts/package.sh`.

## Independent verification (orchestrator, before review)

Re-ran every CI step myself and reproduced the same results. Personally
re-verified the TDD claim: reverted the default back to the string shape
(check left in place), confirmed `validate.sh` failed naming exactly
`eatin_standard_rate_by_tax_code`; restored and confirmed it passed again.

## Review

Independent Fable review (different model from the Opus author), in an
isolated worktree off this branch's pre-review commit. **Verdict: safe to
merge as-is, no required fixes.**

- Re-verified the TDD claim again independently (same revert/restore
  method) — reproduced the exact red and green output.
- Walked every other setting in the manifest (9 `hmrc_*` empty-string
  defaults, the `hmrc_api_base` sandbox URL) against the new heuristic and
  confirmed zero false positives.
- Confirmed the `VendorVersion`/`wantVendorVersion` changes are necessary
  (verified `TestVendorVersionMatchesManifest` and the Fraud Prevention
  Header assembly in `client.go`) and correctly matched to `1.2.1`, with no
  stale `1.2.0` references left anywhere except correctly-historical ones
  (README's "Since ut-docs#1475 (v1.2.0)" note, an old review record).
- Checked against `universal-till` core source (read-only) whether this
  fix needs a stored-setting migration: **no** — `ReconcilePluginSettings`
  preserves an existing stored value across a plugin version bump, and the
  host read path (`hostSettingsGet`) already unwraps one JSON-string level
  regardless of manifest shape, so this setting (never yet exercised by any
  caller) has no existing bad value to migrate. Also confirmed #1269's
  follow-up (universal-till `internal/plugins/manifest.go`'s
  `EncodeMapSettingValue`/`DecodeMapSettingValue`) already landed, so the
  `{}`-object shape is exactly what core expects today.
- Confirmed the version bump is correct and unreleased (`git ls-remote
  --tags origin`: only `v1.0.0`/`v1.1.0`/`v1.2.0` exist).
- Confirmed no file-write-without-`os.MkdirAll` or cwd-relative-path
  concerns apply (pure manifest/script/constant diff).
- No secrets, no real client/shop names.
- Optional, applied: rewrote the WIP commit message to a real one;
  documented the new check in `CLAUDE.md`.

## Verdict

Safe to merge. No ADR implicated (established pattern per #1255's
precedent). No UI surface, no new user-facing i18n string, no money type
involved; the eat-in override itself stays dormant/unwired, as intended —
out of scope for this card.
