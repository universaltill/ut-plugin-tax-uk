// Package chargepolicy is the pure, host-independent half of this plugin's
// charge.policy.ask answer (ADR-0061 Decision 1, ut-docs#975): the United
// Kingdom's service-charge and tip policy, as core's
// universal-till internal/pages/charge_hook.go parses it.
//
// It is its own package with no wasip1 tag so `go test ./src/chargepolicy/...`
// runs on the host, while main.go (wasip1-only) cannot be unit-tested at all.
// Same split as ut-plugin-tax-de's src/chargepolicy/ (ut-docs#974).
//
// The answer changes nothing core would not already do with no plugin
// installed (charge permitted, apportioned at the lines' own rates, tip to
// the employee) — its value is that core now holds the UK's policy as an
// ANSWER rather than a fallback, plus the UK's 12.5% suggested rate for a
// settings UI. The merchant's own configured service-charge rate (and
// whether a service charge is switched on at all) stays authoritative for
// what is charged; core never applies ServiceChargeDefaultRateBP.
package chargepolicy

// Answer is the charge.policy.ask wire shape. No omitempty anywhere: an
// absent service_charge_permitted reads as permitted on core's side, so
// the field must never be able to vanish from the wire. There is
// deliberately no Charges field — see GB().
type Answer struct {
	ServiceChargePermitted     bool   `json:"service_charge_permitted"`
	ServiceChargeDefaultRateBP int    `json:"service_charge_default_rate_bp"`
	ServiceChargeTaxBasisBP    int    `json:"service_charge_tax_basis_bp"`
	TipDefaultRecipient        string `json:"tip_default_recipient"`
	FiscalBusinessCase         string `json:"fiscal_business_case"`
}

// GB is the United Kingdom's researched row from ut-docs#961 (ADR-0061):
//
//   - A discretionary service charge is lawful and common in UK
//     hospitality, so it is permitted, with 12.5% (1250 bp) as the
//     market-norm SUGGESTED rate. This is informational only — a default a
//     settings UI can pre-fill when a merchant turns the charge on. Whether
//     a service charge is applied at all is still the till's own merchant
//     toggle, and the merchant's configured rate is what is charged.
//   - A service charge on a supply is further consideration for that supply
//     and is standard-rated where the supply is (HMRC VAT Notice 709/1
//     §2.3), so it is taxed at the sale's own per-line rate(s) — basis 0
//     tells core to apportion by net line value
//     (pos.ApportionServiceChargeTax). 0 means "apportion", not "no tax"; a
//     flat rate would mis-tax every bill mixing zero-rated takeaway-style
//     lines with standard-rated ones.
//   - A tip defaults to the employee: the Employment (Allocation of Tips)
//     Act 2023 requires tips, gratuities and service charges to be passed
//     to workers in full. A tip the business keeps is an explicit
//     per-payment choice core already carries (PaymentInput.TipRecipient),
//     not a market default, so nothing more is declared here.
//   - FiscalBusinessCase is left empty: it exists for Germany's DSFinV-K
//     export (GV_TYP, e.g. "TrinkgeldAN"), and the UK has no fiscal-export
//     mandate needing a business-case mapping for a tip or service charge.
//   - No additive statutory levies: ADR-0062's Charges list is applied by
//     core verbatim to every sale with no merchant override, and the UK
//     has none.
func GB() Answer {
	return Answer{
		ServiceChargePermitted:     true,
		ServiceChargeDefaultRateBP: 1250,
		ServiceChargeTaxBasisBP:    0,
		TipDefaultRecipient:        "employee", // literal: no fiscalsign package here (the UK has no TSE)
		FiscalBusinessCase:         "",
	}
}
