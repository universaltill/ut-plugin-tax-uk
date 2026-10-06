package wasmrun_test

// MTD VAT return bridge (ut-docs#1475), driven through the REAL compiled
// wasm under wazero. The HTTP layer is the stubHost's scripted respond
// function — no request ever leaves the process — so these tests pin the
// exact call sequence, URLs, headers and bodies the plugin would send to
// HMRC's sandbox, and the exact stdout core's data_api.go decodes.

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

var (
	mtdWasmOnce sync.Once
	mtdWasmBin  []byte
)

// mtdWasm builds the current source once for this file's cases (the bytes
// are held in memory, so the first test's TempDir cleanup doesn't matter).
func mtdWasm(t *testing.T) []byte {
	t.Helper()
	mtdWasmOnce.Do(func() { mtdWasmBin = buildWasm(t) })
	if mtdWasmBin == nil {
		t.Fatal("wasm build failed in an earlier test")
	}
	return mtdWasmBin
}

func mtdSettings() map[string]string {
	return map[string]string{
		"hmrc_client_id":                  "client-abc",
		"hmrc_client_secret":              "secret-xyz",
		"hmrc_refresh_token":              "seed-refresh",
		"hmrc_vrn":                        "123456789",
		"hmrc_api_base":                   "https://test-api.service.hmrc.gov.uk",
		"hmrc_box2_vat_due_acquisitions":  "0.00",
		"hmrc_box4_vat_reclaimed":         "15.00",
		"hmrc_box7_purchases_ex_vat":      "900",
		"hmrc_box8_goods_supplied_ex_vat": "0",
		"hmrc_box9_acquisitions_ex_vat":   "0",
	}
}

// Same two closes as src/hmrcvat's fixtureCloses (incl. one refund cell),
// in core's real export.requested.ask envelope.
const mtdClosesJSON = `[
 {"z_number":41,"report":{"day":"2026-07-01","gross":46459,"method_tax_bands":[
   {"method":"cash","rate_bp":2000,"net":10000,"tax":2000,"gross":12000},
   {"method":"card","rate_bp":2000,"net":25050,"tax":5010,"gross":30060},
   {"method":"card","rate_bp":0,"net":4399,"tax":0,"gross":4399}]}},
 {"z_number":42,"report":{"day":"2026-07-02","gross":900,"method_tax_bands":[
   {"method":"cash","rate_bp":500,"net":2000,"tax":100,"gross":2100},
   {"method":"card","rate_bp":2000,"net":-1000,"tax":-200,"gross":-1200}]}}
]`

func mtdEvent(from, to, entryKey, closes string) string {
	return `{"type":"export.requested.ask","payload":{"from":"` + from + `","to":"` + to + `","entry_key":"` + entryKey + `","sales":null,"eod_closes":` + closes + `}}`
}

const (
	sandbox           = "https://test-api.service.hmrc.gov.uk"
	tokenFixture      = `{"access_token":"acc-new","refresh_token":"ref-new","expires_in":14400,"scope":"read:vat write:vat","token_type":"bearer"}`
	obligationsBody   = `{"obligations":[{"periodKey":"26A1","start":"2026-07-01","end":"2026-09-30","due":"2026-11-07","status":"O"},{"periodKey":"26A2","start":"2026-10-01","end":"2026-12-31","due":"2027-02-07","status":"O"}]}`
	submitReceipt     = `{"processingDate":"2026-10-02T10:15:00.000Z","paymentIndicator":"BANK","formBundleNumber":"256660290587","chargeRefNumber":"aCxFaNx0FZsCvyWF"}`
	wantSubmitBody    = `{"periodKey":"26A1","vatDueSales":69.10,"vatDueAcquisitions":0.00,"totalVatDue":69.10,"vatReclaimedCurrPeriod":15.00,"netVatDue":54.10,"totalValueSalesExVAT":404,"totalValuePurchasesExVAT":900,"totalValueGoodsSuppliedExVAT":0,"totalAcquisitionsExVAT":0,"finalised":true}`
	wantVendorVersion = "ut-plugin-tax-uk=1.2.1"
)

// happyHMRC answers the three sandbox calls; anything else is a test bug.
func happyHMRC(t *testing.T) func(httpCall) (int, string, bool) {
	return func(c httpCall) (int, string, bool) {
		switch {
		case c.Method == "POST" && c.URL == sandbox+"/oauth/token":
			return 200, tokenFixture, true
		case c.Method == "GET" && c.URL == sandbox+"/organisations/vat/123456789/obligations?status=O":
			return 200, obligationsBody, true
		case c.Method == "POST" && c.URL == sandbox+"/organisations/vat/123456789/returns":
			return 201, submitReceipt, true
		}
		t.Errorf("unexpected HTTP call %s %s", c.Method, c.URL)
		return 404, `{"code":"MATCHING_RESOURCE_NOT_FOUND","message":"x"}`, true
	}
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

func assertHeaders(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s headers = %v, want exactly %v", label, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s header %s = %q, want %q", label, k, got[k], v)
		}
	}
}

// TestMTDVATExport_FullSequence pins the whole bridge: refresh grant ->
// obligations -> submit, with exact URLs, headers and bodies; the rotated
// refresh token persisted; the Gov-Client-* gap logged; and the exact
// stdout core decodes.
func TestMTDVATExport_FullSequence(t *testing.T) {
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{}, respond: happyHMRC(t)}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", mtdClosesJSON))

	if len(h.calls) != 3 {
		t.Fatalf("made %d HTTP calls, want 3 (token, obligations, returns): %+v\nlogs: %v", len(h.calls), h.calls, h.logs)
	}

	// 1. Refresh-token grant.
	tok := h.calls[0]
	if tok.Method != "POST" || tok.URL != sandbox+"/oauth/token" {
		t.Errorf("call 1 = %s %s, want POST /oauth/token", tok.Method, tok.URL)
	}
	assertHeaders(t, "token", tok.Headers, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	const wantTokenBody = "client_id=client-abc&client_secret=secret-xyz&grant_type=refresh_token&refresh_token=seed-refresh"
	if tok.Body() != wantTokenBody {
		t.Errorf("token body\n got: %s\nwant: %s", tok.Body(), wantTokenBody)
	}

	// 2. Open obligations.
	obl := h.calls[1]
	if obl.Method != "GET" || obl.URL != sandbox+"/organisations/vat/123456789/obligations?status=O" {
		t.Errorf("call 2 = %s %s", obl.Method, obl.URL)
	}
	assertHeaders(t, "obligations", obl.Headers, map[string]string{
		"Accept":                  "application/vnd.hmrc.1.0+json",
		"Authorization":           "Bearer acc-new",
		"Gov-Vendor-Version":      wantVendorVersion,
		"Gov-Vendor-Product-Name": "Universal%20Till",
	})
	if obl.Body() != "" {
		t.Errorf("GET obligations carried a body: %q", obl.Body())
	}

	// 3. Submit.
	sub := h.calls[2]
	if sub.Method != "POST" || sub.URL != sandbox+"/organisations/vat/123456789/returns" {
		t.Errorf("call 3 = %s %s", sub.Method, sub.URL)
	}
	assertHeaders(t, "returns", sub.Headers, map[string]string{
		"Accept":                  "application/vnd.hmrc.1.0+json",
		"Authorization":           "Bearer acc-new",
		"Content-Type":            "application/json",
		"Gov-Vendor-Version":      wantVendorVersion,
		"Gov-Vendor-Product-Name": "Universal%20Till",
	})
	if sub.Body() != wantSubmitBody {
		t.Errorf("submit body\n got: %s\nwant: %s", sub.Body(), wantSubmitBody)
	}

	// Rotated refresh token carried forward (HMRC refresh tokens are single-use).
	var cached struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		SeedRefreshToken string `json:"seed_refresh_token"`
	}
	if err := json.Unmarshal(h.storage["hmrc_token"], &cached); err != nil {
		t.Fatalf("hmrc_token not stored: %v (%q)", err, h.storage["hmrc_token"])
	}
	if cached.AccessToken != "acc-new" || cached.RefreshToken != "ref-new" || cached.SeedRefreshToken != "seed-refresh" {
		t.Errorf("cached token = %+v", cached)
	}

	// The fraud-prevention gap is logged, not papered over.
	logged := false
	for _, l := range h.logs {
		if strings.Contains(l, "Gov-Client-") && strings.Contains(l, "host function") {
			logged = true
		}
	}
	if !logged {
		t.Errorf("no log line about omitted Gov-Client-* headers: %v", h.logs)
	}

	const wantOut = `{"ok":true,"message":"MTD VAT return 2026-07-01..2026-09-30 (periodKey 26A1) accepted by HMRC at https://test-api.service.hmrc.gov.uk: formBundleNumber 256660290587, processingDate 2026-10-02T10:15:00.000Z. Box 1 £69.10, Box 3 £69.10, Box 5 £54.10, Box 6 £404, from 2 archived day-close(s)."}`
	if strings.TrimSpace(out) != wantOut {
		t.Fatalf("stdout drifted\n got: %s\nwant: %s\nlogs: %v", out, wantOut, h.logs)
	}
	if r := decodeExport(t, out); !r.OK {
		t.Fatalf("ok=false: %+v", r)
	}
}

// A cached, unexpired access token minted from the same settings seed is
// reused: no token call.
func TestMTDVATExport_ReusesCachedToken(t *testing.T) {
	h := &stubHost{settings: mtdSettings(), respond: happyHMRC(t), storage: map[string][]byte{
		"hmrc_token": []byte(`{"access_token":"acc-cached","expires_at":99999999999,"refresh_token":"ref-cached","seed_refresh_token":"seed-refresh"}`),
	}}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", mtdClosesJSON))
	if len(h.calls) != 2 || strings.HasSuffix(h.calls[0].URL, "/oauth/token") {
		t.Fatalf("calls = %+v, want obligations+returns only", h.calls)
	}
	if h.calls[0].Headers["Authorization"] != "Bearer acc-cached" {
		t.Errorf("Authorization = %q", h.calls[0].Headers["Authorization"])
	}
	if r := decodeExport(t, out); !r.OK {
		t.Fatalf("not ok: %+v", r)
	}
}

// An expired access token is refreshed with the ROTATED refresh token from
// storage, not the spent one in settings.
func TestMTDVATExport_RefreshesWithRotatedToken(t *testing.T) {
	h := &stubHost{settings: mtdSettings(), respond: happyHMRC(t), storage: map[string][]byte{
		"hmrc_token": []byte(`{"access_token":"acc-old","expires_at":1,"refresh_token":"ref-rotated","seed_refresh_token":"seed-refresh"}`),
	}}
	run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", mtdClosesJSON))
	if len(h.calls) == 0 {
		t.Fatal("no calls")
	}
	v, _ := url.ParseQuery(h.calls[0].Body())
	if v.Get("refresh_token") != "ref-rotated" {
		t.Fatalf("refresh grant used %q, want ref-rotated", v.Get("refresh_token"))
	}
}

// A cached access token HMRC no longer accepts (expired for real, or
// revoked) is refreshed ONCE — with the rotated refresh token — and the
// obligations call retried; the return then goes through normally. This is
// the path that actually keeps the bridge alive in the real till (found in
// review, ut-docs#1475): universal-till runs plugins under wazero's default
// fake wall clock (no WithSysWalltime in internal/plugins/wasm_runtime.go —
// this harness is faithful to that), so the guest's time.Now() is
// ~2022-01-01 on every run and CachedToken.ExpiresAt can never, by itself,
// notice HMRC's ~4-hour expiry.
func TestMTDVATExport_RejectedCachedTokenRefreshedOnce(t *testing.T) {
	base := happyHMRC(t)
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{
		"hmrc_token": []byte(`{"access_token":"acc-stale","expires_at":99999999999,"refresh_token":"ref-rotated","seed_refresh_token":"seed-refresh"}`),
	}}
	h.respond = func(c httpCall) (int, string, bool) {
		if strings.HasSuffix(c.URL, "/obligations?status=O") && c.Headers["Authorization"] == "Bearer acc-stale" {
			return 401, `{"code":"INVALID_CREDENTIALS","message":"Invalid Authentication information provided"}`, true
		}
		return base(c)
	}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", mtdClosesJSON))

	var seq []string
	for _, c := range h.calls {
		seq = append(seq, c.Method+" "+strings.TrimPrefix(c.URL, sandbox))
	}
	want := []string{
		"GET /organisations/vat/123456789/obligations?status=O", // with the stale cached token -> 401
		"POST /oauth/token", // forced refresh
		"GET /organisations/vat/123456789/obligations?status=O", // retried once with the new token
		"POST /organisations/vat/123456789/returns",
	}
	if strings.Join(seq, "\n") != strings.Join(want, "\n") {
		t.Fatalf("call sequence\n got: %q\nwant: %q\nlogs: %v", seq, want, h.logs)
	}
	if v, _ := url.ParseQuery(h.calls[1].Body()); v.Get("refresh_token") != "ref-rotated" {
		t.Errorf("forced refresh used %q, want the rotated ref-rotated (not the spent settings seed)", v.Get("refresh_token"))
	}
	if h.calls[2].Headers["Authorization"] != "Bearer acc-new" || h.calls[3].Headers["Authorization"] != "Bearer acc-new" {
		t.Errorf("retry/submit not made with the new token: %q / %q", h.calls[2].Headers["Authorization"], h.calls[3].Headers["Authorization"])
	}
	if !strings.Contains(string(h.storage["hmrc_token"]), `"access_token":"acc-new"`) {
		t.Errorf("cache not replaced after the forced refresh: %s", h.storage["hmrc_token"])
	}
	if r := decodeExport(t, out); !r.OK {
		t.Fatalf("not ok after a successful re-auth: %+v", r)
	}
}

// A 401 on a token minted JUST NOW is final: no second refresh, no loop, and
// nothing submitted — the error carries HMRC's own code.
func TestMTDVATExport_FreshTokenRejectedDoesNotLoop(t *testing.T) {
	base := happyHMRC(t)
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{}}
	h.respond = func(c httpCall) (int, string, bool) {
		if strings.HasSuffix(c.URL, "/obligations?status=O") {
			return 401, `{"code":"INVALID_CREDENTIALS","message":"Invalid Authentication information provided"}`, true
		}
		return base(c)
	}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", mtdClosesJSON))
	if len(h.calls) != 2 || !strings.HasSuffix(h.calls[0].URL, "/oauth/token") || !strings.HasSuffix(h.calls[1].URL, "/obligations?status=O") {
		t.Fatalf("calls = %+v, want exactly token + one obligations attempt", h.calls)
	}
	if r := decodeExport(t, out); r.OK || !strings.Contains(r.Error, "INVALID_CREDENTIALS") {
		t.Fatalf("got %+v", r)
	}
}

// A range that is not exactly an open period is refused BY NAME, and
// nothing is submitted.
func TestMTDVATExport_NoMatchingObligationRefuses(t *testing.T) {
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{}, respond: happyHMRC(t)}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-07-31", "mtd-vat-return-uk", mtdClosesJSON))
	for _, c := range h.calls {
		if strings.HasSuffix(c.URL, "/returns") {
			t.Fatal("submitted a return for a non-matching period")
		}
	}
	r := decodeExport(t, out)
	if r.OK || !strings.Contains(r.Error, "2026-07-01..2026-09-30 (periodKey 26A1") {
		t.Fatalf("want refusal listing open periods, got %+v", r)
	}
}

// Missing settings refuse with the setting's name and never touch the network.
func TestMTDVATExport_MissingSettingRefusesWithoutNetwork(t *testing.T) {
	s := mtdSettings()
	delete(s, "hmrc_box4_vat_reclaimed") // host answers not-found
	s["hmrc_vrn"] = ""
	h := &stubHost{settings: s, storage: map[string][]byte{}, respond: happyHMRC(t)}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", mtdClosesJSON))
	if len(h.calls) != 0 {
		t.Fatalf("made %d HTTP calls before settings were valid", len(h.calls))
	}
	r := decodeExport(t, out)
	if r.OK || !strings.Contains(r.Error, "hmrc_vrn") || !strings.Contains(r.Error, "hmrc_box4_vat_reclaimed") {
		t.Fatalf("want refusal naming both settings, got %+v", r)
	}
}

// No archived closes in range (host sent "[]") refuses without the network.
func TestMTDVATExport_NoClosesRefusesWithoutNetwork(t *testing.T) {
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{}, respond: happyHMRC(t)}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", "[]"))
	if len(h.calls) != 0 {
		t.Fatalf("made %d HTTP calls with no closes", len(h.calls))
	}
	if r := decodeExport(t, out); r.OK || !strings.Contains(r.Error, "day-close") {
		t.Fatalf("got %+v", r)
	}
}

// eod_closes absent (null) means the host never granted/declared it — a
// config gap, reported as such, not as "no closes".
func TestMTDVATExport_NullClosesNamesPermission(t *testing.T) {
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{}, respond: happyHMRC(t)}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", "null"))
	if len(h.calls) != 0 {
		t.Fatal("network touched")
	}
	if r := decodeExport(t, out); r.OK || !strings.Contains(r.Error, "sales:read") {
		t.Fatalf("got %+v", r)
	}
}

// An HMRC rejection surfaces HMRC's own error code.
func TestMTDVATExport_HMRCRejectionSurfacesCode(t *testing.T) {
	base := happyHMRC(t)
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{}, respond: func(c httpCall) (int, string, bool) {
		if strings.HasSuffix(c.URL, "/returns") {
			return 403, `{"code":"DUPLICATE_SUBMISSION","message":"The VAT return was already submitted for the given period."}`, true
		}
		return base(c)
	}}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", mtdClosesJSON))
	if r := decodeExport(t, out); r.OK || !strings.Contains(r.Error, "DUPLICATE_SUBMISSION") {
		t.Fatalf("got %+v", r)
	}
}

// A transport failure on the token call reports failure, never a submission.
func TestMTDVATExport_TransportFailure(t *testing.T) {
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{}} // respond nil -> host error
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "mtd-vat-return-uk", mtdClosesJSON))
	if len(h.calls) != 1 {
		t.Fatalf("calls = %d, want 1 (token attempt only)", len(h.calls))
	}
	if r := decodeExport(t, out); r.OK || !strings.Contains(r.Error, "token") {
		t.Fatalf("got %+v", r)
	}
}

// Another entry key: decline (no stdout), no network.
func TestMTDVATExport_ForeignEntryKeyDeclines(t *testing.T) {
	h := &stubHost{settings: mtdSettings(), storage: map[string][]byte{}, respond: happyHMRC(t)}
	out, _ := run(t, mtdWasm(t), h, mtdEvent("2026-07-01", "2026-09-30", "someone-elses-export", mtdClosesJSON))
	if strings.TrimSpace(out) != "" || len(h.calls) != 0 {
		t.Fatalf("foreign entry_key answered %q with %d calls", out, len(h.calls))
	}
}

// Core only dispatches export.requested.ask to a manifest that hooks it,
// only resolves the entry if it exists, only sends eod_closes when the entry
// declares it AND the plugin holds sales:read (data_api.go), and the host
// only allows egress to a net:-granted host.
func TestManifestDeclaresMTDExport(t *testing.T) {
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
		Hooks []struct {
			Event string `json:"event"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	hooked := false
	for _, h := range m.Hooks {
		hooked = hooked || h.Event == "export.requested.ask"
	}
	if !hooked {
		t.Error("hooks[] missing export.requested.ask")
	}
	found := false
	for _, e := range m.Entries {
		if e.Key == "mtd-vat-return-uk" {
			found = true
			if e.Type != "export" || e.Label != "tax_uk.entry_mtd_vat_label" || len(e.Entities) != 1 || e.Entities[0] != "eod_closes" {
				t.Errorf("mtd entry = %+v", e)
			}
		}
	}
	if !found {
		t.Error("entries[] missing mtd-vat-return-uk")
	}
	perms := strings.Join(m.Permissions, " ")
	for _, p := range []string{"sales:read", "storage", "net:test-api.service.hmrc.gov.uk"} {
		if !strings.Contains(" "+perms+" ", " "+p+" ") {
			t.Errorf("permissions missing %s", p)
		}
	}
	if strings.Contains(perms, "net:api.service.hmrc.gov.uk") {
		t.Error("production HMRC host granted — out of scope until HMRC approval")
	}
}
