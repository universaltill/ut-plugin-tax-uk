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
// This is genuinely simpler than ut-plugin-tax-de: no TSE/fiskaly, no
// external API at all — VAT rate switching is pure local business logic, so
// this plugin needs no `net:` permission and makes no outbound calls.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"
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

// taxRateAskPayload mirrors universal-till's internal/pages/tax_hook.go.
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
func handleTaxRateAsk(raw []byte) {
	var wrapper struct {
		Payload json.RawMessage `json:"payload"`
	}
	_ = json.Unmarshal(raw, &wrapper)
	var ask taxRateAskPayload
	_ = json.Unmarshal(wrapper.Payload, &ask)

	if ask.OrderType == "takeaway" {
		os.Exit(0) // takeaway: the item's own (typically zero-rated) tax code already applies
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
	default:
		logf("tax-uk: unhandled event type %q", ev.Type)
		os.Exit(0)
	}
}
