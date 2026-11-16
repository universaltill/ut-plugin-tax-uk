#!/usr/bin/env bash
# Validates the tax-plugin manifest: marketplace-required fields
# (id/name/semver/permissions/locales), wasm runtime, canonical_type "tax"
# (ADR-0025), and one entries[] item of type="tax". This plugin is
# auto-installed on a new UK till (ADR-0172), so its manifest must stay
# eligible (section 2.4): markets ["GB"], min_pos_version at least 0.30.11
# (first till with manifest markets, ut-docs#3176), and permissions limited
# to the allow-list events:receive, sales:read, storage. No net: host, no
# fiscal.* hook. The MTD export lives in ut-plugin-export-mtd-uk (ut-docs#6289).
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
if m.get("markets") != ["GB"]: errs.append("markets must be ['GB']: an empty markets is never auto-installed (ADR-0172 section 2.1)")
mm = re.match(r'^(\d+)\.(\d+)\.(\d+)$', m.get("min_pos_version") or "")
if not mm or tuple(int(x) for x in mm.groups()) < (0, 30, 11):
    errs.append("min_pos_version must be at least 0.30.11, the first till release carrying manifest markets (ut-docs#3176)")
if not m.get("id", "").startswith("com.universaltill."): errs.append("id must carry the reserved com.universaltill. prefix (ADR-0172 section 2.2)")
allow = {"events:receive", "sales:read", "storage"}
extra = sorted(set(m.get("permissions", [])) - allow)
if extra:
    errs.append(f"permissions outside the ADR-0172 auto-install allow-list {sorted(allow)}: {extra} (a net: host belongs in a separate plugin, e.g. ut-plugin-export-mtd-uk)")
for h in m.get("hooks", []):
    if str(h.get("event", "")).startswith("fiscal."): errs.append(f"fiscal hook {h.get('event')} makes the plugin ineligible for auto-install")
for p in m.get("provides", []) or []:
    if str(p).startswith("fiscal."): errs.append(f"provides {p} makes the plugin ineligible for auto-install")
if any(e.get("key") == "mtd-vat-return-uk" for e in m.get("entries", [])):
    errs.append("the MTD export lives in ut-plugin-export-mtd-uk (ut-docs#6289)")
fig = [e for e in m.get("entries", []) if e.get("key") == "vat-figures-uk"]
if len(fig) != 1 or fig[0].get("type") != "export" or fig[0].get("entities") != ["eod_closes"]:
    errs.append("expected exactly one export entry vat-figures-uk declaring entities ['eod_closes'] (accountant CSV, ut-docs#5052)")
for p in ("sales:read", "storage"):
    if p not in m.get("permissions", []):
        errs.append(f"missing permission {p} (figures export: eod_closes needs sales:read)")
if "export.requested.ask" not in [h.get("event") for h in m.get("hooks", [])]:
    errs.append("hooks[] must declare export.requested.ask for the vat-figures-uk export entry")
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
