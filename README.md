# UK VAT: Eat-in / Takeaway — Universal Till plugin

A WASM (`GOOS=wasip1 GOARCH=wasm`) plugin implementing the UK's VAT
eat-in/takeaway rate switch (HMRC VAT Notice 709/1): food a shop sells
zero-rated when it's taken away becomes standard-rated the moment it's
consumed on the premises. Built per
[`ut-docs` ADR-0025](https://github.com/universaltill/ut-docs/blob/main/adr/0025-country-tax-and-fiscal-compliance.md)
and its follow-up refactor
(`universal-till`'s `docs/code-reviews/2026-07-28-tax-rate-plugin-hook-refactor.md`),
which moved every country's VAT-switching rule out of core and into plugins
answering a generic `tax.rate.ask` hook. It also answers
`charge.policy.ask` with the UK's service-charge/tip defaults
([`ut-docs` ADR-0061](https://github.com/universaltill/ut-docs/blob/main/adr/0061-service-charge-tax-and-tip-recipient-country-policy.md)).
This is the second such plugin
(after `ut-plugin-tax-de`) and deliberately much simpler: **no TSE, no
DSFinV-K** — the UK has no cash-register signing mandate, so VAT-rate
switching is pure local business logic with no network access.

Since `ut-docs#1475` (v1.2.0) the plugin also carries one, separate,
network feature: a **sandbox-only Making Tax Digital (MTD) VAT return
bridge** — an `export` entry that computes Box 1 and Box 6 from the till's
archived day-closes and submits the return server-to-server to HMRC's
**test** API. It lives only on the manual Data/Export path, never on
checkout, so no sale ever waits on it (ADR-0003). See
[MTD VAT return bridge](#mtd-vat-return-bridge-sandbox-only) below — read
its status table before relying on it for anything.

## What this does, precisely

Real UK law (VAT Notice 709/1), not invented:
- Most **cold food** a shop sells is **zero-rated** when the customer takes
  it away, but the same item becomes **standard-rated** if eaten on the
  premises. Classic example: a sandwich.
- **Hot food/drink** is a separate rule — near-universally standard-rated
  regardless of eat-in or takeaway. This plugin does **not** switch hot
  items; a merchant just assigns them a standard-rate tax code directly in
  the catalog, no plugin involvement needed (same as any item this plugin
  has no opinion on — it silently declines and the till uses the item's own
  rate).

This plugin only handles the *switch*. It answers `tax.rate.ask`:
- `order_type == "takeaway"` → declines (no override). The item's own tax
  code — typically zero-rated — already applies.
- `order_type == ""` (eat-in/dine-in) → looks up the item's `tax_code_id` in
  the `eatin_standard_rate_by_tax_code` setting; if present, answers with
  that standard rate. If not present (the tax code isn't in the map — e.g.
  it's a hot item or something else entirely), declines, same as takeaway.

### Service-charge and tip policy (`charge.policy.ask`)

The plugin also answers core's whole-store `charge.policy.ask` hook (ADR-0061
Decision 1, `ut-docs#975`) with the UK's researched row from `ut-docs#961` —
a constant, offline answer (`src/chargepolicy/`):

- `service_charge_permitted: true` — a discretionary service charge is
  lawful and common in UK hospitality.
- `service_charge_default_rate_bp: 1250` — 12.5%, the UK market norm, as an
  informational *suggested* rate only. Whether a service charge is switched
  on at all, and the rate actually charged, stay the merchant's own till
  settings; core never applies this default.
- `service_charge_tax_basis_bp: 0` — "apportion", not "no tax": a service
  charge is further consideration for the supply (VAT Notice 709/1 §2.3),
  so its VAT is apportioned across the sale's own per-line rates.
- `tip_default_recipient: "employee"` — the Employment (Allocation of Tips)
  Act 2023 requires tips and service charges to reach workers in full.
- `fiscal_business_case: ""` — unused; the UK has no DSFinV-K-style export
  needing a business-case mapping (unlike `ut-plugin-tax-de`).
- No `charges` key: the UK has no additive statutory levy (ADR-0062).

## MTD VAT return bridge (sandbox only)

Export entry `mtd-vat-return-uk` (label key `tax_uk.entry_mtd_vat_label`,
entity `eod_closes`). Run it from **Data → Export** with a from/to range
that is **exactly** one open VAT period. The plugin then:

1. Validates every `hmrc_*` setting below — any empty or malformed one
   refuses the export **by name**, before any network call.
2. Computes the return from the archived day-closes (Z-reports) the till
   sends — never re-queried from live sales, so the return always agrees
   with the Z-reports the merchant holds (`src/hmrcvat`):
   - **Box 1** `vatDueSales` = sum of the closes' cross-tab `tax`
     (returns are already negative cells; vouchers and tips are already
     excluded by core).
   - **Box 6** `totalValueSalesExVAT` = sum of cross-tab `net`, whole
     pounds, pence left out (truncated toward zero).
   - A close that **had sales but carries no cross-tab** (an archive row
     from before the cross-tab existed) is refused by Z-number rather than
     silently counted as zero.
   - **Boxes 2, 4, 7, 8, 9** = the merchant's settings, verbatim. A till
     has no purchase ledger, so these are **never computed**.
   - **Box 3** `totalVatDue` = Box 1 + Box 2; **Box 5** `netVatDue` =
     |Box 3 − Box 4| — always derived, never typed in.
   - Pence → HMRC decimal: integer arithmetic only (`12345` → `123.45`),
     pinned by `TestAmount_MinorUnitsToHMRCDecimal`.
3. Gets an access token with HMRC's **refresh-token grant**
   (`POST {hmrc_api_base}/oauth/token`), cached in plugin storage
   (`hmrc_token`). HMRC rotates refresh tokens on every use, so the rotated
   one is stored and used next time; pasting a new `hmrc_refresh_token`
   restarts the chain from that value. If HMRC answers `401` to a cached
   access token (really expired, or revoked), the plugin refreshes once and
   retries that one read-only call — see "Known gaps" for why the cache's
   own expiry timestamp cannot be trusted.
4. `GET /organisations/vat/{vrn}/obligations?status=O` and picks the open
   obligation whose `start`/`end` equal the requested range **exactly**. No
   match → refuses and lists the open periods; it never picks the "nearest"
   period or files a partial one.
5. `POST /organisations/vat/{vrn}/returns` with `finalised: true`, and
   reports HMRC's `formBundleNumber`/`processingDate` (or HMRC's own error
   code, e.g. `DUPLICATE_SUBMISSION`) back to the Export page.

Every VAT API call carries `Accept: application/vnd.hmrc.1.0+json`, the
bearer token, and the static Fraud Prevention Headers `Gov-Vendor-Version`
(`ut-plugin-tax-uk=<version>`) and `Gov-Vendor-Product-Name`
(`Universal%20Till`). See "Known gaps" for the headers it does **not** send.

### Status

| Claim | Status |
|---|---|
| Box 1/3/5/6 arithmetic and the pence → pounds conversion | **Confirmed by unit tests** (`src/hmrcvat`), incl. a refund cell, a repayment period (Box 5 absolute) and Box 6 pence truncation. Whether "sum of the day-close cross-tab" is the right Box 1/6 source for a given business (e.g. flat-rate or cash-accounting schemes, which change what Box 1/6 mean) is **not verified** — accountant question. |
| HMRC endpoint paths, `Accept` header, body field names, refresh-token grant, obligations/receipt/error response shapes | **Researched, not tested.** Taken from HMRC's public Developer Hub docs (VAT (MTD) API v1.0, Authorisation). Never run against the real HMRC sandbox. Every fixture is flagged `NEEDS SANDBOX VERIFICATION` in `src/hmrcvat/client_test.go`. |
| Boxes 6–9 as whole-pound JSON integers | **Researched, not tested** (VAT Notice 700/12 "leave out the pence"; sandbox acceptance of `404` vs `404.00` unconfirmed). |
| Call sequence, exact headers and bodies, stdout, token caching/rotation, refusals | **Confirmed against a stubbed HTTP layer**: `src/wasmrun/mtd_test.go` runs the REAL compiled `plugin.wasm` under wazero and pins token → obligations → returns, every header, both bodies, the stored token, the one-shot re-authentication on a `401` to a cached token, and the exact `exportResponse` stdout; plus refusal cases (missing setting, no closes, `eod_closes` not granted, non-matching period, HMRC 403, transport failure, foreign entry key). No real network call anywhere in CI. |
| `universal-till`'s real `http_request`/`storage_*` host functions and a real installed-plugin export through the till UI | **Not verified.** Same line `ut-plugin-tax-de` draws: the plugin half and the HTTP contract are each only checked separately. |
| Fraud Prevention Headers | **Placeholder / incomplete.** Only `Gov-Vendor-Version` and `Gov-Vendor-Product-Name` are sent. All `Gov-Client-*` device/network headers are omitted (see Known gaps). Fine for sandbox, **not lawful for production**. |
| Production submission | **Not supported, deliberately.** `hmrc_api_base` = `https://api.service.hmrc.gov.uk` is refused by the plugin, and the manifest grants only `net:test-api.service.hmrc.gov.uk`. |
| Satisfies MTD's "digital links" requirement for a given merchant | **Not verified** — a legal/accountant question about the merchant's whole bookkeeping chain, not something this plugin can answer by existing. |

### Configure (plugin settings)

None of these has a guessed default; the export refuses by name if one is
empty. Enter `0` explicitly for a box that is nil for the period.

- `hmrc_client_id`, `hmrc_client_secret` — the merchant's (or vendor's)
  HMRC Developer Hub application credentials.
- `hmrc_refresh_token` — from HMRC's browser consent, run once
  **out-of-band** (this plugin cannot run it — see below).
- `hmrc_vrn` — 9-digit VAT registration number, no `GB` prefix.
- `hmrc_api_base` — default `https://test-api.service.hmrc.gov.uk`
  (sandbox). Production is refused.
- **Per-period figures — update before every submission:**
  `hmrc_box2_vat_due_acquisitions`, `hmrc_box4_vat_reclaimed` (pounds and
  pence, e.g. `150.25`); `hmrc_box7_purchases_ex_vat`,
  `hmrc_box8_goods_supplied_ex_vat`, `hmrc_box9_acquisitions_ex_vat`
  (whole pounds). Raw plugin settings — no dedicated per-period form,
  same as `eatin_standard_rate_by_tax_code`.

### Known gaps

- **`Gov-Client-*` Fraud Prevention Headers are not sent.** HMRC makes
  them mandatory by law for every production MTD call (`Gov-Client-
  Connection-Method`, `-Device-ID`, `-Local-IPs`, `-Timezone`, `-Screens`,
  `-User-Agent`, …). A sandboxed WASM plugin (ADR-0001) has no host
  function that exposes real device/network metadata — only `log_write`,
  `settings_get`, `http_request`, `storage_get`, `storage_set` exist — so
  these are omitted rather than fabricated, and every export logs that it
  omitted them. Fixing this needs a new device/network-metadata host
  function in `universal-till` (a host-ABI change affecting every plugin),
  tracked as its own follow-up, not this plugin.
- **No interactive OAuth consent.** HMRC's authorization-code grant is a
  browser redirect; a WASM till plugin has no browser. A self-service
  "Connect to HMRC" flow most likely belongs in the cloud/`my.` surface.
- **Boxes 2/4/7/8/9 are manual** and are not reset between periods — a
  stale value from last quarter would be re-submitted if not updated.
- **Unclosed days are not detected.** The plugin refuses when there are
  no archived closes at all, and refuses a close that had sales but no
  VAT cross-tab, but does not check that the closes cover every trading
  day of the period — a day that was never closed is simply missing from
  Box 1/6.
- **The plugin cannot read the real time.** `universal-till` runs plugins
  under wazero's default fake wall clock (no `WithSysWalltime` in
  `internal/plugins/wasm_runtime.go`), so `time.Now()` inside the plugin is
  ~2022-01-01 on every run. The cached access token's `expires_at` is
  therefore only advisory and never expires on its own; the real check is
  HMRC's `401`, on which the plugin re-authenticates once (pinned in
  `src/wasmrun/mtd_test.go`). A host clock function (or `WithSysWalltime`
  in core) would make the cache honest — a core follow-up, not this plugin.

### What a human still needs to do before this could ever go live

1. **Run HMRC's browser OAuth consent once, out-of-band**, for the
   merchant's VRN (scopes `read:vat write:vat`), and paste the resulting
   refresh token into `hmrc_refresh_token`. Refresh tokens expire (HMRC
   documents 18 months) and must then be re-consented.
2. **Run the bridge against the real HMRC sandbox** with a sandbox test
   organisation and replace every `NEEDS SANDBOX VERIFICATION` fixture
   with a captured real response.
3. **Close the Fraud Prevention Header gap** (the host-function follow-up
   above) and validate the headers with HMRC's Test Fraud Prevention
   Headers API.
4. **Get HMRC production approval** — HMRC reviews real Fraud Prevention
   Header evidence from a working integration before granting production
   credentials. Only then add `net:api.service.hmrc.gov.uk` and lift the
   production refusal.
5. **Get an accountant to confirm** the Box 1/6 sourcing for the
   merchant's VAT scheme, and how Boxes 2/4/7/8/9 are sourced each period.

## In-till documentation

This plugin ships a `docs` page entry (`ut-docs` ADR-0037 — plugin docs
ride the existing plugin-page mechanism, `content/index.html`), so
`/plugins` shows a **Docs** button for it once installed. The button opens
the same explanation as this README's "What this does" / "Configuring"
sections, rendered inside the till — works with the network down, no
separate viewer. Keep `content/index.html` in sync with this README
whenever the switching logic or the settings key changes.

## Configure (plugin settings)

- `eatin_standard_rate_by_tax_code` — a JSON object, `tax_code_id` →
  standard-rate basis points, e.g. `{"tax_zero": 2000}` maps a shop's
  zero-rated tax code to 20% for eat-in. Edited as raw JSON — there's no
  dedicated form for this yet (a merchant needs to know their own tax
  code's ID). Same pre-existing gap as `ut-plugin-tax-de`'s
  `takeaway_rate_overrides` setting — a real catalog-integrated settings UI
  is a follow-up, not built here.

## Verified

Built (`scripts/build.sh`) and run through a real wazero runtime (the same
engine `universal-till` uses) with a minimal host-function stub, covering
all three cases: takeaway declines, eat-in with a configured override
answers the standard rate, eat-in with no override configured declines.
Since `ut-docs#975` that run is a **committed test suite**, `src/wasmrun/`
(`go test ./...` runs it in CI): it compiles the current source to wasm,
executes it under wazero with stubbed `log_write`/`settings_get`, and pins
the exact stdout for `charge.policy.ask` (decoded strictly against a
mirror of core's `chargePolicyAskResponse`), the eat-in and takeaway
`tax.rate.ask` cases, an unhandled event answering nothing, and that
`manifest.json` declares the `charge.policy.ask` hook. Proven to bite:
renaming the `main.go` case, changing `GB()`'s rate, or dropping the
manifest hook each fail it. Since `ut-docs#1475` it also stubs
`http_request`/`storage_get`/`storage_set` and pins the MTD VAT export
end to end against a scripted HTTP layer (`src/wasmrun/mtd_test.go`; see
the MTD status table), including the one-shot re-authentication when HMRC
rejects a cached access token. Not proven: `universal-till`'s real
host-function implementations or a real installed-plugin run through the
till UI. `scripts/validate.sh` passes. Not yet installed via a real marketplace
flow — first installed directly (DB + filesystem, bypassing marketplace
signing) for local dev testing, matching how this codebase's own tests
install plugins.

## What a human still needs to do before this could ever go live

1. Build a settings UI so a merchant can actually pick which of their own
   tax codes get the eat-in override, instead of hand-editing JSON.
2. Legal review — this plugin encodes a simplified reading of VAT Notice
   709/1 (the cold-food-only case); real UK VAT has more edge cases
   (mixed hot/cold combos, catering, delivery) not modeled here.
3. Publish through the real marketplace pipeline (`scripts/publish.sh` +
   `scripts/approve.sh`) once there's a marketplace listing for it — so far
   this has only been installed locally for testing.

## Build

```sh
bash scripts/build.sh   # -> bin/plugin.wasm (GOOS=wasip1 GOARCH=wasm)
```

```sh
go test ./...           # src/chargepolicy + src/hmrcvat (host) + src/wasmrun (builds and runs the real wasm)
```

`src/main.go` is gated `//go:build wasip1`, so a plain host `go build ./...`
skips it — the build check for the module is `scripts/build.sh`, and the
only test that exercises `main.go`'s dispatch is `src/wasmrun` (which builds
its own wasm from the current source; `wazero` is a test-only dependency,
nothing in `bin/plugin.wasm` imports it). CI runs `GOOS=wasip1 GOARCH=wasm
go vet ./...`, a `gofmt -l .` check, `scripts/guard-plugin-i18n.sh`
(locale-key drift + every key-shaped manifest label resolves in
`locales/en.json`), `go test ./...`, then build/validate/package.
`scripts/package.sh` ships `locales/` in the release artifact.
