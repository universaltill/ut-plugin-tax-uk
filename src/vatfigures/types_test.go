package vatfigures

import (
	"encoding/json"
	"testing"
)

// --- money: minor units -> HMRC wire format (the x100 trap) ---

// TestAmount_MinorUnitsToHMRCDecimal pins the one conversion the BA and
// Architect both called out as the easy place to ship a factor-of-100 bug:
// the till's cross-tab is integer pence (internal/money minor units), HMRC's
// VAT API wants pounds with two decimal places. 12345 pence is £123.45 —
// never 12345.00 and never 1.23.
func TestAmount_MinorUnitsToHMRCDecimal(t *testing.T) {
	cases := []struct {
		pence int64
		want  string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{9, "0.09"},
		{10, "0.10"},
		{99, "0.99"},
		{100, "1.00"},
		{12345, "123.45"},
		{1000000, "10000.00"},
		{-1, "-0.01"},
		{-12345, "-123.45"},
	}
	for _, c := range cases {
		if got := Amount(c.pence).String(); got != c.want {
			t.Errorf("Amount(%d).String() = %q, want %q", c.pence, got, c.want)
		}
		// The JSON form is the same text as a bare JSON number (not a
		// quoted string): HMRC's schema types the boxes as numbers.
		b, err := json.Marshal(Amount(c.pence))
		if err != nil {
			t.Fatalf("marshal %d: %v", c.pence, err)
		}
		if string(b) != c.want {
			t.Errorf("json.Marshal(Amount(%d)) = %s, want %s", c.pence, b, c.want)
		}
	}
}

func TestWholePounds_JSONIsIntegerPounds(t *testing.T) {
	b, _ := json.Marshal(WholePounds(1234))
	if string(b) != "1234" {
		t.Fatalf("WholePounds(1234) = %s, want 1234", b)
	}
	b, _ = json.Marshal(WholePounds(-7))
	if string(b) != "-7" {
		t.Fatalf("WholePounds(-7) = %s, want -7", b)
	}
}

// fixtureCloses: two archived day-closes, cash+card, standard (20%),
// reduced (5%) and zero rate, with one refund (negative cell) — summing to
// a deliberately awkward pence remainder on Box 6.
func fixtureCloses() []EODCloseExport {
	return []EODCloseExport{
		{ZNumber: 41, Report: EODReportForExport{Day: "2026-07-01", MethodTaxBands: []MethodTaxBand{
			{Method: "cash", RateBP: 2000, Net: 10000, Tax: 2000, Gross: 12000},
			{Method: "card", RateBP: 2000, Net: 25050, Tax: 5010, Gross: 30060},
			{Method: "card", RateBP: 0, Net: 4399, Tax: 0, Gross: 4399},
		}}},
		{ZNumber: 42, Report: EODReportForExport{Day: "2026-07-02", MethodTaxBands: []MethodTaxBand{
			{Method: "cash", RateBP: 500, Net: 2000, Tax: 100, Gross: 2100},
			{Method: "card", RateBP: 2000, Net: -1000, Tax: -200, Gross: -1200}, // a refund
		}}},
	}
}
