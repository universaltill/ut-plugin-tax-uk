//go:build wasip1

// UK VAT eat-in/takeaway rate switch (VAT Notice 709/1). A WASI command
// (GOOS=wasip1 GOARCH=wasm) the till runs in-process, per ut-docs ADR-0025
// ("Country-specific tax rates and fiscal compliance") and its follow-up
// refactor: universal-till/docs/code-reviews/2026-07-28-tax-rate-plugin-hook-
// refactor.md, which moved ALL country-specific VAT-switching logic out of
// core and into plugins answering the generic "tax.rate.ask" hook.
//
// Real UK law, not invented: most cold food a shop sells is zero-rated VAT
// when the customer takes it away, but the SAME item becomes standard-rated
// the moment it's consumed on the premises ("eating in") — HMRC VAT Notice
// 709/1. Hot food/drink is a separate rule (near-universally standard-rated
// regardless of eat-in/takeaway) and is NOT switched by this plugin — a
// merchant's hot items should just carry a standard-rate tax code directly,
// no override needed, same as any item this plugin has no opinion on.
//
// VAT rate switching is still pure local business logic (no TSE/fiskaly,
// no network). Since ut-docs#1475 the plugin ALSO carries one network
// feature, kept entirely on the manual Data/Export path (never a checkout
// hook, so no sale ever waits on it — ADR-0003): the "mtd-vat-return-uk"
// export entry, a sandbox-only Making Tax Digital VAT return bridge to
// HMRC (see handleMTDVATExport and src/hmrcvat). That is why the manifest
// now holds net:test-api.service.hmrc.gov.uk, storage and sales:read.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/universaltill/ut-plugin-tax-uk/src/chargepolicy"
	"github.com/universaltill/ut-plugin-tax-uk/src/hmrcvat"
)

// --- host functions (module "ut", see ut-docs reference/plugin-host-functions.md) ---
// Buffer ABI: data-returning calls write min(len, dstCap) bytes into dst and
// return the FULL length; a guest seeing len > cap retries with a bigger
// buffer. Negative returns are host errors: -1 not found, -2 denied,
// -3 internal, -4 invalid.

//go:wasmimport ut log_write
func logWrite(ptr, n uint32)

//go:wasmimport ut settings_get
func settingsGet(kPtr, kLen, dstPtr, dstCap uint32) int32

// http_request / storage_get / storage_set (ut-docs#1475): same ABI as
// ut-plugin-tax-de's imports. Each one needs a matching stub in
// src/wasmrun or the module will not instantiate there.

//go:wasmimport ut http_request
func httpRequest(rPtr, rLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut storage_get
func storageGet(kPtr, kLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut storage_set
func storageSet(kPtr, kLen, vPtr, vLen uint32) int32

// mtdVATReturnEntryKey must match manifest.json's "export" entries[].key.
// The host resolves export.requested.ask by plugin id, so this plugin
// (declaring exactly one export entry) is already only ever asked on its
// own behalf — this check is defense-in-depth for the day this plugin
// ships a second export entry with a different key, not a fix for
// cross-plugin routing (that's the host's job, see universal-till's
// EventBus.AskPlugin). Same reasoning as ut-plugin-tax-de's
// dsfinvkExportEntryKey.
const mtdVATReturnEntryKey = "mtd-vat-return-uk"

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func logf(format string, args ...any) {
	msg := []byte(fmt.Sprintf(format, args...))
	p, n := ptrOf(msg)
	logWrite(p, n)
}

// callBuf runs a data-returning host call, honoring the buffer ABI (grow +
// retry once if the first buffer was too small). Same pattern as every
// sibling plugin.
func callBuf(fn func(dstPtr, dstCap uint32) int32) ([]byte, int32) {
	buf := make([]byte, 8192)
	p, c := ptrOf(buf)
	n := fn(p, c)
	if n < 0 {
		return nil, n
	}
	if int(n) > len(buf) {
		buf = make([]byte, n)
		p, c = ptrOf(buf)
		n = fn(p, c)
		if n < 0 {
			return nil, n
		}
		if int(n) > len(buf) {
			n = int32(len(buf))
		}
	}
	return buf[:n], n
}

func setting(key string) string {
	kb := []byte(key)
	out, code := callBuf(func(dp, dc uint32) int32 {
		kp, kl := ptrOf(kb)
		return settingsGet(kp, kl, dp, dc)
	})
	if code < 0 {
		return ""
	}
	return string(out)
}

func storageRead(key string) ([]byte, bool) {
	kb := []byte(key)
	out, code := callBuf(func(dp, dc uint32) int32 {
		kp, kl := ptrOf(kb)
		return storageGet(kp, kl, dp, dc)
	})
	if code < 0 {
		return nil, false
	}
	return out, true
}

func storagePut(key string, v []byte) {
	kp, kl := ptrOf([]byte(key))
	vp, vl := ptrOf(v)
	if code := storageSet(kp, kl, vp, vl); code != 0 {
		logf("tax-uk: storage_set(%s) failed, code=%d", key, code)
	}
}

// httpCall performs one outbound HTTP call through the host's http_request
// function (permission-gated by `net:test-api.service.hmrc.gov.uk` in
// manifest.json — see universal-till/internal/plugins/wasm_hostfns.go
// hostHTTPRequest). ok is false only on a host/transport failure; a non-2xx
// HTTP status still returns ok=true with status/body set. Copied verbatim
// from ut-plugin-tax-de (same host contract); jsonBody is just the raw
// request body bytes, so the form-encoded OAuth body travels through it too.
func httpCall(method, url string, headers map[string]string, jsonBody []byte) (body []byte, status int, ok bool) {
	reqJSON, _ := json.Marshal(map[string]any{
		"method":   method,
		"url":      url,
		"headers":  headers,
		"body_b64": base64.StdEncoding.EncodeToString(jsonBody),
	})
	respBuf, code := callBuf(func(dp, dc uint32) int32 {
		rp, rl := ptrOf(reqJSON)
		return httpRequest(rp, rl, dp, dc)
	})
	if code < 0 {
		return nil, 0, false
	}
	var httpResp struct {
		Status  int    `json:"status"`
		BodyB64 string `json:"body_b64"`
	}
	_ = json.Unmarshal(respBuf, &httpResp)
	b, _ := base64.StdEncoding.DecodeString(httpResp.BodyB64)
	return b, httpResp.Status, true
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// --- MTD VAT return bridge (ut-docs#1475) ---

// hmrcAccessToken returns a bearer token for the VAT API, reusing the one
// cached under hmrcvat.TokenStorageKey while it looks unexpired and was
// minted from the current hmrc_refresh_token setting, otherwise (or when
// force is set) running HMRC's refresh-token grant (mirrors
// ut-plugin-tax-de's fiskalyAuth caching under "fiskaly_token"). HMRC
// rotates refresh tokens on every grant, so the new one is persisted and
// used next time — the pasted setting is single-use. The initial browser
// consent that produced that setting is NOT done here (a WASM plugin has no
// browser, ADR-0001) — see README. minted reports whether the returned
// token came from a grant made just now (so a 401 on it is final, not a
// stale-cache symptom).
//
// Guest-clock caveat (found in review, ut-docs#1475): time.Now() here is
// NOT real time. universal-till instantiates plugins with wazero's default
// ModuleConfig (no WithSysWalltime — internal/plugins/wasm_runtime.go), whose
// wall clock is a deterministic fake that starts at 2022-01-01 on EVERY run.
// So CachedToken.ExpiresAt is only an advisory hint: a cached access token
// looks "unexpired" forever, long past HMRC's real ~4-hour lifetime. The
// authoritative staleness check is HMRC's own answer — handleMTDVATExport
// treats a 401 on the first VAT call as "cached token spent", calls this
// again with force=true and retries that one idempotent GET.
func hmrcAccessToken(s hmrcvat.Settings, base string, force bool) (token string, minted bool, err error) {
	seed := strings.TrimSpace(s.RefreshToken)
	now := time.Now().Unix()
	var cached *hmrcvat.CachedToken
	if raw, ok := storageRead(hmrcvat.TokenStorageKey); ok {
		var c hmrcvat.CachedToken
		if err := json.Unmarshal(raw, &c); err == nil {
			cached = &c
			if !force && c.Usable(seed, now) {
				return c.AccessToken, false, nil
			}
		}
	}
	use := hmrcvat.RefreshTokenToUse(seed, cached)
	body, status, ok := httpCall("POST", hmrcvat.TokenURL(base), hmrcvat.TokenRequestHeaders(),
		hmrcvat.TokenRefreshBody(strings.TrimSpace(s.ClientID), strings.TrimSpace(s.ClientSecret), use))
	if !ok {
		return "", false, fmt.Errorf("HMRC token refresh failed: host could not reach %s (network or net: permission)", base)
	}
	if status < 200 || status >= 300 {
		return "", false, fmt.Errorf("HMRC token refresh rejected (%s) — the refresh token may be spent or expired; re-run HMRC's consent and paste a new hmrc_refresh_token", hmrcvat.DescribeHMRCError(status, body))
	}
	tr, err := hmrcvat.ParseTokenResponse(body)
	if err != nil {
		return "", false, err
	}
	storagePut(hmrcvat.TokenStorageKey, mustJSON(hmrcvat.NewCachedToken(tr, seed, use, now)))
	return tr.AccessToken, true, nil
}

func hmrcSettings() hmrcvat.Settings {
	return hmrcvat.Settings{
		ClientID:       setting("hmrc_client_id"),
		ClientSecret:   setting("hmrc_client_secret"),
		RefreshToken:   setting("hmrc_refresh_token"),
		VRN:            setting("hmrc_vrn"),
		APIBase:        setting("hmrc_api_base"),
		Box2VATDueAcq:  setting("hmrc_box2_vat_due_acquisitions"),
		Box4VATReclaim: setting("hmrc_box4_vat_reclaimed"),
		Box7Purchases:  setting("hmrc_box7_purchases_ex_vat"),
		Box8GoodsSupp:  setting("hmrc_box8_goods_supplied_ex_vat"),
		Box9Acq:        setting("hmrc_box9_acquisitions_ex_vat"),
	}
}

// exportResponse mirrors universal-till internal/pages/data_api.go's
// exportResponse (the struct core decodes stdout into). A struct, not a
// map, so the field order on the wire is stable.
type exportResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// exportFail answers export.requested.ask with {ok:false, error} and exits.
func exportFail(msg string) {
	logf("tax-uk: mtd vat export: %s", msg)
	fmt.Print(string(mustJSON(exportResponse{OK: false, Error: msg})))
	os.Exit(0)
}

// handleMTDVATExport answers export.requested.ask for mtd-vat-return-uk:
// refresh token -> GET open obligations -> pick the obligation that is
// EXACTLY [from, to] (refuse by name otherwise, never the nearest) ->
// hmrcvat.BuildSubmitRequest -> POST the return. Every local check
// (settings, closes, box arithmetic) runs BEFORE any network call, so a
// misconfigured export never even reaches HMRC, and nothing is ever
// submitted with an invented figure.
//
// Fraud Prevention Headers: only the static Gov-Vendor-* headers are sent.
// The Gov-Client-* device/network headers need real device metadata no
// host function exposes today; they are omitted, never fabricated, and the
// gap is logged on every export (README "Known gaps").
func handleMTDVATExport(from, to string, closes []hmrcvat.EODCloseExport) {
	if closes == nil {
		exportFail("the till sent no eod_closes for this export — the plugin needs the sales:read permission granted (and the entry must declare the eod_closes entity); check the plugin's permissions in Settings")
	}
	if from == "" || to == "" {
		exportFail("an MTD VAT return needs an explicit from/to range equal to one open VAT period — the period is never guessed")
	}
	s := hmrcSettings()
	manual, base, err := s.Validate()
	if err != nil {
		exportFail(err.Error())
	}
	req, err := hmrcvat.BuildSubmitRequest(closes, manual)
	if err != nil {
		exportFail(err.Error())
	}
	vrn := strings.TrimSpace(s.VRN)

	logf("tax-uk: mtd vat export: Gov-Client-* fraud prevention headers (Device-ID, Local-IPs, Timezone, Screens, ...) NOT sent — no host function exposes real device/network metadata to a plugin yet; only Gov-Vendor-* are sent. Acceptable for HMRC's sandbox, NOT for production (see README Known gaps)")

	token, minted, err := hmrcAccessToken(s, base, false)
	if err != nil {
		exportFail(err.Error())
	}

	body, status, ok := httpCall("GET", hmrcvat.ObligationsURL(base, vrn), hmrcvat.APIHeaders(token, false), nil)
	if ok && status == 401 && !minted {
		// The CACHED access token was rejected: really expired (the guest
		// clock cannot tell — see hmrcAccessToken) or revoked. Refresh once
		// and retry this one idempotent GET; a 401 on a freshly minted token
		// is final and falls through to the error below. The returns POST
		// is never retried this way — a stale token is caught here, before
		// anything is submitted.
		logf("tax-uk: mtd vat export: HMRC rejected the cached access token (%s) — refreshing once and retrying the obligations call", hmrcvat.DescribeHMRCError(status, body))
		if token, _, err = hmrcAccessToken(s, base, true); err != nil {
			exportFail(err.Error())
		}
		body, status, ok = httpCall("GET", hmrcvat.ObligationsURL(base, vrn), hmrcvat.APIHeaders(token, false), nil)
	}
	if !ok {
		exportFail("HMRC obligations request failed: host could not reach " + base)
	}
	if status < 200 || status >= 300 {
		exportFail("HMRC obligations request rejected: " + hmrcvat.DescribeHMRCError(status, body))
	}
	obs, err := hmrcvat.ParseObligations(body)
	if err != nil {
		exportFail(err.Error())
	}
	obl, err := hmrcvat.MatchObligation(obs, from, to)
	if err != nil {
		exportFail(err.Error())
	}
	req.PeriodKey = obl.PeriodKey

	body, status, ok = httpCall("POST", hmrcvat.ReturnsURL(base, vrn), hmrcvat.APIHeaders(token, true), mustJSON(req))
	if !ok {
		exportFail("HMRC VAT return submission failed: host could not reach " + base + " — the return was NOT confirmed as submitted; check HMRC before retrying")
	}
	if status < 200 || status >= 300 {
		exportFail("HMRC rejected the VAT return: " + hmrcvat.DescribeHMRCError(status, body))
	}
	receipt := hmrcvat.ParseSubmitResponse(body)
	msg := fmt.Sprintf("MTD VAT return %s..%s (periodKey %s) accepted by HMRC at %s: formBundleNumber %s, processingDate %s. Box 1 £%s, Box 3 £%s, Box 5 £%s, Box 6 £%d, from %d archived day-close(s).",
		from, to, obl.PeriodKey, base, receipt.FormBundleNumber, receipt.ProcessingDate,
		req.VATDueSales, req.TotalVATDue, req.NetVATDue, int64(req.TotalValueSalesExVAT), len(closes))
	logf("tax-uk: %s", msg)
	fmt.Print(string(mustJSON(exportResponse{OK: true, Message: msg})))
	os.Exit(0)
}

// taxRateAskPayload mirrors universal-till's internal/pages/tax_hook.go.
// OrderType has three wire values: "" (dine-in/eat-in, the unchanged
// default), "takeaway", and "none" (the shop has the dine-in/takeaway choice
// switched off, sale.order_type_prompt = off, ut-docs#3632 — there is no
// consumption-mode distinction, so the item's own rate must apply). Only ""
// is eat-in; anything else, including a value this plugin has never heard of,
// is treated as "not eat-in" (see handleTaxRateAsk's fail-safe).
type taxRateAskPayload struct {
	ItemID    string `json:"item_id"`
	TaxCodeID string `json:"tax_code_id"`
	TaxRateBP int    `json:"tax_rate_bp"`
	OrderType string `json:"order_type"`
}

// handleTaxRateAsk answers the "tax.rate.ask" hook. Writing valid JSON to
// stdout is the answer; writing nothing means "no opinion on this line," and
// the till falls back to the line's own configured rate — that's the
// correct outcome for takeaway (the item's own rate, typically zero-rated,
// already applies) and for any item this plugin has no override for.
//
// The mapping of WHICH tax codes switch, and to what standard rate, is
// merchant-configured via eatin_standard_rate_by_tax_code (a JSON object,
// tax_code_id -> basis points), not hardcoded: a shop's own tax-code IDs
// aren't knowable in advance, same reasoning as ut-plugin-tax-de's
// takeaway_rate_overrides setting.
//
// Fail-safe: ONLY order_type "" (eat-in) consults that setting. "takeaway",
// "none" and ANY unknown value decline (empty stdout, exit 0) without
// reading the setting, so an unrecognised value can never apply the eat-in
// uplift.
func handleTaxRateAsk(raw []byte) {
	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	_ = json.Unmarshal(raw, &wrapper)
	var ask taxRateAskPayload
	_ = json.Unmarshal(wrapper.Payload, &ask)

	if ask.OrderType != "" {
		// takeaway / none / unknown: the item's own (typically zero-rated)
		// tax code already applies; never read the eat-in setting.
		os.Exit(0)
	}

	overrides := map[string]int{}
	if raw := strings.TrimSpace(setting("eatin_standard_rate_by_tax_code")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
			logf("tax-uk: eatin_standard_rate_by_tax_code setting is not valid JSON: %v", err)
			os.Exit(0)
		}
	}

	bp, ok := overrides[ask.TaxCodeID]
	if !ok || bp <= 0 {
		os.Exit(0) // no eat-in override configured for this tax code
	}

	out, _ := json.Marshal(map[string]int{"rate_bp": bp})
	os.Stdout.Write(out)
	os.Exit(0)
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var ev struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &ev)

	switch ev.Type {
	case "tax.rate.ask":
		handleTaxRateAsk(raw)

	// ADR-0061 Decision 1 (ut-docs#975): the UK's service-charge/tip
	// policy. A constant, offline answer — the payload is deliberately
	// empty (a whole-store ask), so there is nothing to read from it.
	case "charge.policy.ask":
		out, _ := json.Marshal(chargepolicy.GB())
		os.Stdout.Write(out)
		os.Exit(0)
	// The generic export/report dispatch hook (ut-docs#189) — the host
	// (internal/pages/data_api.go) resolves entries[].key to this plugin's
	// id and asks this plugin specifically (EventBus.AskPlugin, not a
	// broadcast Ask), so entry_key here is always already ours. Checked
	// anyway (declining, not answering, on a mismatch) as defense-in-depth
	// against a future second export entry in this same plugin — same
	// switch shape as ut-plugin-tax-de.
	case "export.requested.ask":
		var payload struct {
			From      string                   `json:"from"`
			To        string                   `json:"to"`
			EntryKey  string                   `json:"entry_key"`
			EODCloses []hmrcvat.EODCloseExport `json:"eod_closes"` // ut-docs#1005: "[]" = supported, none in range; null/absent = not granted/declared
		}
		var wrapper struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			logf("tax-uk: export.requested.ask: unparseable event envelope: %v", err)
			fmt.Print(string(mustJSON(exportResponse{OK: false, Error: "malformed export request"})))
			os.Exit(0)
		}
		if err := json.Unmarshal(wrapper.Payload, &payload); err != nil {
			logf("tax-uk: export.requested.ask: unparseable payload: %v", err)
			fmt.Print(string(mustJSON(exportResponse{OK: false, Error: "malformed export request payload"})))
			os.Exit(0)
		}
		switch payload.EntryKey {
		case mtdVATReturnEntryKey:
			handleMTDVATExport(payload.From, payload.To, payload.EODCloses)
		default:
			logf("tax-uk: export.requested.ask for entry_key=%q, not ours (%q) — declining", payload.EntryKey, mtdVATReturnEntryKey)
			os.Exit(0)
		}

	default:
		logf("tax-uk: unhandled event type %q", ev.Type)
		os.Exit(0)
	}
}
