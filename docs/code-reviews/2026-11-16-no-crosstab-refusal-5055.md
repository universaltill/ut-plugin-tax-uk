# Review: empty cross-tab refusal follows core's identities (ut-docs#5055 item 2)

Branch `fix/5055-voucher-only-close`, v1.3.0 → v1.3.1. Written by a Sonnet
Dev agent; independent review by Opus 5.5 (reviewer skill).

## What shipped

Before this change, the MTD bridge (`BuildSubmitRequest`) and the VAT figures
export (`BuildFigures`) refused any close with an empty per-method tax
cross-tab (`method_tax_bands`) whenever `gross != 0`. So a Z close that only
issued multi-purpose vouchers was refused as "archived before the cross-tab
existed". A refunds-only legacy close (gross 0) passed silently as 0.

Both call sites now share one helper, `crossTabProblem` (`src/hmrcvat/figures.go`).
A close with an empty cross-tab is accepted only when both of core's
reconciliation identities leave nothing for the bands to carry:

- `gross − refund_total − vouchers_issued == 0`. This is
  `sum(band.Gross) == Net − multi-purpose vouchers issued`, with
  `Net = Gross − RefundTotal`.
- `tax_net == 0`. This is `sum(band.Tax) == TaxNet`. It was added in review
  (finding F1).

When both hold, Box 1 (the sum of tax) and Box 6 (the sum of gross − tax) are
exactly 0, so accepting the close cannot under-report.

The refusal names the Z, its window, the signed unexplained amount, its VAT,
and the gross, refunds and vouchers. `closeLabel` now prints `from to to`.
The bridge no longer prints the empty `Report.Day`. The wire mirror
`EODReportForExport` gains `refund_total`, `vouchers_issued` and `tax_net`.
The README states the rule in three places and in "Verified".

Checked against universal-till (`main` @ 18157109):
- `internal/data/pos_repo.go`: `EODReport` JSON names (`gross`,
  `refund_total`, `tax_net`, `vouchers_issued`).
- `dateRangeSummary`: `RefundTotal` is the positive sum of return totals,
  `Net = Gross − RefundTotal`, and `TaxNet` is scanned in the same query as
  `Gross`, so any archive with `gross` has `tax_net`.
- `internal/data/voucher_repo.go` `VouchersIssuedRedeemedForRange`: issued
  counts multi-purpose vouchers only, including top-ups.
- `internal/pos/eod_tax_bands.go` and `internal/pages/eod_tax_bands.go`: the
  identities.
- Tips are outside Gross and outside the cross-tab.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | Major (silent under-report) | The gross-gap-only predicate accepted a legacy close with no cross-tab where a sale and an equal return cancel out (gross == refund_total). The old rule refused that close. If the two were at different VAT rates (for example £60 sold at 20% and £60 returned at 0%), £10 VAT was due but the close counted as 0 in Box 1/6. Zero-default legacy fields made this reachable. | **Fixed**: the close must also have `tax_net == 0`, and the message names the VAT. New `TestCrossTabProblem` rows cover this: "offsetting sale and return at different rates" is refused, "balanced … same rate" is accepted. The decode test pins the `tax_net` tag. |
| F2 | Minor (test strength, Tester N1) | The wasmrun refusal case (gross 5000) passed under the old predicate too. | **Fixed**: it is now a refunds-only close (gross 0, refund 1200), asserting `-£12.00` and `Z9`. |
| F3 | Minor | `vouchers_issued > gross`, for example when a voucher transaction's local date falls in another window than its sale, gives a negative gap, so the close is refused with a signed `-£X`. | **Accepted**: the failure is a refusal, never a silent pass. It is only reachable on a close with an empty cross-tab. |
| F4 | Info | Overflow: int64 pence, three subtractions. Not reachable with real till takings. | Accepted |
| F5 | Info | The wasm-level voucher-only case covers only the figures export, not the bridge (Tester N2). The bridge goes through the same helper and is covered at unit level. | Accepted |

Generic checks: there are no file writes and no paths in the diff, so the
MkdirAll and cwd-relative path checks do not apply. Money is integer pence
only, with no floats. `pounds()` signs before `£`. VendorVersion ==
manifest == `wantVendorVersion` == 1.3.1. The README does not overclaim and
fabricates no box values.

## Extra verification

- Tester mutation (5055-test.md): reverting the predicate to the old
  `empty && gross != 0` made `TestCrossTabProblem` fail (AC9 voucher-only
  refused, AC11 refunds-only accepted, balanced mix refused).
  `TestEODCloseExport_DecodesVoucherFields` and the wasm
  `TestVATFiguresExport_AcceptsVoucherOnlyClose` also failed. The tree was
  restored byte-identical (cmp).
- Tester driven run on the real wasm: mtd closes plus a voucher-only Z43 gave
  a CSV with Box 1 69.10, Box 6 404 (unchanged from baseline), 3 closes, the
  Z43 row at 0.00, and 0 HTTP calls.
- Reviewer mutation A: removing the `tax_net` clause fails the "offsetting
  sale and return at different rates" row on both the bridge and the figures
  export. Restored with cmp.
- Reviewer mutation B: the old predicate fails the reworked wasm
  `TestVATFiguresExport_NoCrossTabRefusalNamesAmount`. Restored.
- Gate after the fixes, all green:
  - `GOOS=wasip1 GOARCH=wasm go vet ./...`
  - `gofmt -l .` (empty)
  - `go test -count=1 ./...`
  - `scripts/build.sh`
  - `scripts/validate.sh` (v1.3.1)
  - `scripts/guard-plugin-i18n.sh`
  - `scripts/check-version-bump.test.sh`

## Verdict

**Approve.** F1 and F2 are fixed in the working tree. Nothing blocks.

## Deferred

- Item 1 of ut-docs#5055, bucketing closes by trading date into the VAT
  period, is tracked on ut-docs#6313.

Pre-flight: review record updated. Plugin README updated (Verified section
and Known gaps). Till help/docs-shots n/a (plugin only, no till UI change).
en.json/meta n/a (Go error strings, no locale keys). Manifest
field/host function/plugin type n/a (version bump only). ADR n/a (a bug fix
that brings the check into line with core's documented identities).
Plan/price and platform claims n/a. Follow-up work deferred → ut-docs#6313.
