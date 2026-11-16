package vatfigures

import (
	"encoding/json"
	"strings"
	"testing"
)

func closeWith(z int64, day string, gross int64, bands ...MethodTaxBand) EODCloseExport {
	return EODCloseExport{ZNumber: z, Report: EODReportForExport{Day: day, Gross: gross, MethodTaxBands: bands}}
}

// TestBuildFigures_BoxesRatesAndCloses pins the accountant export: Box 1/6
// come from the cross-tab exactly as the MTD export plugin computes them, the
// manual boxes are left BLANK (never 0 — a till has no purchase ledger), and
// the per-rate / per-close lines add up to the same totals.
func TestBuildFigures_BoxesRatesAndCloses(t *testing.T) {
	closes := []EODCloseExport{
		closeWith(7, "2026-07-01", 12000,
			MethodTaxBand{Method: "cash", RateBP: 2000, Net: 5000, Tax: 1000, Gross: 6000},
			MethodTaxBand{Method: "card", RateBP: 2000, Net: 2500, Tax: 500, Gross: 3000},
			MethodTaxBand{Method: "card", RateBP: 0, Net: 3000, Tax: 0, Gross: 3000}),
		// a refund day: negative cells
		closeWith(8, "2026-07-02", -1200,
			MethodTaxBand{Method: "cash", RateBP: 2000, Net: -1000, Tax: -200, Gross: -1200}),
	}
	got, err := BuildFigures(closes, "2026-07-01", "2026-07-02")
	if err != nil {
		t.Fatal(err)
	}
	if got.Box1 != 1300 {
		t.Errorf("Box1 = %d, want 1300", got.Box1)
	}
	// net = 5000+2500+3000-1000 = 9500p = £95.00 -> Box 6 = 95
	if got.Box6 != 95 || got.NetSales != 9500 {
		t.Errorf("Box6/NetSales = %d/%d, want 95/9500", got.Box6, got.NetSales)
	}
	if len(got.Rates) != 2 || got.Rates[0].RateBP != 0 || got.Rates[1].RateBP != 2000 {
		t.Fatalf("rates = %+v, want sorted 0%% then 20%%", got.Rates)
	}
	if r := got.Rates[1]; r.Net != 6500 || r.Tax != 1300 || r.Gross != 7800 {
		t.Errorf("20%% row = %+v, want net 6500 tax 1300 gross 7800", r)
	}
	if len(got.Closes) != 2 || got.Closes[0].ZNumber != 7 || got.Closes[1].Net != -1000 {
		t.Errorf("closes = %+v", got.Closes)
	}
}

func TestBuildFigures_Box6TruncatesPence(t *testing.T) {
	got, err := BuildFigures([]EODCloseExport{closeWith(1, "2026-07-01", 1099,
		MethodTaxBand{RateBP: 0, Net: 1099, Gross: 1099})}, "2026-07-01", "2026-07-01")
	if err != nil {
		t.Fatal(err)
	}
	if got.Box6 != 10 || got.NetSales != 1099 {
		t.Errorf("Box6/NetSales = %d/%d, want 10/1099 (pence left out, never rounded up)", got.Box6, got.NetSales)
	}
}

func TestBuildFigures_Refusals(t *testing.T) {
	if _, err := BuildFigures(nil, "2026-07-01", "2026-07-31"); err == nil || !strings.Contains(err.Error(), "no archived day-closes") {
		t.Errorf("no closes: err = %v", err)
	}
	if _, err := BuildFigures([]EODCloseExport{closeWith(1, "d", 1)}, "", "2026-07-31"); err == nil || !strings.Contains(err.Error(), "from/to") {
		t.Errorf("no range: err = %v", err)
	}
	// a close with sales but no cross-tab is refused by Z-number, never counted as 0
	_, err := BuildFigures([]EODCloseExport{closeWith(41, "2026-07-03", 5000)}, "2026-07-01", "2026-07-31")
	if err == nil || !strings.Contains(err.Error(), "Z41") {
		t.Errorf("no cross-tab: err = %v, want refusal naming Z41", err)
	}
}

func TestFigures_CSV(t *testing.T) {
	c7 := closeWith(7, "", 6000, MethodTaxBand{Method: "cash", RateBP: 2000, Net: 5000, Tax: 1000, Gross: 6000})
	// current core (ADR-0066): Day empty, window in From/To
	c7.Report.From, c7.Report.To = "2026-07-01T09:00:00+01:00", "2026-07-01T21:00:00+01:00"
	f, err := BuildFigures([]EODCloseExport{c7}, "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatal(err)
	}
	csv := string(f.CSV())
	for _, want := range []string{
		"Period from,2026-07-01\r\n",
		"Period to,2026-07-31\r\n",
		"Box 1,VAT due on sales,10.00\r\n",
		"Box 6,Total value of sales ex VAT (whole pounds),50\r\n",
		"Box 2,VAT due on acquisitions,\r\n",
		"Box 4,VAT reclaimed on purchases,\r\n",
		"20.00,50.00,10.00,60.00\r\n",
		"7,2026-07-01T09:00:00+01:00,2026-07-01T21:00:00+01:00,50.00,10.00,60.00\r\n",
		"Flat Rate Scheme",
	} {
		if !strings.Contains(csv, want) {
			t.Errorf("CSV missing %q in:\n%s", want, csv)
		}
	}
	if strings.Contains(csv, "Box 3") || strings.Contains(csv, "Box 5") {
		t.Errorf("Box 3/5 depend on the manual boxes and must not be invented:\n%s", csv)
	}
}

func TestFigures_Filename(t *testing.T) {
	f := Figures{From: "2026-07-01", To: "2026-09-30"}
	if got := f.Filename(); got != "vat-figures-2026-07-01_2026-09-30.csv" {
		t.Errorf("filename = %q", got)
	}
}

func TestBuildFigures_NegativeNetTruncatesTowardZero(t *testing.T) {
	got, err := BuildFigures([]EODCloseExport{closeWith(1, "2026-07-01", -1099,
		MethodTaxBand{RateBP: 0, Net: -1099, Gross: -1099})}, "2026-07-01", "2026-07-01")
	if err != nil {
		t.Fatal(err)
	}
	if got.Box6 != -10 {
		t.Errorf("Box6 = %d, want -10 (toward zero, as the MTD export plugin)", got.Box6)
	}
}

func TestBuildFigures_MissingZWarned(t *testing.T) {
	b := MethodTaxBand{RateBP: 0, Net: 100, Gross: 100}
	f, err := BuildFigures([]EODCloseExport{closeWith(41, "2026-07-01", 100, b), closeWith(44, "2026-07-02", 100, b)}, "2026-07-01", "2026-07-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.MissingZ) != 2 || f.MissingZ[0] != 42 || f.MissingZ[1] != 43 {
		t.Fatalf("MissingZ = %v, want [42 43]", f.MissingZ)
	}
	if !strings.Contains(string(f.CSV()), `WARNING,"Day-closes missing from this range, so the figures may be understated: Z42 Z43"`) {
		t.Errorf("CSV lacks the missing-close warning:\n%s", f.CSV())
	}
	ok, _ := BuildFigures([]EODCloseExport{closeWith(41, "d", 100, b), closeWith(42, "d", 100, b)}, "a", "b")
	if strings.Contains(string(ok.CSV()), "WARNING") {
		t.Error("consecutive closes must not warn")
	}
}

// TestCrossTabProblem pins the empty-cross-tab check (ut-docs#5055 item 2):
// core's identity is sum(cross-tab gross) == gross - refund_total -
// multi-purpose vouchers_issued, so a close with no cross-tab is refused only
// when that gap is non-zero. Each row runs through the figures export.
func TestCrossTabProblem(t *testing.T) {
	mkTax := func(gross, refund, vouchers, taxNet int64) EODCloseExport {
		return EODCloseExport{ZNumber: 43, Report: EODReportForExport{
			From: "2026-07-03T09:00:00+01:00", To: "2026-07-03T21:00:00+01:00",
			Gross: gross, RefundTotal: refund, VouchersIssued: vouchers, TaxNet: taxNet,
		}}
	}
	mk := func(gross, refund, vouchers int64) EODCloseExport { return mkTax(gross, refund, vouchers, 0) }
	rows := []struct {
		name  string
		c     EODCloseExport
		want  []string // empty: accepted
		never []string
	}{
		{"AC9 voucher-only close", mk(2500, 0, 2500), nil, nil},
		{"AC10 sales without cross-tab", mk(5000, 0, 0), []string{"Z43", "2026-07-03T09:00:00+01:00 to 2026-07-03T21:00:00+01:00", "£50.00", "method_tax_bands"}, []string{"Z43 ()", "( )"}},
		{"AC11 refunds only", mk(0, 1200, 0), []string{"Z43", "-£12.00", "method_tax_bands"}, nil},
		{"AC12 empty close", mk(0, 0, 0), nil, nil},
		{"balanced mix", mk(3000, 500, 2500), nil, nil},
		{"sales beside vouchers", mk(3000, 0, 2500), []string{"£5.00"}, nil},
		// £60 sold at 20% (VAT £10) and £60 returned at 0%: the gross gap is
		// zero but £10 VAT is due, so an empty cross-tab must be refused.
		{"offsetting sale and return at different rates", mkTax(6000, 6000, 0, 1000), []string{"Z43", "£0.00", "VAT £10.00"}, nil},
		{"balanced sale and return, same rate", mkTax(6000, 6000, 0, 0), nil, nil},
	}
	baseFig, err := BuildFigures(fixtureCloses(), "2026-07-01", "2026-07-31")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		closes := append(fixtureCloses(), r.c)
		fig, errF := BuildFigures(closes, "2026-07-01", "2026-07-31")
		if len(r.want) == 0 {
			if errF != nil {
				t.Errorf("%s: refused: %v", r.name, errF)
				continue
			}
			if fig.Box1 != baseFig.Box1 || fig.NetSales != baseFig.NetSales {
				t.Errorf("%s: figures moved: %+v", r.name, fig)
			}
			continue
		}
		for who, err := range map[string]error{"figures": errF} {
			if err == nil {
				t.Errorf("%s/%s: accepted, want refusal", r.name, who)
				continue
			}
			for _, w := range r.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("%s/%s: %q does not mention %q", r.name, who, err, w)
				}
			}
			for _, w := range r.never {
				if strings.Contains(err.Error(), w) {
					t.Errorf("%s/%s: %q must not contain %q", r.name, who, err, w)
				}
			}
		}
	}
}

// TestEODCloseExport_DecodesVoucherFields pins the JSON tags of the fields
// the cross-tab check reads.
func TestEODCloseExport_DecodesVoucherFields(t *testing.T) {
	var c EODCloseExport
	if err := json.Unmarshal([]byte(`{"z_number":5,"report":{"gross":2500,"refund_total":0,"vouchers_issued":2500,"method_tax_bands":null}}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Report.VouchersIssued != 2500 || c.Report.Gross != 2500 {
		t.Fatalf("decoded %+v", c.Report)
	}
	if _, err := BuildFigures([]EODCloseExport{c}, "2026-07-01", "2026-07-31"); err != nil {
		t.Errorf("voucher-only close refused: %v", err)
	}
	var r EODCloseExport
	if err := json.Unmarshal([]byte(`{"z_number":6,"report":{"refund_total":1200,"tax_net":-200}}`), &r); err != nil || r.Report.RefundTotal != 1200 || r.Report.TaxNet != -200 {
		t.Fatalf("refund_total/tax_net not decoded: %v %+v", err, r.Report)
	}
}
