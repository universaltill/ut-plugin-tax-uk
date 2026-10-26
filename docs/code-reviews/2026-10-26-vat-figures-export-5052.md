# Review: VAT return figures export for the accountant (ut-docs#5052)

## What shipped (v1.3.0)
New export entry `vat-figures-uk`: Box 1 / Box 6 (same sums as the HMRC bridge), blank Boxes 2/4/7/8/9, per-rate table, per-Z lines, as a CSV via `content_b64`. No network call, no `hmrc_*` setting, no permission change.

## Independent review (Opus, read-only)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | high | Per-Z "Day" column blank on current core: ADR-0066 closes carry `report.from`/`to`, not `day`; fixtures used the old shape | Fixed: `From`/`To` read, legacy `Day` fallback, fixtures switched to the real shape, wasm test pins it |
| 2 | medium | A gap in the Z sequence (core skips unreadable archive rows) understates the period silently | Fixed: WARNING row listing missing Z-numbers (+ tests); boundary attribution (close made after midnight) is pre-existing in the bridge too, follow-up card |
| 3 | medium/low | Flat Rate / cash-accounting caveat only in the README | Fixed: stated in the CSV header row |
| 4 | low | No-cross-tab check compares headline gross (includes vouchers/tips), so a voucher-only close is refused with the wrong reason | Accepted, inherited from the bridge; follow-up card |
| 5 | low | No test for negative non-whole-pound Box 6; no wasm no-cross-tab test | Fixed: both added |

Verified: gofmt, wasip1 vet, build, validate.sh, guard-plugin-i18n, `go test -count=1 ./...` (wasmrun builds and runs the real wasm). Checked against core's `exportResponse` and eod_closes path. Not verified: a real installed plugin through the till's Data → Export page; Excel opening of the file.

## Verdict
Safe to merge after the fixes above. Out of scope, as stated in the README: whether the cross-tab is the right Box 1/6 source for non-standard VAT schemes.
