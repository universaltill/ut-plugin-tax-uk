package hmrcvat

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Figures is the accountant export (ut-docs#5052): the VAT return figures the
// till can truthfully compute from archived day-closes, with no HMRC
// connection and no hmrc_* setting. Box 1 and Box 6 use exactly the sums
// BuildSubmitRequest uses; Boxes 2/4/7/8/9 are the accountant's and are left
// blank, and Boxes 3/5 are not shown because they depend on them.
type Figures struct {
	From, To string
	Box1     Amount      // VAT due on sales
	Box6     WholePounds // sales ex VAT, pence left out
	// MissingZ lists Z-numbers absent between the lowest and highest close
	// in the range (core skips an unreadable archive row with only a log
	// line, so a gap could otherwise understate the period silently).
	MissingZ []int64
	NetSales Amount // exact net, so the accountant can reconcile Box 6
	Rates    []RateFigures
	Closes   []CloseFigures
}

// RateFigures is one VAT rate's totals across every payment method.
type RateFigures struct {
	RateBP          int
	Net, Tax, Gross Amount
}

// CloseFigures is one archived day-close's totals.
type CloseFigures struct {
	ZNumber         int64
	From, To        string // the close's window (ADR-0066), or the legacy Day in both
	Net, Tax, Gross Amount
}

// BuildFigures sums the closes. Same refusals as BuildSubmitRequest: an
// explicit range, at least one close, and no close with sales but no
// cross-tab (refused by Z-number rather than silently counted as zero).
func BuildFigures(closes []EODCloseExport, from, to string) (Figures, error) {
	if from == "" || to == "" {
		return Figures{}, fmt.Errorf("VAT figures need an explicit from/to range — the period is never guessed")
	}
	if len(closes) == 0 {
		return Figures{}, fmt.Errorf("VAT figures not produced: no archived day-closes in the requested period — they are computed from archived day-close (Z-report) records only, so close every trading day in the period first")
	}
	var problems []string
	byRate := map[int]*RateFigures{}
	f := Figures{From: from, To: to}
	var net, tax int64
	for _, c := range closes {
		if p := crossTabProblem(c, "its figures cannot be computed"); p != "" {
			problems = append(problems, p)
			continue
		}
		cf := CloseFigures{ZNumber: c.ZNumber, From: c.Report.From, To: c.Report.To}
		if cf.To == "" {
			cf.From, cf.To = c.Report.Day, c.Report.Day // legacy calendar-day close
		}
		for _, b := range c.Report.MethodTaxBands {
			r := byRate[b.RateBP]
			if r == nil {
				r = &RateFigures{RateBP: b.RateBP}
				byRate[b.RateBP] = r
			}
			r.Net += Amount(b.Net)
			r.Tax += Amount(b.Tax)
			r.Gross += Amount(b.Gross)
			cf.Net += Amount(b.Net)
			cf.Tax += Amount(b.Tax)
			cf.Gross += Amount(b.Gross)
			net += b.Net
			tax += b.Tax
		}
		f.Closes = append(f.Closes, cf)
	}
	if len(problems) > 0 {
		return Figures{}, fmt.Errorf("VAT figures not produced: %s", strings.Join(problems, "; "))
	}
	f.MissingZ = missingZ(f.Closes)
	for _, r := range byRate {
		f.Rates = append(f.Rates, *r)
	}
	sort.Slice(f.Rates, func(i, j int) bool { return f.Rates[i].RateBP < f.Rates[j].RateBP })
	f.Box1 = Amount(tax)
	f.NetSales = Amount(net)
	f.Box6 = wholePoundsFromPence(net)
	return f, nil
}

// closeLabel names a close's period for a message: its window, else the
// legacy day, else "no date".
func closeLabel(r EODReportForExport) string {
	switch {
	case r.From != "" && r.To != "":
		return r.From + " to " + r.To
	case r.To != "":
		return "to " + r.To
	case r.Day != "":
		return r.Day
	}
	return "no date"
}

// pounds formats pence as a signed pounds string ("£50.00", "-£12.00").
func pounds(p int64) string {
	if p < 0 {
		return "-£" + Amount(-p).String()
	}
	return "£" + Amount(p).String()
}

// crossTabProblem returns "" when c's cross-tab can account for its turnover,
// else the refusal clause naming the Z, its window and the unexplained amount.
// Core's identities are sum(cross-tab gross) == Gross - RefundTotal -
// VouchersIssued (multi-purpose vouchers only) and sum(cross-tab tax) ==
// TaxNet, so an empty cross-tab is fine exactly when both are zero — then
// Box 1 (sum of tax) and Box 6 (sum of gross - tax) are both exactly 0. The
// gross gap alone is not enough: a sale and an equal return at different VAT
// rates net it to zero while VAT is still due. consequence completes "so ..."
// for the caller.
func crossTabProblem(c EODCloseExport, consequence string) string {
	r := c.Report
	gap := r.Gross - r.RefundTotal - r.VouchersIssued
	if len(r.MethodTaxBands) != 0 || (gap == 0 && r.TaxNet == 0) {
		return ""
	}
	return fmt.Sprintf("close Z%d (%s) has turnover of %s (VAT %s) not covered by any VAT cross-tab (method_tax_bands): gross %s, refunds %s, multi-purpose vouchers issued %s — archived before the cross-tab existed, so %s; narrow the range to closes that carry one",
		c.ZNumber, closeLabel(r), pounds(gap), pounds(r.TaxNet), pounds(r.Gross), pounds(r.RefundTotal), pounds(r.VouchersIssued), consequence)
}

// missingZ returns the Z-numbers absent between the lowest and highest close.
func missingZ(cs []CloseFigures) []int64 {
	have := map[int64]bool{}
	var lo, hi int64
	for i, c := range cs {
		have[c.ZNumber] = true
		if i == 0 || c.ZNumber < lo {
			lo = c.ZNumber
		}
		if i == 0 || c.ZNumber > hi {
			hi = c.ZNumber
		}
	}
	var out []int64
	for z := lo; z <= hi && len(out) < 100; z++ {
		if !have[z] {
			out = append(out, z)
		}
	}
	return out
}

// Filename is the download name, e.g. vat-figures-2026-07-01_2026-09-30.csv.
func (f Figures) Filename() string {
	return "vat-figures-" + f.From + "_" + f.To + ".csv"
}

// rateString formats basis points as a percentage, 2000 -> "20.00".
func rateString(bp int) string { return Amount(bp).String() }

// CSV renders the figures. Every cell is a number or a fixed label — no
// merchant-entered text — so there is nothing to formula-escape.
func (f Figures) CSV() []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	rows := [][]string{
		{"VAT figures for your accountant (computed by Universal Till from archived day-closes; not a filed return)"},
		{"Assumes standard (invoice-basis) VAT accounting: not valid as Box 1/6 for the Flat Rate Scheme; check cash accounting with your accountant"},
		{"Period from", f.From},
		{"Period to", f.To},
		{"Day-closes included", strconv.Itoa(len(f.Closes))},
		{},
		{"Box", "Description", "Amount GBP"},
		{"Box 1", "VAT due on sales", f.Box1.String()},
		{"Box 2", "VAT due on acquisitions", ""},
		{"Box 4", "VAT reclaimed on purchases", ""},
		{"Box 6", "Total value of sales ex VAT (whole pounds)", strconv.FormatInt(int64(f.Box6), 10)},
		{"Box 7", "Total value of purchases ex VAT", ""},
		{"Box 8", "Total value of goods supplied ex VAT", ""},
		{"Box 9", "Total acquisitions ex VAT", ""},
		{"", "Net sales ex VAT, exact (Box 6 leaves out the pence)", f.NetSales.String()},
		{},
		{"VAT rate %", "Net", "VAT", "Gross"},
	}
	for _, r := range f.Rates {
		rows = append(rows, []string{rateString(r.RateBP), r.Net.String(), r.Tax.String(), r.Gross.String()})
	}
	if len(f.MissingZ) > 0 {
		ids := make([]string, len(f.MissingZ))
		for i, z := range f.MissingZ {
			ids[i] = "Z" + strconv.FormatInt(z, 10)
		}
		rows = append(rows, []string{}, []string{"WARNING", "Day-closes missing from this range, so the figures may be understated: " + strings.Join(ids, " ")})
	}
	rows = append(rows, []string{}, []string{"Z number", "Window from", "Window to", "Net", "VAT", "Gross"})
	for _, c := range f.Closes {
		rows = append(rows, []string{strconv.FormatInt(c.ZNumber, 10), c.From, c.To, c.Net.String(), c.Tax.String(), c.Gross.String()})
	}
	_ = w.WriteAll(rows)
	return buf.Bytes()
}
