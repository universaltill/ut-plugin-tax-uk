#!/usr/bin/env bash
# Validates the tax-plugin manifest: marketplace-required fields
# (id/name/semver/permissions/locales), wasm runtime, canonical_type "tax"
# (ADR-0025), and one entries[] item of type="tax". Since ut-docs#1475 the
# plugin also has one "export" entry (the sandbox-only MTD VAT return
# bridge), so the only outbound host it may request is HMRC's sandbox —
# production stays refused until HMRC approval (see README).
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
net = sorted(p for p in m.get("permissions", []) if p.startswith("net:"))
if net != ["net:test-api.service.hmrc.gov.uk"]:
    errs.append(f"the only net: permission allowed is net:test-api.service.hmrc.gov.uk (MTD sandbox; production needs HMRC approval first), got {net}")
mtd = [e for e in m.get("entries", []) if e.get("key") == "mtd-vat-return-uk"]
if len(mtd) != 1 or mtd[0].get("type") != "export" or mtd[0].get("entities") != ["eod_closes"]:
    errs.append("expected exactly one export entry mtd-vat-return-uk declaring entities ['eod_closes']")
for p in ("sales:read", "storage"):
    if p not in m.get("permissions", []):
        errs.append(f"missing permission {p} (MTD export: eod_closes needs sales:read, token cache needs storage)")
if "export.requested.ask" not in [h.get("event") for h in m.get("hooks", [])]:
    errs.append("hooks[] must declare export.requested.ask for the MTD export entry")
# A map/list-typed setting must declare a real JSON object/list default, not
# a JSON string that merely contains one ("{}"): core's install path
# json.Marshal()s default_value, so a string-wrapped "{}" is stored
# double-encoded and the plugin later reads a string, not a map — this broke
# ut-plugin-tax-de's takeaway_rate_overrides in production (ut-docs#1255,
# ut-docs#1270). Plain-string defaults ("", URLs) don't parse to a dict/list.
for s in m.get("settings", []):
    dv = s.get("default_value")
    if isinstance(dv, str):
        try:
            parsed = json.loads(dv)
        except ValueError:
            continue
        if isinstance(parsed, (dict, list)):
            errs.append(f"setting {s.get('key')}'s default_value is a JSON-string-wrapped object/list ({dv!r}) instead of a real JSON object/list — double-encodes at plugin-install time (ut-docs#1255, ut-docs#1270)")
defaults = {s.get("key"): s.get("default_value") for s in m.get("settings", [])}
for k in ("hmrc_client_id","hmrc_client_secret","hmrc_refresh_token","hmrc_vrn","hmrc_box2_vat_due_acquisitions","hmrc_box4_vat_reclaimed","hmrc_box7_purchases_ex_vat","hmrc_box8_goods_supplied_ex_vat","hmrc_box9_acquisitions_ex_vat"):
    if k not in defaults: errs.append(f"missing setting {k}")
    elif defaults[k] != "": errs.append(f"setting {k} must have an empty default (never guessed)")
if defaults.get("hmrc_api_base") != "https://test-api.service.hmrc.gov.uk":
    errs.append("hmrc_api_base must default to the HMRC sandbox")
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
