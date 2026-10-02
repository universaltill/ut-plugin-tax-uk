# Code review — MTD VAT return bridge to HMRC's sandbox (`mtd-vat-return-uk`)

**Date:** 2026-10-02
**Card:** universaltill/ut-docs#1475
**Branch:** `feat/1475-mtd-vat-return-uk`
**Complexity:** medium
**Dev:** Opus, subagent (cloud routine lane `:41`)
**Reviewer:** Fable 5.1, one independent subagent with a fresh context
(worktree-isolated; ran the full gate itself, mutation-tested, read core's
and `ut-plugin-tax-de`'s source directly rather than trusting the brief)

## What shipped (Dev's WIP snapshot, reviewed as the diff against `main`)

- `src/hmrcvat/` (new, host-fn-free, 512 lines + 500 of tests): HMRC VAT
  (MTD) API v1.0 shapes; `Amount` (pence → `123.45` JSON number, integer
  arithmetic only) and `WholePounds` (Boxes 6–9, pence truncated toward
  zero); `Settings.Validate` (ten `hmrc_*` settings, refuses by name,
  refuses production `https://api.service.hmrc.gov.uk`, refuses a VRN that
  is not 9 digits, parses the five manual boxes with a strict pounds regex);
  `BuildSubmitRequest` (Box 1 = Σ cross-tab `tax`, Box 6 = Σ `net` in whole
  pounds, Boxes 2/4/7/8/9 verbatim from settings, Box 3 = 1+2, Box 5 =
  |3−4|, `finalised: true`); refresh-token grant body/headers; refresh-token
  rotation bookkeeping (`CachedToken` with a `SeedRefreshToken` so a newly
  pasted setting wins over the stored chain); `MatchObligation` (exact
  `start`/`end` match on open obligations only, lists the open periods on
  refusal); error/receipt parsing; static `Gov-Vendor-*` headers only.
- `src/main.go`: `http_request`/`storage_get`/`storage_set` imports and
  the `httpCall`/`storageRead`/`storagePut` helpers copied from
  `ut-plugin-tax-de`; `export.requested.ask` routed on `entry_key ==
  "mtd-vat-return-uk"` (declines otherwise); `handleMTDVATExport` runs
  every local check (closes present, range present, settings, box
  arithmetic) before any network call, then token → obligations → returns.
- `src/wasmrun/`: stubs for the three new host functions (scripted HTTP
  layer, in-memory storage) and `mtd_test.go` driving the REAL compiled
  wasm: full sequence with exact URLs/headers/bodies/stdout, cached-token
  reuse, rotated-token refresh, non-matching period, missing settings,
  `[]`/`null` closes, HMRC 403, transport failure, foreign entry key,
  manifest declaration.
- `manifest.json` 1.1.0 → 1.2.0: one `export` entry (`eod_closes`, locale
  key label), ten settings (all empty defaults except `hmrc_api_base` =
  sandbox), permissions + `sales:read`, `net:test-api.service.hmrc.gov.uk`,
  `storage`, hook `export.requested.ask`.
- `locales/en.json` + `en-GB.json` (first locale files in this repo),
  `scripts/guard-plugin-i18n.sh` (byte-identical to the ut-docs template —
  diffed), `scripts/package.sh` ships `locales/`, `scripts/validate.sh`
  now enforces: exactly one `net:` permission (the sandbox), the export
  entry shape, `sales:read`/`storage`, the hook, every credential/box
  setting defaulting to `""`, `hmrc_api_base` defaulting to the sandbox.
- `ci.yml` gains the i18n guard step; README/CLAUDE.md/`content/index.html`
  carry the DE-style status table, known gaps and "what a human still needs
  to do".

## Gate (run by the reviewer, after the fixes below)

```
$ gofmt -l .                                   (no output, exit 0)
$ go vet ./...                                 exit 0
$ GOOS=wasip1 GOARCH=wasm go vet ./...         exit 0
$ go test -count=1 ./...
ok  github.com/universaltill/ut-plugin-tax-uk/src/chargepolicy  0.002s
ok  github.com/universaltill/ut-plugin-tax-uk/src/hmrcvat       0.003s
ok  github.com/universaltill/ut-plugin-tax-uk/src/wasmrun       24.393s
$ bash scripts/build.sh                        built bin/plugin.wasm (3762187 bytes)
$ bash scripts/validate.sh                     ok com.universaltill.tax-uk v1.2.0
$ bash scripts/guard-plugin-i18n.sh
guard-plugin-i18n: 1 manifest entries[].label key(s) resolve in locales/en.json
guard-plugin-i18n: ok (1 key(s) across 2 locale file(s))
$ bash scripts/package.sh                      packaged dist/com.universaltill.tax-uk_1.2.0_universal.tar.gz
$ tar -tzf dist/com.universaltill.tax-uk_1.2.0_universal.tar.gz
manifest.json README.md bin/ bin/plugin.wasm LICENSE content/ content/index.html
locales/ locales/en.json locales/en-GB.json
$ GOTOOLCHAIN=local go mod tidy -diff          clean (exit 0)
```

The same gate was green on Dev's snapshot before any reviewer change.

## Verification beyond the automated tests

- **Box arithmetic, hand-checked against the VAT return box definitions
  (two cases, not Dev's numbers taken on trust):**
  - Fixture (two closes, 20%/5%/0% cells, one refund cell): Box 1 =
    2000+5010+0+100−200 = 6910p = £69.10; Box 6 = 10000+25050+4399+2000−1000
    = 40449p → £404 (pence left out, VAT Notice 700/12); Box 3 = 69.10+0.00;
    Box 5 = |69.10−15.00| = 54.10. Wire body pinned byte-exact in both
    `client_test.go` and the wasm run. Correct.
  - Repayment period: Box 2 = £0.90, Box 4 = £100.00 → Box 3 = £70.00, Box 5
    = |70.00−100.00| = £30.00 (HMRC's `netVatDue` is the absolute
    difference, 0.00..99999999999.99). Correct.
  - Negative Box 6 (refund-dominated period, −15099p) → −150, truncated
    toward zero, not rounded away. Correct. `Amount(-1)` → `-0.01`.
- **Obligations matching is exact-match only**: `MatchObligation` compares
  `o.Start == from && o.End == to` on `status == "O"` rows and never
  overlap/nearest; core sends `from`/`to` as `YYYY-MM-DD`
  (`internal/pages/data_api.go:533-538`), the same format as HMRC's
  obligation dates, so an exact match is actually reachable in production.
  A partial, overhanging, non-existent or fulfilled (`F`) period is
  refused, naming the open periods.
- **Settings defaults** (`manifest.json` + `validate.sh` enforcement): nine
  credential/box settings default to `""`; only `hmrc_api_base` has a
  default and it is the sandbox host. `Settings.Validate` additionally
  refuses `https://api.service.hmrc.gov.uk` and the manifest grants only
  `net:test-api.service.hmrc.gov.uk` (both pinned by tests and the
  validator).
- **No partial-data submission path**: `null` closes → refuses naming
  `sales:read`; `[]` closes → refuses ("close every trading day"); any
  empty/malformed setting → refuses by name; no exact obligation → refuses
  listing the open periods; all before the returns POST. Plus the new
  refusal in finding 2.
- **Secrets / placeholders**: no literal credential anywhere (grepped the
  tree for `secret|token|client_id|password` followed by a 16+ char value:
  nothing). Fixtures use `client-abc`, `secret-xyz`, `seed-refresh`,
  `cid`/`csecret`/`rtok`, VRN `123456789`; the receipt fixture reuses
  HMRC's own public doc example. No secret reaches stdout or `log_write`
  (error strings carry HMRC's `code`/`message`, the API base and the VRN
  only).
- **Filesystem bug classes (MkdirAll / cwd-relative paths)**: N/A — the
  guest touches no filesystem; the only file I/O is the wasmrun harness
  reading sources and writing to `t.TempDir()`.
- **i18n**: `tax_uk.entry_mtd_vat_label` resolves in both `locales/en.json`
  and `locales/en-GB.json`; core's `syncLocales`
  (`internal/plugins/plugins.go:65-106`) loads every `locales/*.json` of an
  active plugin regardless of the manifest's `locales` array, so both
  overlays reach `T`. Re-ran `scripts/package.sh` and listed the tarball —
  `locales/` is in it (above).
- **Fraud Prevention Headers**: only `Gov-Vendor-Version`
  (`ut-plugin-tax-uk=1.2.0`, pinned to the manifest version by
  `TestVendorVersionMatchesManifest`) and `Gov-Vendor-Product-Name`
  (`Universal%20Till`) are sent; no `Gov-Client-*` value is fabricated, and
  the omission is logged on every export. Per the Architect's brief this is
  the intended scope, not a defect.
- **Host ABI contract vs core**, read directly: request/response envelope
  (`method`/`url`/`headers`/`body_b64` → `status`/`body_b64`) matches
  `internal/plugins/wasm_hostfns.go:250-332`; `storage_get` not-found = −1
  matches; `exportResponse` fields match `internal/pages/data_api.go:115`
  (decoded strictly with `DisallowUnknownFields` in the harness);
  `eod_closes` `[]`-vs-`null` semantics match `data_api.go:90-97`.

### Mutation tests (reviewer's own, different logic from Tester's)

Tester had already broken the obligations exact-match check. I broke two
other things, restored each byte-identical, and re-ran green:

1. **Box 5 computed from Box 1 instead of Box 3** (`box5 := box1 - Box4`,
   i.e. a merchant-entered Box 2 silently skipped):
   ```
   --- FAIL: TestBuildSubmitRequest_RepaymentPeriodBox5IsAbsolute (0.00s)
       client_test.go:259: Box 5 = 3090, want 3000
   ```
2. **A fabricated `Gov-Client-Device-ID` header added to `APIHeaders`:**
   ```
   --- FAIL: TestAPIHeaders (0.00s)
       client_test.go:443: headers map[... Gov-Client-Device-ID:beec798b-... ], want exactly map[...]
   --- FAIL: TestMTDVATExport_FullSequence (1.77s)
       mtd_test.go:151: obligations headers = map[... Gov-Client-Device-ID:... ], want exactly map[...]
       mtd_test.go:166: returns headers = map[... Gov-Client-Device-ID:... ], want exactly map[...]
   ```
   Caught both on the host and through the real compiled module.
3. **My own fix's test, with the 401 retry disabled** (`status == 999`):
   ```
   --- FAIL: TestMTDVATExport_RejectedCachedTokenRefreshedOnce (1.60s)
       mtd_test.go:277: call sequence
            got: ["GET /organisations/vat/123456789/obligations?status=O"]
           want: ["GET .../obligations?status=O" "POST /oauth/token" "GET .../obligations?status=O" "POST .../returns"]
   ```
   — which is exactly the pre-fix production behaviour (one 401, hard stop).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | **The access-token cache can never expire in the real till.** `hmrcAccessToken` trusts `time.Now()` for `CachedToken.ExpiresAt`/`Usable`, but `universal-till` instantiates plugins with `wazero.NewModuleConfig()` and never calls `WithSysWalltime()` (`internal/plugins/wasm_runtime.go:662`), so the guest's wall clock is wazero's deterministic fake (`FakeEpochNanos` = 2022-01-01, +1 ms per read, verified in the wazero source in the module cache). `now` is ~1640995200 on every run, `ExpiresAt` = that + 14400, so a cached token looks fresh forever; after HMRC's real ~4 h lifetime every export would fail `HTTP 401 INVALID_CREDENTIALS` on the obligations call with no recovery except pasting a new refresh token (which changes the seed). Invisible to the test-suite because the harness uses the same default fake clock. | **Fixed** (`src/main.go` `hmrcAccessToken(s, base, force) (token, minted, err)` + the 401 branch in `handleMTDVATExport`): a 401 on the first, idempotent VAT GET with a *cached* token forces one refresh (with the rotated refresh token) and retries that GET once; a 401 on a freshly minted token is final. The returns POST is never retried. Pinned by two new wasm cases (`TestMTDVATExport_RejectedCachedTokenRefreshedOnce`, `TestMTDVATExport_FreshTokenRejectedDoesNotLoop`); documented in README "Known gaps", CLAUDE.md and the code. |
| 2 | minor | **A close with sales but no cross-tab counted as zero.** `BuildSubmitRequest` summed `method_tax_bands` only; an archived close from before ut-docs#1004 (cross-tab absent) with `gross != 0` would contribute 0 to Box 1/6 and the return would under-report the period silently — the "partially-missing data" class the card says must refuse by name. (DE's DATEV builder has the same blind spot, noted below.) | **Fixed** (`src/hmrcvat/client.go`): `EODReportForExport` now reads core's `gross`; a close with `len(MethodTaxBands)==0 && Gross != 0` is refused naming `Z<n> (<day>)`; a zero-sales close with no cross-tab is still accepted. Unit-tested (`TestBuildSubmitRequest_RefusesCloseWithSalesButNoCrossTab`); README updated. |
| 3 | nit (accepted) | `hmrc_api_base` accepts any `https://` host other than HMRC production (e.g. a typo'd host) | Accepted: the manifest grants exactly one `net:` host and `validate.sh` pins it, so the host denies any other egress at dial time; refusing production explicitly is the point of the check. |
| 4 | nit (accepted) | Boxes 2/4/7/8/9 persist across periods (a stale Box 4 would be re-submitted) | Already an explicit non-goal (per-period settings UI) and already an honest README "Known gaps" bullet. |
| 5 | process | Review record missing | This file |

Everything the card explicitly scopes out (browser OAuth consent, Boxes
2/4/7/8/9 computed, production host, `Gov-Client-*` headers, settings form,
legal sign-off) was checked for *honesty*, not for presence: each is in the
README status table / known gaps / "what a human still needs to do". None
is flagged as a defect.

## Help manual (`universal-till/web/help/`)

No update needed, but for a slightly different reason than "a generic
export topic covers it": there is **no** `web/help/en/` topic for Data
management → Export at all (only `fiscal-register.md` mentions it, for the
DE §146a entry, and `plugins.md` for plugin bundles). The only till-visible
surface this card adds is one more entry in the existing export picker,
labelled by the plugin's own locale key, and the plugin ships its own
in-till docs page (`content/index.html`, ADR-0037), which Dev extended with
the MTD section. A core help topic for one third-party plugin's export
entry would be the wrong place (DE's DATEV entry has none either).

## Not verified

- Never run against HMRC's real sandbox: endpoint paths, `Accept`
  versioning, the refresh grant, the obligations/receipt/error shapes and
  whether Boxes 6–9 are accepted as bare integers are all "researched, not
  tested" — flagged `NEEDS SANDBOX VERIFICATION` per fixture, same line DE
  drew before its 2026-08-18 live run.
- `universal-till`'s real `http_request`/`storage_*` implementations and a
  real installed-plugin export through the till UI.

## Separate-card candidates (not blocking; for the orchestrator to file)

- **Core: plugins have no real clock.** `wasm_runtime.go` never sets
  `WithSysWalltime()` (wazero's sandboxing default), so every plugin's
  `time.Now()` is 2022-01-01. This also silently breaks
  `ut-plugin-tax-de`'s `fiskalyAuth` 5-minute reuse window
  (`time.Now().Unix()-t.ObtainedAt < 300` is always true → a cached
  fiskaly JWT is reused past its 24 h expiry; DE's README already lists
  "behaviour when a cached token is presented past its real expiry" as
  unverified — this is why). Either `WithSysWalltime()` in core (a trust
  decision) or a `clock_now` host function, then revisit both plugins'
  caches.
- **Core: a device/network-metadata host function** so plugins can send
  real `Gov-Client-*` Fraud Prevention Headers — the Architect's named
  close-out follow-up.
- **ut-plugin-tax-de:** `datev.BuildFromCloses` has the same
  sales-but-no-cross-tab blind spot as finding 2 (a pre-#1004 close with
  `gross != 0` yields no rows and is silently dropped from the batch unless
  *every* close is empty).
- This repo still has no version-bump CI guard (noted in the #975 review).

## Verdict

Safe to merge after the two fixes above. Gate: gofmt, host + wasip1 vet,
`go test ./...` (host + wazero-compiled-module tests, 11 MTD cases),
build, validate, i18n guard, package (tarball includes `locales/`),
`go mod tidy -diff` — all green.
