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
// VAT rate switching is pure local business logic: no TSE/fiskaly and no
// network. This plugin holds no net: permission, which is what lets a new UK
// till install it automatically (ADR-0172 section 6). The sandbox HMRC MTD
// VAT return bridge that used to live here (ut-docs#1475) moved to
// ut-plugin-export-mtd-uk (ut-docs#6289). What stays on the manual Data/Export
// path is "vat-figures-uk", the accountant's CSV, computed locally.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"

	"github.com/universaltill/ut-plugin-tax-uk/src/chargepolicy"
	"github.com/universaltill/ut-plugin-tax-uk/src/vatfigures"
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

// vatFiguresEntryKey is the accountant export (ut-docs#5052): the same
// Box 1/6 figures as a CSV, no HMRC connection, no setting.
const vatFiguresEntryKey = "vat-figures-uk"

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

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
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
	logf("tax-uk: vat export: %s", msg)
	fmt.Print(string(mustJSON(exportResponse{OK: false, Error: msg})))
	os.Exit(0)
}

// handleVATFiguresExport answers export.requested.ask for vat-figures-uk:
// pure local computation from the archived day-closes, returned inline as a
// CSV (content_b64). No network call and no settings are read, so it works
// for every UK shop without HMRC production access.
func handleVATFiguresExport(from, to string, closes []vatfigures.EODCloseExport) {
	if closes == nil {
		exportFail("the till sent no eod_closes for this export — the plugin needs the sales:read permission granted (and the entry must declare the eod_closes entity); check the plugin's permissions in Settings")
	}
	f, err := vatfigures.BuildFigures(closes, from, to)
	if err != nil {
		exportFail(err.Error())
	}
	logf("tax-uk: vat figures export built from=%s to=%s closes=%d", from, to, len(f.Closes))
	fmt.Print(string(mustJSON(struct {
		OK         bool   `json:"ok"`
		Filename   string `json:"filename"`
		ContentB64 string `json:"content_b64"`
	}{true, f.Filename(), base64.StdEncoding.EncodeToString(f.CSV())})))
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
			From      string                      `json:"from"`
			To        string                      `json:"to"`
			EntryKey  string                      `json:"entry_key"`
			EODCloses []vatfigures.EODCloseExport `json:"eod_closes"` // ut-docs#1005: "[]" = supported, none in range; null/absent = not granted/declared
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
		case vatFiguresEntryKey:
			handleVATFiguresExport(payload.From, payload.To, payload.EODCloses)
		default:
			logf("tax-uk: export.requested.ask for entry_key=%q, not ours (%q) — declining", payload.EntryKey, vatFiguresEntryKey)
			os.Exit(0)
		}

	default:
		logf("tax-uk: unhandled event type %q", ev.Type)
		os.Exit(0)
	}
}
