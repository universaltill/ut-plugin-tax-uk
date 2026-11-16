package wasmrun_test

// ADR-0172 §2.4 / §6 (ut-docs#6289): this manifest must stay eligible for
// the till's tax-plugin auto-install. The same rules the till applies to a
// verified manifest, checked by hand here so a regression fails in this
// repo's CI rather than at a customer's setup.

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// firstTillWithMarkets is the first universal-till release carrying
// ut-docs#3176 (manifest provides/markets): git tag --contains the merge of
// PR #1540 gives v0.30.11. An older till drops the unknown fields and the
// signature check fails closed.
const firstTillWithMarkets = "0.30.11"

func TestManifestEligibleForTaxAutoInstall(t *testing.T) {
	b, err := os.ReadFile("../../manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID            string   `json:"id"`
		MinPOSVersion string   `json:"min_pos_version"`
		Markets       []string `json:"markets"`
		Provides      []string `json:"provides"`
		Permissions   []string `json:"permissions"`
		Entries       []struct {
			Key string `json:"key"`
		} `json:"entries"`
		Settings []struct {
			Key string `json:"key"`
		} `json:"settings"`
		Hooks []struct {
			Event string `json:"event"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Markets) != 1 || m.Markets[0] != "GB" {
		t.Errorf("markets = %v, want [GB] (an empty markets is never auto-installed, ADR-0172 §2.1)", m.Markets)
	}
	if m.MinPOSVersion != firstTillWithMarkets {
		t.Errorf("min_pos_version = %q, want %q (first till with ADR-0129 markets)", m.MinPOSVersion, firstTillWithMarkets)
	}
	if !strings.HasPrefix(m.ID, "com.universaltill.") {
		t.Errorf("id %q lacks the reserved com.universaltill. prefix", m.ID)
	}
	allow := map[string]bool{"events:receive": true, "sales:read": true, "storage": true}
	for _, p := range m.Permissions {
		if !allow[p] {
			t.Errorf("permission %q is not on the auto-install allow-list (events:receive, sales:read, storage)", p)
		}
	}
	for _, h := range m.Hooks {
		if strings.HasPrefix(h.Event, "fiscal.") {
			t.Errorf("hook %q is fiscal.*", h.Event)
		}
	}
	for _, p := range m.Provides {
		if strings.HasPrefix(p, "fiscal.") {
			t.Errorf("provides %q is fiscal.*", p)
		}
	}
	// The MTD export moved to ut-plugin-export-mtd-uk (ADR-0172 §6).
	for _, e := range m.Entries {
		if e.Key == "mtd-vat-return-uk" {
			t.Error("mtd-vat-return-uk entry must live in ut-plugin-export-mtd-uk")
		}
	}
	for _, s := range m.Settings {
		if strings.HasPrefix(s.Key, "hmrc_") {
			t.Errorf("setting %q belongs to ut-plugin-export-mtd-uk", s.Key)
		}
	}
}

// The binary must match the manifest: no net: permission means the module
// must not even import the host's http_request function (ADR-0172 §2.4).
func TestCompiledModuleImportsNoNetworkHostFunction(t *testing.T) {
	wasm := buildWasm(t)
	if bytes.Contains(wasm, []byte("http_request")) {
		t.Error("plugin.wasm imports http_request, but the manifest holds no net: permission")
	}
}
