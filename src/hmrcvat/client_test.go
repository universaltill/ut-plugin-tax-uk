package hmrcvat

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
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

func TestParsePence(t *testing.T) {
	ok := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"0.00", 0},
		{"12", 1200},
		{"12.3", 1230},
		{"12.34", 1234},
		{" 1500.00 ", 150000},
		{"-4.50", -450},
	}
	for _, c := range ok {
		got, err := parsePence(c.in)
		if err != nil || got != c.want {
			t.Errorf("parsePence(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "1.234", "1,000", "£12", "1e3", ".5", "1.", "--1", "12.3.4"} {
		if _, err := parsePence(bad); err == nil {
			t.Errorf("parsePence(%q) accepted, want error", bad)
		}
	}
}

// --- settings validation: refuse by name, never guess ---

func validSettings() Settings {
	return Settings{
		ClientID:       "cid",
		ClientSecret:   "csecret",
		RefreshToken:   "rtok",
		VRN:            "123456789",
		APIBase:        SandboxBase,
		Box2VATDueAcq:  "0.00",
		Box4VATReclaim: "150.25",
		Box7Purchases:  "900",
		Box8GoodsSupp:  "0",
		Box9Acq:        "0",
	}
}

func TestSettingsValidate_OK(t *testing.T) {
	m, base, err := validSettings().Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if base != SandboxBase {
		t.Errorf("base = %q", base)
	}
	want := ManualBoxes{VATDueAcquisitions: 0, VATReclaimedCurrPeriod: 15025, TotalValuePurchasesExVAT: 900, TotalValueGoodsSuppliedExVAT: 0, TotalAcquisitionsExVAT: 0}
	if m != want {
		t.Errorf("ManualBoxes = %+v, want %+v", m, want)
	}
}

// Every required setting, emptied in turn, must produce an error that NAMES
// that setting — the merchant has to know which field to fill.
func TestSettingsValidate_RefusesEachEmptyFieldByName(t *testing.T) {
	cases := map[string]func(*Settings){
		"hmrc_client_id":                  func(s *Settings) { s.ClientID = "" },
		"hmrc_client_secret":              func(s *Settings) { s.ClientSecret = " " },
		"hmrc_refresh_token":              func(s *Settings) { s.RefreshToken = "" },
		"hmrc_vrn":                        func(s *Settings) { s.VRN = "" },
		"hmrc_api_base":                   func(s *Settings) { s.APIBase = "" },
		"hmrc_box2_vat_due_acquisitions":  func(s *Settings) { s.Box2VATDueAcq = "" },
		"hmrc_box4_vat_reclaimed":         func(s *Settings) { s.Box4VATReclaim = "" },
		"hmrc_box7_purchases_ex_vat":      func(s *Settings) { s.Box7Purchases = "" },
		"hmrc_box8_goods_supplied_ex_vat": func(s *Settings) { s.Box8GoodsSupp = "" },
		"hmrc_box9_acquisitions_ex_vat":   func(s *Settings) { s.Box9Acq = "" },
	}
	for name, mutate := range cases {
		s := validSettings()
		mutate(&s)
		_, _, err := s.Validate()
		if err == nil {
			t.Errorf("%s empty: Validate accepted it", name)
			continue
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s empty: error %q does not name the setting", name, err)
		}
	}
}

func TestSettingsValidate_RejectsBadValues(t *testing.T) {
	cases := map[string]func(*Settings){
		"hmrc_vrn":                        func(s *Settings) { s.VRN = "12345678" },          // 8 digits
		"hmrc_vrn ":                       func(s *Settings) { s.VRN = "GB123456789" },       // prefix
		"hmrc_api_base":                   func(s *Settings) { s.APIBase = ProductionBase },  // out of scope
		"hmrc_api_base ":                  func(s *Settings) { s.APIBase = "http://x.test" }, // not https
		"hmrc_box4_vat_reclaimed":         func(s *Settings) { s.Box4VATReclaim = "12.345" },
		"hmrc_box7_purchases_ex_vat":      func(s *Settings) { s.Box7Purchases = "900.50" }, // pence on a whole-pounds box
		"hmrc_box8_goods_supplied_ex_vat": func(s *Settings) { s.Box8GoodsSupp = "abc" },
	}
	for name, mutate := range cases {
		s := validSettings()
		mutate(&s)
		_, _, err := s.Validate()
		if err == nil {
			t.Errorf("%s: Validate accepted a bad value", name)
			continue
		}
		if !strings.Contains(err.Error(), strings.TrimSpace(name)) {
			t.Errorf("%s: error %q does not name the setting", name, err)
		}
	}
}

func TestSettingsValidate_TrimsTrailingSlashOnBase(t *testing.T) {
	s := validSettings()
	s.APIBase = SandboxBase + "/"
	_, base, err := s.Validate()
	if err != nil || base != SandboxBase {
		t.Fatalf("base = %q, err = %v", base, err)
	}
}

// --- Box arithmetic ---

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

func TestBuildSubmitRequest_ComputesBoxes(t *testing.T) {
	manual := ManualBoxes{
		VATDueAcquisitions:           0,
		VATReclaimedCurrPeriod:       1500, // £15.00
		TotalValuePurchasesExVAT:     900,
		TotalValueGoodsSuppliedExVAT: 0,
		TotalAcquisitionsExVAT:       0,
	}
	got, err := BuildSubmitRequest(fixtureCloses(), manual)
	if err != nil {
		t.Fatalf("BuildSubmitRequest: %v", err)
	}
	// Box 1 = sum(Tax) = 2000+5010+0+100-200 = 6910p = £69.10
	if got.VATDueSales != 6910 {
		t.Errorf("Box 1 vatDueSales = %d, want 6910", got.VATDueSales)
	}
	// Box 3 = Box 1 + Box 2
	if got.TotalVATDue != 6910 {
		t.Errorf("Box 3 totalVatDue = %d, want 6910", got.TotalVATDue)
	}
	// Box 5 = |Box 3 - Box 4| = 6910 - 1500
	if got.NetVATDue != 5410 {
		t.Errorf("Box 5 netVatDue = %d, want 5410", got.NetVATDue)
	}
	// Box 6 = sum(Net) = 10000+25050+4399+2000-1000 = 40449p = £404.49 -> £404 (pence left out)
	if got.TotalValueSalesExVAT != 404 {
		t.Errorf("Box 6 totalValueSalesExVAT = %d, want 404 (whole pounds)", got.TotalValueSalesExVAT)
	}
	if got.TotalValuePurchasesExVAT != 900 || got.TotalValueGoodsSuppliedExVAT != 0 || got.TotalAcquisitionsExVAT != 0 {
		t.Errorf("Boxes 7/8/9 not taken verbatim from settings: %+v", got)
	}
	if got.VATReclaimedCurrPeriod != 1500 || got.VATDueAcquisitions != 0 {
		t.Errorf("Boxes 2/4 not taken verbatim from settings: %+v", got)
	}
	if !got.Finalised {
		t.Error("finalised must be true (HMRC rejects a non-finalised return)")
	}

	// The exact wire body (periodKey set by the caller after obligation matching).
	got.PeriodKey = "26A1"
	b, _ := json.Marshal(got)
	const want = `{"periodKey":"26A1","vatDueSales":69.10,"vatDueAcquisitions":0.00,"totalVatDue":69.10,"vatReclaimedCurrPeriod":15.00,"netVatDue":54.10,"totalValueSalesExVAT":404,"totalValuePurchasesExVAT":900,"totalValueGoodsSuppliedExVAT":0,"totalAcquisitionsExVAT":0,"finalised":true}`
	if string(b) != want {
		t.Fatalf("submit body drifted\n got: %s\nwant: %s", b, want)
	}
}

// Box 5 is the ABSOLUTE difference: a repayment period (more reclaimed than
// due) still sends a non-negative netVatDue — HMRC infers the direction.
// A merchant-entered Box 4 must never let Box 3/5 be skipped.
func TestBuildSubmitRequest_RepaymentPeriodBox5IsAbsolute(t *testing.T) {
	manual := ManualBoxes{VATDueAcquisitions: 90, VATReclaimedCurrPeriod: 10000}
	got, err := BuildSubmitRequest(fixtureCloses(), manual)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalVATDue != 6910+90 {
		t.Errorf("Box 3 = %d, want %d (Box 1 + Box 2)", got.TotalVATDue, 6910+90)
	}
	if got.NetVATDue != 10000-7000 {
		t.Errorf("Box 5 = %d, want %d", got.NetVATDue, 10000-7000)
	}
}

func TestBuildSubmitRequest_Box6TruncatesTowardZero(t *testing.T) {
	closes := []EODCloseExport{{ZNumber: 1, Report: EODReportForExport{MethodTaxBands: []MethodTaxBand{
		{Method: "card", RateBP: 2000, Net: -15099, Tax: -3020},
	}}}}
	got, err := BuildSubmitRequest(closes, ManualBoxes{})
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalValueSalesExVAT != -150 {
		t.Errorf("Box 6 = %d, want -150 (pence left out, not rounded away from zero)", got.TotalValueSalesExVAT)
	}
}

// A close that HAD sales but carries no cross-tab (an archive row from
// before ut-docs#1004) would contribute 0 to Box 1/6 and under-report the
// period: refused, naming the Z-number. A close with no sales at all and no
// cross-tab (a day closed with nothing sold) is fine — there is nothing to
// under-report.
func TestBuildSubmitRequest_RefusesCloseWithSalesButNoCrossTab(t *testing.T) {
	closes := append(fixtureCloses(), EODCloseExport{ZNumber: 43, Report: EODReportForExport{Day: "2026-07-03", Gross: 5000}})
	_, err := BuildSubmitRequest(closes, ManualBoxes{})
	if err == nil {
		t.Fatal("close with gross 5000 and no method_tax_bands accepted")
	}
	for _, want := range []string{"Z43", "2026-07-03", "50.00", "method_tax_bands"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	quiet := append(fixtureCloses(), EODCloseExport{ZNumber: 44, Report: EODReportForExport{Day: "2026-07-04", Gross: 0}})
	got, err := BuildSubmitRequest(quiet, ManualBoxes{})
	if err != nil {
		t.Fatalf("zero-sales close without a cross-tab refused: %v", err)
	}
	if got.VATDueSales != 6910 || got.TotalValueSalesExVAT != 404 {
		t.Errorf("boxes changed by an empty close: %+v", got)
	}
}

func TestBuildSubmitRequest_RefusesNoCloses(t *testing.T) {
	if _, err := BuildSubmitRequest(nil, ManualBoxes{}); err == nil {
		t.Fatal("nil closes accepted")
	}
	if _, err := BuildSubmitRequest([]EODCloseExport{}, ManualBoxes{}); err == nil {
		t.Fatal("empty closes accepted")
	}
}

// --- OAuth refresh ---

func TestTokenRefreshBody(t *testing.T) {
	got := string(TokenRefreshBody("cid", "c sec&ret", "r/tok"))
	const want = "client_id=cid&client_secret=c+sec%26ret&grant_type=refresh_token&refresh_token=r%2Ftok"
	if got != want {
		t.Fatalf("token body\n got: %s\nwant: %s", got, want)
	}
	v, err := url.ParseQuery(got)
	if err != nil || v.Get("client_secret") != "c sec&ret" || v.Get("grant_type") != "refresh_token" {
		t.Fatalf("token body does not round-trip: %v %v", v, err)
	}
}

// Reconstructed from HMRC's public "Authorisation" docs (refresh-token
// grant response). NEEDS SANDBOX VERIFICATION — same flag ut-plugin-tax-de
// carried on its fiskaly shapes until a live run confirmed them.
const tokenResponseFixture = `{"access_token":"acc-new","refresh_token":"ref-new","expires_in":14400,"scope":"read:vat write:vat","token_type":"bearer"}`

func TestParseTokenResponse(t *testing.T) {
	tr, err := ParseTokenResponse([]byte(tokenResponseFixture))
	if err != nil {
		t.Fatal(err)
	}
	if tr.AccessToken != "acc-new" || tr.RefreshToken != "ref-new" || tr.ExpiresIn != 14400 {
		t.Fatalf("parsed %+v", tr)
	}
	if _, err := ParseTokenResponse([]byte(`{"token_type":"bearer"}`)); err == nil {
		t.Fatal("response without access_token accepted")
	}
	if _, err := ParseTokenResponse([]byte(`not json`)); err == nil {
		t.Fatal("garbage accepted")
	}
}

// HMRC rotates refresh tokens: the one in settings is single-use. The
// rotated token must be carried forward, but a NEW token the merchant pastes
// into settings (re-consent) must win over the stored chain.
func TestRefreshTokenToUse(t *testing.T) {
	if got := RefreshTokenToUse("seed", nil); got != "seed" {
		t.Errorf("no cache: got %q", got)
	}
	c := &CachedToken{RefreshToken: "rotated", SeedRefreshToken: "seed"}
	if got := RefreshTokenToUse("seed", c); got != "rotated" {
		t.Errorf("same seed: got %q, want rotated", got)
	}
	if got := RefreshTokenToUse("pasted-new", c); got != "pasted-new" {
		t.Errorf("merchant re-consented: got %q, want pasted-new", got)
	}
	if got := RefreshTokenToUse("seed", &CachedToken{SeedRefreshToken: "seed"}); got != "seed" {
		t.Errorf("empty rotated token: got %q, want seed", got)
	}
}

func TestNewCachedTokenAndUsable(t *testing.T) {
	tr := TokenResponse{AccessToken: "a", RefreshToken: "r2", ExpiresIn: 14400}
	c := NewCachedToken(tr, "seed", "r1", 1000)
	if c.ExpiresAt != 1000+14400 || c.RefreshToken != "r2" || c.SeedRefreshToken != "seed" {
		t.Fatalf("cached %+v", c)
	}
	if !c.Usable("seed", 1000) {
		t.Error("fresh token not usable")
	}
	if c.Usable("seed", 1000+14400-30) {
		t.Error("token inside the 60s safety margin treated as usable")
	}
	if c.Usable("other-seed", 1000) {
		t.Error("token minted from a different seed treated as usable after merchant re-consent")
	}
	// No rotated token in the response: keep the one we just used.
	c = NewCachedToken(TokenResponse{AccessToken: "a", ExpiresIn: 10}, "seed", "r1", 0)
	if c.RefreshToken != "r1" {
		t.Errorf("RefreshToken = %q, want r1 kept", c.RefreshToken)
	}
}

// --- Obligations ---

// Reconstructed from HMRC's VAT (MTD) API v1.0 "Retrieve VAT obligations"
// docs. NEEDS SANDBOX VERIFICATION.
const obligationsFixture = `{"obligations":[
 {"periodKey":"26A1","start":"2026-07-01","end":"2026-09-30","due":"2026-11-07","status":"O"},
 {"periodKey":"26A2","start":"2026-10-01","end":"2026-12-31","due":"2027-02-07","status":"O"}
]}`

func TestParseObligations(t *testing.T) {
	obs, err := ParseObligations([]byte(obligationsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 || obs[0].PeriodKey != "26A1" || obs[1].End != "2026-12-31" || obs[0].Status != "O" {
		t.Fatalf("parsed %+v", obs)
	}
	if _, err := ParseObligations([]byte(`nope`)); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestMatchObligation_Exact(t *testing.T) {
	obs, _ := ParseObligations([]byte(obligationsFixture))
	o, err := MatchObligation(obs, "2026-10-01", "2026-12-31")
	if err != nil || o.PeriodKey != "26A2" {
		t.Fatalf("got %+v, %v", o, err)
	}
}

// Never guess the nearest period: a range that only overlaps, or sits
// inside, an open obligation is refused, and the error lists the open
// periods so the merchant can pick the right range.
func TestMatchObligation_RefusesNonExactByName(t *testing.T) {
	obs, _ := ParseObligations([]byte(obligationsFixture))
	for _, r := range [][2]string{
		{"2026-07-01", "2026-07-31"}, // inside 26A1 — a partial return
		{"2026-06-01", "2026-09-30"}, // overhangs
		{"2027-01-01", "2027-03-31"}, // no such open period
	} {
		_, err := MatchObligation(obs, r[0], r[1])
		if err == nil {
			t.Errorf("%v: matched, want refusal", r)
			continue
		}
		if !strings.Contains(err.Error(), "2026-07-01..2026-09-30") || !strings.Contains(err.Error(), "26A1") {
			t.Errorf("%v: error %q does not list the open periods", r, err)
		}
	}
	if _, err := MatchObligation(nil, "2026-07-01", "2026-09-30"); err == nil || !strings.Contains(err.Error(), "no open") {
		t.Errorf("no obligations: %v", err)
	}
	// A fulfilled ("F") obligation is never re-submitted.
	f := []Obligation{{PeriodKey: "26A1", Start: "2026-07-01", End: "2026-09-30", Status: "F"}}
	if _, err := MatchObligation(f, "2026-07-01", "2026-09-30"); err == nil {
		t.Error("fulfilled obligation matched")
	}
}

// --- URLs + headers ---

func TestURLs(t *testing.T) {
	if got := ObligationsURL(SandboxBase, "123456789"); got != "https://test-api.service.hmrc.gov.uk/organisations/vat/123456789/obligations?status=O" {
		t.Errorf("obligations url %s", got)
	}
	if got := ReturnsURL(SandboxBase, "123456789"); got != "https://test-api.service.hmrc.gov.uk/organisations/vat/123456789/returns" {
		t.Errorf("returns url %s", got)
	}
	if got := TokenURL(SandboxBase); got != "https://test-api.service.hmrc.gov.uk/oauth/token" {
		t.Errorf("token url %s", got)
	}
}

func TestAPIHeaders(t *testing.T) {
	h := APIHeaders("tok", false)
	want := map[string]string{
		"Accept":                  "application/vnd.hmrc.1.0+json",
		"Authorization":           "Bearer tok",
		"Gov-Vendor-Version":      "ut-plugin-tax-uk=" + VendorVersion,
		"Gov-Vendor-Product-Name": "Universal%20Till",
	}
	if len(h) != len(want) {
		t.Fatalf("headers %v, want exactly %v", h, want)
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("%s = %q, want %q", k, h[k], v)
		}
	}
	if APIHeaders("tok", true)["Content-Type"] != "application/json" {
		t.Error("POST headers missing Content-Type: application/json")
	}
	// Never fabricated: no device/network Gov-Client-* header is sent.
	for k := range APIHeaders("tok", true) {
		if strings.HasPrefix(k, "Gov-Client-") {
			t.Errorf("fabricated fraud-prevention header %s", k)
		}
	}
}

// VendorVersion is sent to HMRC as Gov-Vendor-Version, so it must track the
// version the marketplace actually ships.
func TestVendorVersionMatchesManifest(t *testing.T) {
	b, err := os.ReadFile("../../manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != VendorVersion {
		t.Fatalf("VendorVersion = %q, manifest.json version = %q — bump both together", VendorVersion, m.Version)
	}
}

// --- Responses ---

// Reconstructed from HMRC's "Submit VAT return for period" 201 response.
// NEEDS SANDBOX VERIFICATION.
const submitResponseFixture = `{"processingDate":"2026-10-02T10:15:00.000Z","paymentIndicator":"BANK","formBundleNumber":"256660290587","chargeRefNumber":"aCxFaNx0FZsCvyWF"}`

func TestParseSubmitResponse(t *testing.T) {
	r := ParseSubmitResponse([]byte(submitResponseFixture))
	if r.FormBundleNumber != "256660290587" || r.ProcessingDate != "2026-10-02T10:15:00.000Z" || r.ChargeRefNumber != "aCxFaNx0FZsCvyWF" {
		t.Fatalf("parsed %+v", r)
	}
}

func TestDescribeHMRCError(t *testing.T) {
	got := DescribeHMRCError(403, []byte(`{"code":"DUPLICATE_SUBMISSION","message":"The VAT return was already submitted for the given period."}`))
	if got != "HTTP 403 DUPLICATE_SUBMISSION: The VAT return was already submitted for the given period." {
		t.Errorf("got %q", got)
	}
	if got := DescribeHMRCError(502, []byte(`<html>`)); got != "HTTP 502" {
		t.Errorf("got %q", got)
	}
}
