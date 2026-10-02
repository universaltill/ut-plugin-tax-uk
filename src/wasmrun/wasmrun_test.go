// Package wasmrun_test runs the REAL compiled plugin through a real wazero
// runtime — the same engine `universal-till` uses
// (internal/plugins/wasm_runtime.go) — with a minimal stand-in for the two
// host functions this plugin imports (`ut.log_write`, `ut.settings_get`).
//
// Why this exists (ut-docs#975 review): `src/main.go` is `GOOS=wasip1`-only,
// so neither `go test ./...` nor a host `go vet` ever sees its event
// dispatch. `src/chargepolicy` pins the JSON `GB()` produces, but nothing
// else proved that `main.go` actually routes `charge.policy.ask` to it and
// writes that JSON to stdout — deleting the `case` would have left every
// other test green. Same harness shape as `ut-plugin-tax-de`'s
// `src/wasmrun/` (ut-docs#818, #974), cut down to this plugin's two host
// functions.
//
// WHAT THIS PROVES: the compiled module dispatches on the event's `type`,
// reads settings through the host ABI, and writes exactly the stdout JSON
// core parses (`internal/pages/charge_hook.go`'s chargePolicyAskResponse,
// `internal/pages/tax_hook.go`'s rate answer).
//
// WHAT THIS DOES NOT PROVE: `universal-till`'s real host-function
// implementations, or a real installed-plugin flow through the till UI.
package wasmrun_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Host ABI (ut-docs reference/plugin-host-functions.md, mirrored by every
// sibling plugin): data calls return the FULL length of the value — the
// guest retries with a bigger buffer if that exceeds its cap. Negative
// returns are host errors.
const hostErrNotFound = -1

// stubHost is the fake till: settings plus captured log lines.
type stubHost struct {
	mu       sync.Mutex
	settings map[string]string
	logs     []string
}

func writeOut(mem api.Memory, dstPtr, dstCap uint32, val []byte) int32 {
	if uint32(len(val)) <= dstCap {
		mem.Write(dstPtr, val)
	}
	return int32(len(val)) // full length either way, per the ABI
}

func readStr(mem api.Memory, ptr, n uint32) string {
	b, _ := mem.Read(ptr, n)
	return string(b)
}

func (h *stubHost) register(ctx context.Context, r wazero.Runtime) error {
	_, err := r.NewHostModuleBuilder("ut").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n uint32) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.logs = append(h.logs, readStr(m.Memory(), ptr, n))
	}).Export("log_write").
		NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kPtr, kLen, dstPtr, dstCap uint32) int32 {
		h.mu.Lock()
		defer h.mu.Unlock()
		v, ok := h.settings[readStr(m.Memory(), kPtr, kLen)]
		if !ok {
			return hostErrNotFound
		}
		return writeOut(m.Memory(), dstPtr, dstCap, []byte(v))
	}).Export("settings_get").
		Instantiate(ctx)
	return err
}

// buildWasm compiles the CURRENT source to wasm, so this test can never
// pass against a stale committed artefact.
func buildWasm(t *testing.T) []byte {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	// Register every guest source as an input of THIS test, so `go test`'s
	// result cache invalidates when they change. Load-bearing, not
	// defensive: the wasm is built by a subprocess, and the go command's
	// cache only tracks files the test process itself opens — without
	// these reads a cached PASS survives an edit that deletes the dispatch
	// (ut-plugin-tax-de hit exactly that, 2026-08-19).
	srcDir := filepath.Join(root, "src")
	err = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		_, readErr := os.ReadFile(path)
		return readErr
	})
	if err != nil {
		t.Fatalf("registering guest sources as cache inputs: %v", err)
	}

	out := filepath.Join(t.TempDir(), "plugin.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./src")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building wasm: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read wasm: %v", err)
	}
	return b
}

// run executes the compiled plugin with event JSON on stdin and returns
// stdout plus the host stub (for asserting what it did).
func run(t *testing.T, wasm []byte, h *stubHost, event string) (string, *stubHost) {
	t.Helper()
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		t.Fatalf("wasi: %v", err)
	}
	if err := h.register(ctx, r); err != nil {
		t.Fatalf("host module: %v", err)
	}

	var stdout, stderr strings.Builder
	cfg := wazero.NewModuleConfig().
		WithStdin(strings.NewReader(event)).
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithStartFunctions("_start")

	// The guest calls os.Exit(0); wazero surfaces that as a sys.ExitError
	// with code 0, which is a normal, successful finish for this plugin.
	if _, err := r.InstantiateWithConfig(ctx, wasm, cfg); err != nil {
		if !strings.Contains(err.Error(), "exit_code(0)") {
			t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
		}
	}
	return stdout.String(), h
}

// coreChargePolicyAskResponse mirrors universal-till
// internal/pages/charge_hook.go's chargePolicyAskResponse — the struct core
// decodes this plugin's stdout into. Decoded with DisallowUnknownFields so
// a renamed/extra key here fails instead of silently reading as the zero
// value on core's side.
type coreChargePolicyAskResponse struct {
	ServiceChargePermitted     *bool           `json:"service_charge_permitted"`
	ServiceChargeDefaultRateBP int             `json:"service_charge_default_rate_bp"`
	ServiceChargeTaxBasisBP    int             `json:"service_charge_tax_basis_bp"`
	TipDefaultRecipient        string          `json:"tip_default_recipient"`
	FiscalBusinessCase         string          `json:"fiscal_business_case"`
	Charges                    json.RawMessage `json:"charges"`
}

// TestChargePolicyAsk_AnswersUKPolicy drives the REAL compiled plugin with
// core's charge.policy.ask event (ADR-0061; its payload is deliberately
// empty — a whole-store ask) and asserts the answer core will parse: service
// charge permitted, 12.5% suggested, taxed at the sale's own per-line rates
// (basis 0 = apportion, VAT Notice 709/1 §2.3), tips default to the
// employee (Employment (Allocation of Tips) Act 2023), no fiscal business
// case, no additive charges.
func TestChargePolicyAsk_AnswersUKPolicy(t *testing.T) {
	wasm := buildWasm(t)
	h := &stubHost{settings: map[string]string{}}
	out, _ := run(t, wasm, h, `{"type":"charge.policy.ask","payload":{}}`)

	// Byte-exact first: this is the same literal src/chargepolicy's
	// TestGB_WireShape pins, now proven to reach stdout through main.go.
	const want = `{"service_charge_permitted":true,"service_charge_default_rate_bp":1250,"service_charge_tax_basis_bp":0,"tip_default_recipient":"employee","fiscal_business_case":""}`
	if strings.TrimSpace(out) != want {
		t.Fatalf("charge.policy.ask stdout drifted\n got: %q\nwant: %q\nlogs: %v", out, want, h.logs)
	}

	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(out)))
	dec.DisallowUnknownFields()
	var got coreChargePolicyAskResponse
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not core's chargePolicyAskResponse shape: %v\nstdout: %q", err, out)
	}
	if got.ServiceChargePermitted == nil || !*got.ServiceChargePermitted {
		t.Errorf("service_charge_permitted = %v, want explicit true", got.ServiceChargePermitted)
	}
	if got.ServiceChargeDefaultRateBP != 1250 {
		t.Errorf("service_charge_default_rate_bp = %d, want 1250 (12.5%%, the UK market norm)", got.ServiceChargeDefaultRateBP)
	}
	if got.ServiceChargeTaxBasisBP != 0 {
		t.Errorf("service_charge_tax_basis_bp = %d, want 0 (apportion at the lines' own rates — a flat rate would mis-tax a mixed zero-rated/standard-rated bill)", got.ServiceChargeTaxBasisBP)
	}
	if got.TipDefaultRecipient != "employee" {
		t.Errorf("tip_default_recipient = %q, want employee", got.TipDefaultRecipient)
	}
	if got.FiscalBusinessCase != "" {
		t.Errorf("fiscal_business_case = %q, want empty (no UK fiscal export needs one)", got.FiscalBusinessCase)
	}
	if len(got.Charges) != 0 {
		t.Errorf("charges = %s, want absent (core applies each item verbatim to every sale)", got.Charges)
	}
}

// TestManifestSubscribesChargePolicyAsk: core only dispatches an event to a
// plugin whose manifest.json hooks[] declares it, so a handler in main.go
// without this entry is dead code that every other test here would still
// pass.
func TestManifestSubscribesChargePolicyAsk(t *testing.T) {
	b, err := os.ReadFile("../../manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Hooks []struct {
			Event string `json:"event"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, h := range m.Hooks {
		if h.Event == "charge.policy.ask" {
			return
		}
	}
	t.Fatal("manifest.json hooks[] does not declare charge.policy.ask — core would never ask this plugin")
}

// TestTaxRateAsk_EatInConfiguredCodeAnswersStandardRate commits the
// eat-in case README's "Verified" section previously described only as an
// ad-hoc, uncommitted wazero run: the compiled plugin reads
// eatin_standard_rate_by_tax_code through the real host settings_get call
// and answers {"rate_bp":N} for a configured tax code.
func TestTaxRateAsk_EatInConfiguredCodeAnswersStandardRate(t *testing.T) {
	wasm := buildWasm(t)
	h := &stubHost{settings: map[string]string{
		"eatin_standard_rate_by_tax_code": `{"tax_zero":2000}`,
	}}
	const ask = `{"type":"tax.rate.ask","payload":{"item_id":"item-sandwich","tax_code_id":"tax_zero","tax_rate_bp":0,"order_type":""}}`
	out, _ := run(t, wasm, h, ask)
	var got struct {
		RateBP int `json:"rate_bp"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("stdout not the {\"rate_bp\":N} shape tax.rate.ask requires: %v\nstdout: %q\nlogs: %v", err, out, h.logs)
	}
	if got.RateBP != 2000 {
		t.Fatalf("rate_bp = %d, want 2000 (the configured eat-in standard rate)", got.RateBP)
	}
}

// TestTaxRateAsk_TakeawayAnswersNothing pins the takeaway short-circuit at
// the compiled-plugin level: no override is ever consulted, stdout stays
// empty, and the till keeps the item's own (zero) rate.
func TestTaxRateAsk_TakeawayAnswersNothing(t *testing.T) {
	wasm := buildWasm(t)
	h := &stubHost{settings: map[string]string{
		"eatin_standard_rate_by_tax_code": `{"tax_zero":2000}`,
	}}
	const ask = `{"type":"tax.rate.ask","payload":{"item_id":"item-sandwich","tax_code_id":"tax_zero","tax_rate_bp":0,"order_type":"takeaway"}}`
	out, _ := run(t, wasm, h, ask)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("takeaway produced an answer, want no opinion (empty stdout): %q", out)
	}
}

// TestUnknownEventAnswersNothing: an event this plugin does not handle must
// produce no stdout at all (an empty answer is "no opinion" on core's side;
// any bytes would be parsed as one).
func TestUnknownEventAnswersNothing(t *testing.T) {
	wasm := buildWasm(t)
	h := &stubHost{settings: map[string]string{}}
	out, _ := run(t, wasm, h, `{"type":"sale.completed","payload":{}}`)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("unhandled event produced stdout, want none: %q", out)
	}
}
