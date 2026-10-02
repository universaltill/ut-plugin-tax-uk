package chargepolicy

import (
	"encoding/json"
	"testing"
)

// TestGB_WireShape pins the exact JSON this plugin answers to core's
// charge.policy.ask (ADR-0061 Decision 1; core parses it in
// universal-till internal/pages/charge_hook.go's chargePolicyAskResponse).
// Every value is ut-docs#961's researched UK row — see the doc comments on
// GB() for the legal source of each.
func TestGB_WireShape(t *testing.T) {
	b, err := json.Marshal(GB())
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"service_charge_permitted":true,"service_charge_default_rate_bp":1250,"service_charge_tax_basis_bp":0,"tip_default_recipient":"employee","fiscal_business_case":""}`
	if string(b) != want {
		t.Fatalf("charge.policy.ask answer drifted\n got: %s\nwant: %s", b, want)
	}
}

// TestGB_PermittedIsExplicit guards the one field whose zero value is
// dangerous: core reads an ABSENT service_charge_permitted as permitted,
// but an explicit false suppresses every UK till's configured service
// charge. An omitempty tag here would be harmless today (true is never
// omitted) but would silently turn a future `false` into "absent =
// permitted", so the field must always be on the wire.
func TestGB_PermittedIsExplicit(t *testing.T) {
	a := GB()
	a.ServiceChargePermitted = false
	b, _ := json.Marshal(a)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if v, ok := m["service_charge_permitted"]; !ok || v != false {
		t.Fatalf("service_charge_permitted must always be serialised, got %s", b)
	}
}

// TestGB_NeverDeclaresAdditiveCharges: the UK has no statutory levy on a
// sale (ADR-0062's Charges list is GCC-only). A "charges" item here would
// be APPLIED VERBATIM by core to every UK sale — there is no merchant
// setting that can override it — so the answer must not carry the key at
// all.
func TestGB_NeverDeclaresAdditiveCharges(t *testing.T) {
	b, _ := json.Marshal(GB())
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if _, ok := m["charges"]; ok {
		t.Fatalf("GB answer must not declare additive charges, got %s", b)
	}
}
