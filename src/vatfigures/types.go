// Package vatfigures is the host-function-free half of the accountant VAT
// return figures export (ut-docs#5052): the shape of the archived day-closes
// the till sends on export.requested.ask, and the Box 1/6 sums computed from
// them. The HMRC MTD submission that used to share this package now lives in
// ut-plugin-export-mtd-uk (ut-docs#6289, ADR-0172 section 6).
package vatfigures

import (
	"fmt"
	"strconv"
)

// --- money ---

// Amount is a money value in minor units (pence) — the same integer the
// till's internal/money.Money and the EOD cross-tab carry. It marshals to
// HMRC's wire form: a JSON number in pounds with exactly two decimal places
// (12345 -> 123.45). Formatted with integer arithmetic only, never via
// float64, so no value can pick up a binary-rounding error on the way out.
type Amount int64

func (a Amount) String() string {
	v := int64(a)
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// MarshalJSON emits the decimal as a bare JSON number (HMRC types the boxes
// as numbers, not strings).
func (a Amount) MarshalJSON() ([]byte, error) { return []byte(a.String()), nil }

// WholePounds is a value in whole pounds — HMRC's Boxes 6-9 carry no pence
// (VAT Notice 700/12: "leave out the pence"). Marshals as a JSON integer.
type WholePounds int64

func (w WholePounds) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(w), 10)), nil
}

// wholePoundsFromPence leaves out the pence (truncates toward zero) —
// never rounds a merchant's turnover up.
func wholePoundsFromPence(p int64) WholePounds { return WholePounds(p / 100) }

// --- EOD close wire shape (mirror of universal-till's data.EODCloseExport) ---

// EODCloseExport mirrors universal-till's internal/data.EODCloseExport JSON
// shape (export.requested.ask's "eod_closes", ut-docs#1005) — only the
// fields this package reads; same deliberate wire-shape duplication as
// ut-plugin-tax-de's datev.EODCloseExport (no Go dependency on core).
type EODCloseExport struct {
	ZNumber int64              `json:"z_number"`
	Report  EODReportForExport `json:"report"`
}

// EODReportForExport carries the archived Z-report fields Box 1/6 read.
type EODReportForExport struct {
	Day string `json:"day"`
	// From/To are the close's window as RFC3339 instants (ADR-0066: current
	// core leaves Day empty and sets these; From is empty on the till's
	// first-ever close). Read only by the accountant export (ut-docs#5052).
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Gross is the close's own headline gross (core's EODReport.Gross). Read
	// only for the cross-tab gap check (crossTabProblem).
	Gross int64 `json:"gross"`
	// RefundTotal is core's EODReport.RefundTotal (positive); VouchersIssued
	// is multi-purpose voucher issuance only (core voucher_repo.go:906).
	// With Gross they give core's identity sum(cross-tab gross) ==
	// Gross - RefundTotal - VouchersIssued; read only by crossTabProblem.
	RefundTotal    int64 `json:"refund_total"`
	VouchersIssued int64 `json:"vouchers_issued"`
	// TaxNet is core's EODReport.TaxNet (sales' tax minus returns' tax);
	// core's identity sum(cross-tab tax) == TaxNet. Read only by
	// crossTabProblem: a sale and a return at different VAT rates can net
	// the gross gap to zero while leaving VAT due (ut-docs#5055 review).
	TaxNet int64 `json:"tax_net"`
	// MethodTaxBands is the payment-method x VAT-rate cross-tab
	// (ut-docs#1004). Returns are already signed negative in it, and
	// **multi-purpose** voucher issuance (a liability) and tips (held out of revenue) are
	// already excluded, so summing it gives the period's taxable turnover.
	MethodTaxBands []MethodTaxBand `json:"method_tax_bands"`
}

// MethodTaxBand mirrors universal-till's data.MethodTaxBand.
type MethodTaxBand struct {
	Method string `json:"method"`
	RateBP int    `json:"rate_bp"`
	Net    int64  `json:"net"`
	Tax    int64  `json:"tax"`
	Gross  int64  `json:"gross"`
}
