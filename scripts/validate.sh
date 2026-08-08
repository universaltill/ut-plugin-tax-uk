#!/usr/bin/env bash
# Validates the tax-plugin manifest: marketplace-required fields
# (id/name/semver/permissions/locales), wasm runtime, canonical_type "tax"
# (ADR-0025), and one entries[] item of type="tax". Unlike ut-plugin-tax-de,
# no "export" entry is required — the UK has no DSFinV-K-equivalent mandate.
set -euo pipefail
cd "$(dirname "$0")/.."
python3 - <<'PY'
import json, os, re, sys
m = json.load(open("manifest.json"))
errs = []
if not re.match(r'^[a-z0-9]+([.-][a-z0-9]+)*$', m.get("id","")): errs.append("bad id")
if not m.get("name"): errs.append("missing name")
if not re.match(r'^\d+\.\d+\.\d+', m.get("version","")): errs.append("bad version")
if not m.get("permissions"): errs.append("missing permissions")
if not m.get("locales"): errs.append("missing locales")
if m.get("runtime") not in ("none", "wasm"): errs.append("runtime must be 'none' or 'wasm' (ADR-0001)")
if m.get("runtime") == "wasm":
    ep = (m.get("entrypoint") or "").lstrip("./")
    if not ep.endswith(".wasm"): errs.append("wasm runtime needs a .wasm entrypoint")
    elif not os.path.isfile(ep): errs.append(f"module not found: {ep} (run scripts/build.sh)")
if m.get("device_arch") != "any": errs.append("device_arch must be 'any'")
if m.get("canonical_type") != "tax": errs.append("canonical_type must be 'tax' (ADR-0025)")
if m.get("countries") != ["GB"]: errs.append("countries must be ['GB'] (ADR-0025 decision 3 — forward-looking metadata field, not yet enforced by the marketplace schema)")
types = [e.get("type") for e in m.get("entries", [])]
if "tax" not in types: errs.append("expected an entries[] item with type=tax")
if "net:" in " ".join(m.get("permissions", [])): errs.append("this plugin makes no outbound calls — no net: permission should be requested")
# A declared "docs" page entry (ADR-0037) must ship real content, or the
# Docs button opens the "registered a page but ships no page content" stub
# — the broken affordance ut-docs#406's AC3 explicitly forbids.
for e in m.get("entries", []):
    if e.get("type") == "page" and e.get("key") == "docs":
        has_bundle = os.path.isdir("content") and any(f.endswith(".json") for f in os.listdir("content"))
        has_static = os.path.isfile("content/index.html")
        if not (has_bundle or has_static):
            errs.append("entries[] declares a docs page but content/index.html or content/<locale>.json is missing")
if errs:
    print("FAIL: " + "; ".join(errs)); sys.exit(1)
print(f"ok {m['id']} v{m['version']}")
PY
