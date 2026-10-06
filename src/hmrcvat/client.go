// Package hmrcvat is the host-function-free half of the MTD VAT return
// bridge (ut-docs#1475): HMRC's VAT (MTD) API v1.0 request/response shapes,
// the nine-box return builder, settings validation and the OAuth
// refresh-token bookkeeping. Everything here is pure, so it is unit-tested
// on the host — src/main.go is wasip1-only and does nothing but move these
// values through the ut.http_request / ut.storage_* host functions (same
// split as ut-plugin-tax-de's src/fiskalyparse, src/fiscalsign, src/datev).
//
// STATUS — read before relying on this for anything real:
//   - Endpoint paths, the Accept header, the nine box field names and the
//     refresh-token grant are taken from HMRC's public Developer Hub docs
//     (VAT (MTD) API v1.0, "Authorisation"), researched, NOT run against
//     the HMRC sandbox. Every captured-shape fixture in client_test.go is
//     marked NEEDS SANDBOX VERIFICATION, same convention ut-plugin-tax-de
//     used for fiskaly until its 2026-08-18 live run.
//   - Boxes 2, 4, 7, 8 and 9 are NEVER computed here: a till has no purchase
//     ledger. They come from merchant settings verbatim.
//   - Fraud Prevention Headers: only the static Gov-Vendor-* headers are
//     sent. The Gov-Client-* device/network headers are mandatory by law in
//     production but a WASM guest has no host function to read real device
//     metadata, so they are omitted rather than fabricated (README "Known
//     gaps").
package hmrcvat

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	// SandboxBase is HMRC's test API host — the only host this plugin's
	// manifest grants (net:test-api.service.hmrc.gov.uk) and the default of
	// the hmrc_api_base setting.
	SandboxBase = "https://test-api.service.hmrc.gov.uk"
	// ProductionBase is HMRC's live API. Refused by Settings.Validate:
	// production submission needs HMRC's own approval (incl. a review of
	// real Fraud Prevention Header evidence) and is out of scope for
	// ut-docs#1475. Named here only so the refusal is explicit.
	ProductionBase = "https://api.service.hmrc.gov.uk"

	// AcceptHeader selects VAT (MTD) API v1.0.
	AcceptHeader = "application/vnd.hmrc.1.0+json"

	// VendorProductName / VendorVersion feed the static Gov-Vendor-*
	// Fraud Prevention Headers. VendorVersion must equal manifest.json's
	// version (pinned by TestVendorVersionMatchesManifest).
	VendorProductName = "Universal Till"
	VendorVersion     = "1.2.1"
	vendorSoftwareKey = "ut-plugin-tax-uk"

	// TokenStorageKey is the plugin-storage key the access token and the
	// rotated refresh token are cached under (mirrors ut-plugin-tax-de's
	// "fiskaly_token").
	TokenStorageKey = "hmrc_token"

	// tokenSafetyMarginSeconds: a cached access token this close to expiry
	// is refreshed rather than risked mid-sequence.
	tokenSafetyMarginSeconds = 60
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

var poundsRe = regexp.MustCompile(`^-?[0-9]+(\.[0-9]{1,2})?$`)

// parsePence parses a merchant-entered pounds value ("150", "150.5",
// "150.25", "-4.50") into pence. Anything else — thousands separators, a
// currency symbol, three decimal places, exponent notation — is refused
// rather than guessed at.
func parsePence(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if !poundsRe.MatchString(s) {
		return 0, fmt.Errorf("%q is not a pounds amount like 150.25", s)
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	p := w*100 + f
	if neg {
		p = -p
	}
	return p, nil
}

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
	// Gross is the close's own headline gross (core's EODReport.Gross). Read
	// only to detect a close that HAD sales but carries no cross-tab (an
	// archive row from before ut-docs#1004 added method_tax_bands) — such a
	// close would otherwise contribute 0 to Box 1/6 and silently
	// under-report the period, so BuildSubmitRequest refuses it by Z-number.
	Gross int64 `json:"gross"`
	// MethodTaxBands is the payment-method x VAT-rate cross-tab
	// (ut-docs#1004). Returns are already signed negative in it, and
	// voucher issuance (a liability) and tips (held out of revenue) are
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

// --- settings ---

// Settings is the raw plugin settings the MTD export reads. Every field is
// required; none has a guessed default (hmrc_api_base defaults to the
// sandbox in manifest.json, which is "stay in test", never "assume live").
type Settings struct {
	ClientID       string // hmrc_client_id
	ClientSecret   string // hmrc_client_secret
	RefreshToken   string // hmrc_refresh_token (from HMRC's browser consent, run out-of-band)
	VRN            string // hmrc_vrn
	APIBase        string // hmrc_api_base
	Box2VATDueAcq  string // hmrc_box2_vat_due_acquisitions (pounds.pence)
	Box4VATReclaim string // hmrc_box4_vat_reclaimed (pounds.pence)
	Box7Purchases  string // hmrc_box7_purchases_ex_vat (whole pounds)
	Box8GoodsSupp  string // hmrc_box8_goods_supplied_ex_vat (whole pounds)
	Box9Acq        string // hmrc_box9_acquisitions_ex_vat (whole pounds)
}

// ManualBoxes are the five boxes a till cannot truthfully compute — always
// the merchant's (or their accountant's) figures for the period.
type ManualBoxes struct {
	VATDueAcquisitions           Amount      // Box 2
	VATReclaimedCurrPeriod       Amount      // Box 4
	TotalValuePurchasesExVAT     WholePounds // Box 7
	TotalValueGoodsSuppliedExVAT WholePounds // Box 8
	TotalAcquisitionsExVAT       WholePounds // Box 9
}

var vrnRe = regexp.MustCompile(`^[0-9]{9}$`)

// Validate refuses — naming every offending setting — rather than guessing
// any value. It returns the parsed manual boxes and the normalised API base.
func (s Settings) Validate() (ManualBoxes, string, error) {
	required := []struct{ name, val string }{
		{"hmrc_client_id", s.ClientID},
		{"hmrc_client_secret", s.ClientSecret},
		{"hmrc_refresh_token", s.RefreshToken},
		{"hmrc_vrn", s.VRN},
		{"hmrc_api_base", s.APIBase},
		{"hmrc_box2_vat_due_acquisitions", s.Box2VATDueAcq},
		{"hmrc_box4_vat_reclaimed", s.Box4VATReclaim},
		{"hmrc_box7_purchases_ex_vat", s.Box7Purchases},
		{"hmrc_box8_goods_supplied_ex_vat", s.Box8GoodsSupp},
		{"hmrc_box9_acquisitions_ex_vat", s.Box9Acq},
	}
	var missing []string
	for _, r := range required {
		if strings.TrimSpace(r.val) == "" {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return ManualBoxes{}, "", fmt.Errorf("MTD VAT return not submitted: required setting(s) empty: %s (Boxes 2/4/7/8/9 are never computed by the till — enter 0 explicitly if a box is nil for this period)", strings.Join(missing, ", "))
	}

	if !vrnRe.MatchString(strings.TrimSpace(s.VRN)) {
		return ManualBoxes{}, "", fmt.Errorf("hmrc_vrn must be the 9-digit VAT registration number without a GB prefix, got %q", s.VRN)
	}
	base := strings.TrimRight(strings.TrimSpace(s.APIBase), "/")
	if !strings.HasPrefix(base, "https://") {
		return ManualBoxes{}, "", fmt.Errorf("hmrc_api_base must be an https:// URL, got %q", s.APIBase)
	}
	if base == ProductionBase {
		return ManualBoxes{}, "", fmt.Errorf("hmrc_api_base points at HMRC production (%s) — this plugin is sandbox-only until HMRC production approval (see README); use %s", ProductionBase, SandboxBase)
	}

	var m ManualBoxes
	pence := func(name, v string) (int64, error) {
		p, err := parsePence(v)
		if err != nil {
			return 0, fmt.Errorf("%s: %v", name, err)
		}
		return p, nil
	}
	whole := func(name, v string) (WholePounds, error) {
		p, err := pence(name, v)
		if err != nil {
			return 0, err
		}
		if p%100 != 0 {
			return 0, fmt.Errorf("%s: HMRC takes this box in whole pounds (no pence), got %q", name, v)
		}
		return WholePounds(p / 100), nil
	}
	var err error
	var p int64
	if p, err = pence("hmrc_box2_vat_due_acquisitions", s.Box2VATDueAcq); err != nil {
		return ManualBoxes{}, "", err
	}
	m.VATDueAcquisitions = Amount(p)
	if p, err = pence("hmrc_box4_vat_reclaimed", s.Box4VATReclaim); err != nil {
		return ManualBoxes{}, "", err
	}
	m.VATReclaimedCurrPeriod = Amount(p)
	if m.TotalValuePurchasesExVAT, err = whole("hmrc_box7_purchases_ex_vat", s.Box7Purchases); err != nil {
		return ManualBoxes{}, "", err
	}
	if m.TotalValueGoodsSuppliedExVAT, err = whole("hmrc_box8_goods_supplied_ex_vat", s.Box8GoodsSupp); err != nil {
		return ManualBoxes{}, "", err
	}
	if m.TotalAcquisitionsExVAT, err = whole("hmrc_box9_acquisitions_ex_vat", s.Box9Acq); err != nil {
		return ManualBoxes{}, "", err
	}
	return m, base, nil
}

// --- the return ---

// SubmitReturnRequest is the body of POST /organisations/vat/{vrn}/returns.
// Field order matches HMRC's docs (Box 1..9), which also makes the
// marshaled body byte-stable for the wasmrun pin.
type SubmitReturnRequest struct {
	PeriodKey                    string      `json:"periodKey"`
	VATDueSales                  Amount      `json:"vatDueSales"`                  // Box 1 — computed
	VATDueAcquisitions           Amount      `json:"vatDueAcquisitions"`           // Box 2 — merchant
	TotalVATDue                  Amount      `json:"totalVatDue"`                  // Box 3 — derived
	VATReclaimedCurrPeriod       Amount      `json:"vatReclaimedCurrPeriod"`       // Box 4 — merchant
	NetVATDue                    Amount      `json:"netVatDue"`                    // Box 5 — derived
	TotalValueSalesExVAT         WholePounds `json:"totalValueSalesExVAT"`         // Box 6 — computed
	TotalValuePurchasesExVAT     WholePounds `json:"totalValuePurchasesExVAT"`     // Box 7 — merchant
	TotalValueGoodsSuppliedExVAT WholePounds `json:"totalValueGoodsSuppliedExVAT"` // Box 8 — merchant
	TotalAcquisitionsExVAT       WholePounds `json:"totalAcquisitionsExVAT"`       // Box 9 — merchant
	Finalised                    bool        `json:"finalised"`
}

// BuildSubmitRequest computes Box 1 (VAT due on sales = sum of the closes'
// cross-tab Tax) and Box 6 (sales ex-VAT = sum of Net, pence left out) from
// the ALREADY-ARCHIVED Z-reports the host supplied — never a fresh
// recomputation, so the return can never disagree with the Z-reports the
// merchant holds. Boxes 2/4/7/8/9 are copied from manual verbatim. Boxes 3
// and 5 are always derived by HMRC's fixed arithmetic — a merchant-entered
// Box 4 can never skip it:
//
//	Box 3 totalVatDue = Box 1 + Box 2
//	Box 5 netVatDue   = |Box 3 - Box 4|
//
// PeriodKey is left empty: the caller sets it from the matched obligation.
func BuildSubmitRequest(closes []EODCloseExport, manual ManualBoxes) (SubmitReturnRequest, error) {
	if len(closes) == 0 {
		return SubmitReturnRequest{}, fmt.Errorf("MTD VAT return not submitted: no archived day-closes in the requested period — Box 1 and Box 6 are computed from archived day-close (Z-report) records only, so close every trading day in the period first")
	}
	// Validate every close before summing anything (same all-up-front
	// discipline as ut-plugin-tax-de's datev.BuildFromCloses): a close with
	// sales but no cross-tab cannot be represented, and a return silently
	// missing that day would under-report — refuse by Z-number instead.
	var problems []string
	var tax, net int64
	for _, c := range closes {
		if len(c.Report.MethodTaxBands) == 0 && c.Report.Gross != 0 {
			problems = append(problems, fmt.Sprintf("close Z%d (%s) has sales (gross £%s) but no VAT cross-tab (method_tax_bands) — archived before the cross-tab existed, so its share of Box 1/6 cannot be computed; narrow the range to closes that carry one", c.ZNumber, c.Report.Day, Amount(c.Report.Gross)))
			continue
		}
		for _, b := range c.Report.MethodTaxBands {
			tax += b.Tax
			net += b.Net
		}
	}
	if len(problems) > 0 {
		return SubmitReturnRequest{}, fmt.Errorf("MTD VAT return not submitted: %s", strings.Join(problems, "; "))
	}
	box1 := Amount(tax)
	box3 := box1 + manual.VATDueAcquisitions
	box5 := box3 - manual.VATReclaimedCurrPeriod
	if box5 < 0 {
		box5 = -box5
	}
	return SubmitReturnRequest{
		VATDueSales:                  box1,
		VATDueAcquisitions:           manual.VATDueAcquisitions,
		TotalVATDue:                  box3,
		VATReclaimedCurrPeriod:       manual.VATReclaimedCurrPeriod,
		NetVATDue:                    box5,
		TotalValueSalesExVAT:         wholePoundsFromPence(net),
		TotalValuePurchasesExVAT:     manual.TotalValuePurchasesExVAT,
		TotalValueGoodsSuppliedExVAT: manual.TotalValueGoodsSuppliedExVAT,
		TotalAcquisitionsExVAT:       manual.TotalAcquisitionsExVAT,
		Finalised:                    true,
	}, nil
}

// --- OAuth (refresh-token grant only; the browser consent is out-of-band) ---

// TokenURL is HMRC's OAuth token endpoint.
func TokenURL(base string) string { return base + "/oauth/token" }

// TokenRefreshBody is the application/x-www-form-urlencoded body of the
// refresh-token grant. url.Values.Encode sorts keys, so it is byte-stable.
func TokenRefreshBody(clientID, clientSecret, refreshToken string) []byte {
	v := url.Values{}
	v.Set("grant_type", "refresh_token")
	v.Set("client_id", clientID)
	v.Set("client_secret", clientSecret)
	v.Set("refresh_token", refreshToken)
	return []byte(v.Encode())
}

// TokenRequestHeaders are the headers of the token call (no bearer, no
// Accept versioning — it is the OAuth endpoint, not the VAT API).
func TokenRequestHeaders() map[string]string {
	return map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
}

// TokenResponse is HMRC's refresh-grant response. NEEDS SANDBOX
// VERIFICATION (reconstructed from the public Authorisation docs).
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// ParseTokenResponse refuses a body without an access token.
func ParseTokenResponse(body []byte) (TokenResponse, error) {
	var t TokenResponse
	if err := json.Unmarshal(body, &t); err != nil {
		return TokenResponse{}, fmt.Errorf("HMRC token response unparseable: %v", err)
	}
	if t.AccessToken == "" {
		return TokenResponse{}, fmt.Errorf("HMRC token response has no access_token")
	}
	return t, nil
}

// CachedToken is what is kept in plugin storage under TokenStorageKey.
//
// HMRC rotates refresh tokens: each refresh grant returns a new refresh
// token and the old one stops working. So after the first export the
// pasted hmrc_refresh_token setting is spent and the live token lives here.
// SeedRefreshToken records which pasted setting value this chain grew from:
// when the merchant pastes a NEW token (re-ran the consent), the setting
// wins and the stored chain is abandoned.
type CachedToken struct {
	AccessToken      string `json:"access_token"`
	ExpiresAt        int64  `json:"expires_at"` // unix seconds
	RefreshToken     string `json:"refresh_token"`
	SeedRefreshToken string `json:"seed_refresh_token"`
}

// RefreshTokenToUse picks the refresh token for the next grant.
func RefreshTokenToUse(settingsToken string, cached *CachedToken) string {
	if cached != nil && cached.SeedRefreshToken == settingsToken && cached.RefreshToken != "" {
		return cached.RefreshToken
	}
	return settingsToken
}

// NewCachedToken records a successful grant. used is the refresh token the
// grant was made with; it is kept if HMRC did not rotate it.
func NewCachedToken(t TokenResponse, seed, used string, now int64) CachedToken {
	rt := t.RefreshToken
	if rt == "" {
		rt = used
	}
	return CachedToken{AccessToken: t.AccessToken, ExpiresAt: now + t.ExpiresIn, RefreshToken: rt, SeedRefreshToken: seed}
}

// Usable reports whether the cached access token can be reused now for the
// given settings seed.
func (c CachedToken) Usable(seed string, now int64) bool {
	return c.AccessToken != "" && c.SeedRefreshToken == seed && now < c.ExpiresAt-tokenSafetyMarginSeconds
}

// --- VAT API ---

// ObligationsURL lists the OPEN obligations (status=O; HMRC then requires no
// from/to). vrn is validated as 9 digits by Settings.Validate, so it is
// path-safe.
func ObligationsURL(base, vrn string) string {
	return base + "/organisations/vat/" + vrn + "/obligations?status=O"
}

// ReturnsURL is the submit endpoint.
func ReturnsURL(base, vrn string) string {
	return base + "/organisations/vat/" + vrn + "/returns"
}

// APIHeaders are the headers on every VAT API call: versioned Accept,
// bearer token, and the STATIC Fraud Prevention Headers this plugin can
// truthfully send. The device/network Gov-Client-* headers are deliberately
// absent — see the package doc and README "Known gaps".
func APIHeaders(token string, jsonBody bool) map[string]string {
	h := map[string]string{
		"Accept":                  AcceptHeader,
		"Authorization":           "Bearer " + token,
		"Gov-Vendor-Version":      url.QueryEscape(vendorSoftwareKey) + "=" + url.QueryEscape(VendorVersion),
		"Gov-Vendor-Product-Name": url.PathEscape(VendorProductName),
	}
	if jsonBody {
		h["Content-Type"] = "application/json"
	}
	return h
}

// Obligation is one VAT return period. NEEDS SANDBOX VERIFICATION.
type Obligation struct {
	PeriodKey string `json:"periodKey"`
	Start     string `json:"start"`
	End       string `json:"end"`
	Due       string `json:"due"`
	Status    string `json:"status"`
	Received  string `json:"received,omitempty"`
}

// ParseObligations parses the obligations response.
func ParseObligations(body []byte) ([]Obligation, error) {
	var r struct {
		Obligations []Obligation `json:"obligations"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("HMRC obligations response unparseable: %v", err)
	}
	return r.Obligations, nil
}

// MatchObligation picks the OPEN obligation whose period is exactly
// [from, to]. Exact, not "overlapping" or "nearest": submitting a range
// that only covers part of a period would file a return that under-reports
// it, and a return cannot simply be re-filed. On no match it refuses and
// lists the open periods so the merchant can re-run with the right range.
func MatchObligation(obs []Obligation, from, to string) (Obligation, error) {
	var open []string
	for _, o := range obs {
		if o.Status != "O" {
			continue
		}
		if o.Start == from && o.End == to {
			return o, nil
		}
		open = append(open, fmt.Sprintf("%s..%s (periodKey %s, due %s)", o.Start, o.End, o.PeriodKey, o.Due))
	}
	if len(open) == 0 {
		return Obligation{}, fmt.Errorf("MTD VAT return not submitted: HMRC reports no open VAT obligation for this VRN")
	}
	return Obligation{}, fmt.Errorf("MTD VAT return not submitted: the requested range %s..%s is not exactly an open VAT period — export one of: %s", from, to, strings.Join(open, "; "))
}

// SubmitResponse is HMRC's 201 receipt. NEEDS SANDBOX VERIFICATION.
type SubmitResponse struct {
	ProcessingDate   string `json:"processingDate"`
	PaymentIndicator string `json:"paymentIndicator"`
	FormBundleNumber string `json:"formBundleNumber"`
	ChargeRefNumber  string `json:"chargeRefNumber"`
}

// ParseSubmitResponse is best-effort: a 2xx means HMRC accepted the return
// whether or not the receipt body parses, so this never turns an accepted
// submission into a reported failure (a retry would be a duplicate).
func ParseSubmitResponse(body []byte) SubmitResponse {
	var r SubmitResponse
	_ = json.Unmarshal(body, &r)
	return r
}

// DescribeHMRCError renders a non-2xx response as "HTTP <status> <code>:
// <message>" from HMRC's standard error body, or just the status.
func DescribeHMRCError(status int, body []byte) string {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil && e.Code != "" {
		return fmt.Sprintf("HTTP %d %s: %s", status, e.Code, e.Message)
	}
	return fmt.Sprintf("HTTP %d", status)
}
