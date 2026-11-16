package wasmrun_test

// The accountant VAT figures export (vat-figures-uk, ut-docs#5052), driven
// through the REAL compiled wasm under wazero. Pure local computation: every
// case also pins that no HTTP call is made. (The HMRC MTD bridge that used to
// be pinned here moved to ut-plugin-export-mtd-uk, ut-docs#6289.)

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
)

var (
	figuresWasmOnce sync.Once
	figuresWasmBin  []byte
)

// figuresWasm builds the current source once for this file's cases (the bytes
// are held in memory, so the first test's TempDir cleanup doesn't matter).
func figuresWasm(t *testing.T) []byte {
	t.Helper()
	figuresWasmOnce.Do(func() { figuresWasmBin = buildWasm(t) })
	if figuresWasmBin == nil {
		t.Fatal("wasm build failed in an earlier test")
	}
	return figuresWasmBin
}

// Same two closes as the MTD plugin's fixtureCloses (incl. one refund cell),
// in core's real export.requested.ask envelope.
const figuresClosesJSON = `[
 {"z_number":41,"report":{"from":"2026-07-01T09:00:00+01:00","to":"2026-07-01T21:00:00+01:00","gross":46459,"method_tax_bands":[
   {"method":"cash","rate_bp":2000,"net":10000,"tax":2000,"gross":12000},
   {"method":"card","rate_bp":2000,"net":25050,"tax":5010,"gross":30060},
   {"method":"card","rate_bp":0,"net":4399,"tax":0,"gross":4399}]}},
 {"z_number":42,"report":{"from":"2026-07-02T09:00:00+01:00","to":"2026-07-02T21:00:00+01:00","gross":900,"method_tax_bands":[
   {"method":"cash","rate_bp":500,"net":2000,"tax":100,"gross":2100},
   {"method":"card","rate_bp":2000,"net":-1000,"tax":-200,"gross":-1200}]}}
]`

func figuresEvent(from, to, entryKey, closes string) string {
	return `{"type":"export.requested.ask","payload":{"from":"` + from + `","to":"` + to + `","entry_key":"` + entryKey + `","sales":null,"eod_closes":` + closes + `}}`
}

// coreExportResponse mirrors universal-till internal/pages/data_api.go's
// exportResponse, decoded strictly.
type coreExportResponse struct {
	OK         bool   `json:"ok"`
	Filename   string `json:"filename,omitempty"`
	ContentB64 string `json:"content_b64,omitempty"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

func decodeExport(t *testing.T, out string) coreExportResponse {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(out)))
	dec.DisallowUnknownFields()
	var r coreExportResponse
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("stdout is not core's exportResponse shape: %v\nstdout: %q", err, out)
	}
	return r
}

// --- vat-figures-uk: the accountant export (ut-docs#5052) ---

// The figures export must work with NO hmrc_* setting and make NO network
// call, and returns a CSV inline the way core's file downloads expect.
func TestVATFiguresExport_NoNetworkNoSettings(t *testing.T) {
	h := &stubHost{settings: map[string]string{}, storage: map[string][]byte{}, respond: func(c httpCall) (int, string, bool) {
		t.Errorf("figures export made an HTTP call: %s %s", c.Method, c.URL)
		return 500, "", false
	}}
	out, _ := run(t, figuresWasm(t), h, figuresEvent("2026-07-01", "2026-07-02", "vat-figures-uk", figuresClosesJSON))
	if len(h.calls) != 0 {
		t.Fatalf("made %d HTTP calls, want 0", len(h.calls))
	}
	r := decodeExport(t, out)
	if !r.OK || r.Filename != "vat-figures-2026-07-01_2026-07-02.csv" || r.ContentB64 == "" || r.Error != "" {
		t.Fatalf("response = %+v", r)
	}
	raw, err := base64.StdEncoding.DecodeString(r.ContentB64)
	if err != nil {
		t.Fatal(err)
	}
	csv := string(raw)
	// Same closes as the bridge test: Box 1 = 20.00+50.10+0+1.00-2.00 = 69.10, Box 6 = 404.
	for _, want := range []string{"Box 1,VAT due on sales,69.10\r\n", "Box 6,Total value of sales ex VAT (whole pounds),404\r\n", "41,2026-07-01T09:00:00+01:00,2026-07-01T21:00:00+01:00,", "42,2026-07-02T09:00:00+01:00,2026-07-02T21:00:00+01:00,"} {
		if !strings.Contains(csv, want) {
			t.Errorf("CSV missing %q:\n%s", want, csv)
		}
	}
}

// A close that only issued multi-purpose vouchers has an empty cross-tab but
// is fully explained (gross - refunds - vouchers_issued = 0): accepted, adds
// nothing to Box 1/6 (ut-docs#5055).
func TestVATFiguresExport_AcceptsVoucherOnlyClose(t *testing.T) {
	h := &stubHost{settings: map[string]string{}, storage: map[string][]byte{}, respond: func(c httpCall) (int, string, bool) {
		t.Errorf("figures export made an HTTP call: %s %s", c.Method, c.URL)
		return 500, "", false
	}}
	closes := strings.TrimSuffix(strings.TrimSpace(figuresClosesJSON), "]") + `,
 {"z_number":43,"report":{"from":"2026-07-03T09:00:00+01:00","to":"2026-07-03T21:00:00+01:00","gross":2500,"vouchers_issued":2500}}]`
	out, _ := run(t, figuresWasm(t), h, figuresEvent("2026-07-01", "2026-07-31", "vat-figures-uk", closes))
	r := decodeExport(t, out)
	if !r.OK || r.Error != "" {
		t.Fatalf("voucher-only close refused: %+v", r)
	}
	raw, err := base64.StdEncoding.DecodeString(r.ContentB64)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Box 1,VAT due on sales,69.10\r\n", "Box 6,Total value of sales ex VAT (whole pounds),404\r\n"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("CSV missing %q:\n%s", want, raw)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("made %d HTTP calls, want 0", len(h.calls))
	}
}

// A refunds-only close with no cross-tab (gross 0) was silently counted as 0
// under the old gross != 0 predicate; it must be refused, naming Z9 and the
// signed unexplained amount (ut-docs#5055).
func TestVATFiguresExport_NoCrossTabRefusalNamesAmount(t *testing.T) {
	h := &stubHost{settings: map[string]string{}, storage: map[string][]byte{}}
	out, _ := run(t, figuresWasm(t), h, figuresEvent("2026-07-01", "2026-07-31", "vat-figures-uk", `[{"z_number":9,"report":{"from":"2026-07-01T09:00:00+01:00","to":"2026-07-01T21:00:00+01:00","gross":0,"refund_total":1200,"tax_net":-200}}]`))
	r := decodeExport(t, out)
	if r.OK || !strings.Contains(r.Error, "-£12.00") || !strings.Contains(r.Error, "Z9") {
		t.Errorf("response = %+v, want refusal naming Z9 and -£12.00", r)
	}
}

func TestVATFiguresExport_RefusesWithoutNetwork(t *testing.T) {
	h := &stubHost{settings: map[string]string{}, storage: map[string][]byte{}}
	for name, ev := range map[string]string{
		"no closes":    figuresEvent("2026-07-01", "2026-07-31", "vat-figures-uk", "[]"),
		"no range":     figuresEvent("", "", "vat-figures-uk", figuresClosesJSON),
		"no cross-tab": figuresEvent("2026-07-01", "2026-07-31", "vat-figures-uk", `[{"z_number":9,"report":{"from":"2026-07-01T09:00:00+01:00","to":"2026-07-01T21:00:00+01:00","gross":5000}}]`),
		"not granted":  `{"type":"export.requested.ask","payload":{"from":"2026-07-01","to":"2026-07-31","entry_key":"vat-figures-uk","eod_closes":null}}`,
	} {
		out, _ := run(t, figuresWasm(t), h, ev)
		r := decodeExport(t, out)
		if r.OK || r.Error == "" || r.ContentB64 != "" {
			t.Errorf("%s: response = %+v, want a refusal", name, r)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("refusals made %d HTTP calls, want 0", len(h.calls))
	}
}

func TestManifestDeclaresVATFiguresExport(t *testing.T) {
	b, err := os.ReadFile("../../manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Permissions []string `json:"permissions"`
		Entries     []struct {
			Type     string   `json:"type"`
			Key      string   `json:"key"`
			Label    string   `json:"label"`
			Entities []string `json:"entities"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range m.Entries {
		if e.Key == "vat-figures-uk" {
			found = true
			if e.Type != "export" || e.Label != "tax_uk.entry_vat_figures_label" || len(e.Entities) != 1 || e.Entities[0] != "eod_closes" {
				t.Errorf("vat-figures entry = %+v", e)
			}
		}
	}
	if !found {
		t.Error("entries[] missing vat-figures-uk")
	}
}
