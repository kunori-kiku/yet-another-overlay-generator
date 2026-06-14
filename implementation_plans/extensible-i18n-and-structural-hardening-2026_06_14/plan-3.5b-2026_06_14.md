# plan-3.5b — HTTP error-envelope channel: code every wire error, delete the `writeError` shim

<!-- authored 2026-06-14. Strategy locked by a 6-Opus debate (workflow wf_a087df8f-975 /
     wtrdjdvls): 3 brainstorm (maximalist / pragmatist / principled-hybrid) → 2 critic
     (principle-fidelity / pragmatics+user-lens) → 1 synthesize. Supersedes the one-paragraph
     plan-3.5b sketch in plan-3.5-2026_06_14.md. -->

plan-3.5a localized the **validator channel** (200 `ValidateResponse`). This plan does the OTHER
channel: the **HTTP error envelope** (`{error:{code,message,params}}`). End state — every wire error
is coded; the transitional `writeError`/`CodeLegacyUncoded`/`WithMessage` shim is **deleted**
(grep-gated). ~200 `writeError(` sites + ~50 deep-package `fmt.Errorf` strings, ~31 new codes.

## The granularity rule (the debated fork — apply this ordered, judgement-free test to EVERY site)

> **Q0 METHOD GATE** — any `r.Method != …` rejection → the single shared
> `CodeMethodNotAllowed.With("method", "GET"|"POST"|"GET, POST")`. Never per-endpoint. (stop)
>
> **Q1 SENTINEL?** — if a handler has (or should have) an `errors.Is/As` branch on a
> `controller.Err*` sentinel or a stdlib typed error (`*json.SyntaxError`, `MaxBytesError`,
> `context.DeadlineExceeded`): the HANDLER maps sentinel→dedicated code at the route seam via that
> branch, `.Wrap(err)`. The sentinel is the stable identity; never bucket it. The store NEVER imports
> `apierr` (locked constraint). (stop)
>
> **Q2 WHO CAN FIX IT?** — "can the panel operator change their topology / request body / credentials
> to make this succeed?"
> - **NO** (store I/O, audit append, canonicalize/marshal, missing-identity invariant, template
>   parse, render plumbing, signer, mkdtemp, zip) → **BUCKET** code. The English text becomes
>   `.Wrap(cause)` — LOG-ONLY, never a wire param. **Status-code tie-breaker:** any 5xx that is NOT a
>   genuine compile-constraint is a bucket. *Carve-out:* a 5xx that names a specific operator
>   remediation (keystone "no signed manifest", staged-manifest-mismatch, "no pinned credential")
>   gets a DEDICATED code despite being 5xx.
> - **YES** → Q3.
>
> **Q3 DOES THE ACTIONABLE DETAIL VARY?**
> - the operator-changeable thing is a VALUE (node, cidr, port, method, field, resource, alg) →
>   DEDICATED code carrying it as a `{param}`. Siblings that differ ONLY in the noun = ONE code + a
>   param, NOT N codes ("params over codes when only a noun differs"): required-field 400s → one
>   `CodeReqFieldRequired{field}`; format 400s → one `CodeReqFieldInvalid{field}`; `ErrNotFound` → one
>   `CodeNotFound{resource}` (except two materially-distinct carve-outs below).
> - a FIXED actionable sentence, no variable part ("node id is reserved") → DEDICATED code, no params.

**`{detail}` on the wire** is allowed ONLY where the shipped keygen precedent already does it (the
variant text is itself the actionable thing, e.g. a key-parse error). Default for buckets: **no
free-text/English param on the wire** — the verb/artifact rides `.Wrap(cause)`, log-only, so a zh
operator never sees an English fragment inside a localized sentence. **Never string-sniff
`err.Error()`** to choose a code — the code comes from the SOURCE or from the handler's knowledge of
the OPERATION it performed.

Three families result: **DEDICATED-actionable** (param or fixed sentence; ~4xx + the keystone 5xx
carve-out), **MAPPED-SENTINEL** (handler seam), **BUCKET** (non-actionable internal 5xx).

## Deep-package decisions (per file)

- **compiler/peers.go** (10 sites) — CODE AT SOURCE (the real wrong-language bug: compile-constraint
  failures leaking Chinese via `HandleCompile`'s 422). 4 codes, all **422**: `CodeTransitPoolExhausted{cidr}`
  (:390 alloc-fail wrap +`.Wrap`, :724, :738, :796, :809 — same action: widen CIDR / fewer links),
  `CodeTransitCIDRInvalid{cidr,detail?}` (:709/:763), `CodeTransitCIDRNotIPv4{cidr}` (:714/:766),
  `CodeListenPortExhausted{node,base}` (:840 — template MUST contain "65535" + render `{node}` so
  `peers_portbounds_test.go` survives, or migrate that test to `HasCode`).
- **compiler/compiler.go** (4) — propagate, 0 codes. :100/:125 wrap already-coded inner errors with
  Chinese prefixes → STRIP prefix, return inner `err` (reason: the rendered string for CLI/log/curl;
  `errors.As` already traverses `%w`). :82/:88 corrupted format strings on the dead pre-validated API
  path → fix to English in place.
- **allocator/ip.go** — corrupted strings are :88 (`" CIDR: %s"`) and :170 (`"CIDR %s  IP "`) (the
  design-doc ":68" cite is stale; :68/:74 are already clean English). 3 codes, all **422**:
  `CodeOverlayCIDRInvalid{cidr}` (:88, fix+code), `CodeOverlayPoolExhausted{cidr}` (:170, fix+code),
  `CodeNodeUnknownDomain{node,domain}` (:68, code only). Defensive guards :113/:119/:137/:179 are
  schema-unreachable → leave plain English `fmt.Errorf`.
- **render/render.go** (7, :153–227) — HANDLER-BUCKET. 1 code `CodeRenderFailed` (500). Englishize the
  `All()` prose to a log-only cause. KEEP the 4 shipped `GenerateKeys` codes (:87/:91/:121/:128).
- **renderer/wireguard.go** (4, :192/:198/:213/:219) — HANDLER-BUCKET, 0 codes. Englishize the 4
  literals; they fold into `CodeRenderFailed` via the `render.All` seam.
- **artifacts/export.go** — SPLIT. 2 codes: `CodeExportUnsafeName{name,reason?}` (400, code-at-source —
  `Export` holds `node.Name`; the 5 `validateSafeName` checks collapse to one code, same action =
  "rename the node") + `CodeExportIOFailed` (500, source-returned bucket — ~13 write/sign/mkdir/marshal
  I/O failures; artifact name + os err ride `.Wrap(cause)` log-only, NO English wire param).

## Handler decisions

- **Literal English 500s** "failed to {verb} {object}" (largest class) → one `CodeInternalStorage`
  (500), verb/object in `.Wrap(cause)` log-only, template e.g. "A storage operation failed; please
  retry." "missing authenticated identity" (invariant) → `CodeInternalIdentityMissing` (500, no param).
- **Required-field 400s** → one `CodeReqFieldRequired{field}`. **Format 400s** → one
  `CodeReqFieldInvalid{field}` (err = log-only cause; promote to `{detail}` only if the err text is
  itself operator-actionable). **Fixed sentences** → `CodeNodeIDReserved`, `CodeReqUnsupportedAlg{alg}`.
- **Auth literals** (distinct UX per failure) → dedicated: `CodeAuthCredentialsInvalid`,
  `CodeAuthRateLimited`, `CodeAuthPasskeyFailed`, `CodeForbidden`, `CodeReqBearerRequired` (doubles as
  auth-required), `CodeReqCSRFInvalid`, `CodeReqOperatorRequired`, `CodeTotpInvalidCode`.
- **Actionable keystone/deploy 5xx** (Q2 carve-out) → `CodeNoPinnedCredential`,
  `CodeStagedManifestMismatch`, `CodeNoStagedManifest`, `CodeKeystoneNoSignedManifest`.
- **Sentinel maps** (existing `errors.Is` branch, `.Wrap(err)`, store unchanged): `ErrTokenInvalid|ErrTokenConsumed`
  → `CodeEnrollmentTokenInvalid` (401); `ErrDuplicateWGKey` → `CodeDuplicateWGKey{wg_pubkey,existing_node}`
  (409); `ErrNotFound` → `CodeNotFound{resource}` DEFAULT, with two carve-outs
  `CodeTopologyVersionNotFound` (the "pruned" hint) + `CodeNoTopologyStored`; `ErrNoStagedBundle` →
  `CodeNoStagedBundle`; `context.DeadlineExceeded/Canceled` → `CodeRequestTimeout`.
- **readTopology/decodeJSON** (handler.go) RETURNS `*apierr.Error` at source: JSON → `CodeReqInvalidBody`,
  empty → `CodeReqBodyEmpty`, too-large → `CodeReqBodyTooLarge{limit}` (`isBodyTooLarge` →
  `apierr.HasCode(err, CodeReqBodyTooLarge)`). The two `HandleUpdateTopology` JSON/schema sites
  (:735/:738) COLLAPSE to one `CodeReqInvalidBody` (same action). The custody branch :743 +
  `topology_custody_test` are SACROSANCT.
- **auth_controller.go:132** (non-mechanical: status+msg are variables) — fix at the PRODUCER:
  `authenticateNode`/`operatorAuth` RETURN/emit `*apierr.Error` (`CodeReqBearerRequired` /
  `CodeAuthCredentialsInvalid` / `CodeReqCSRFInvalid` / `CodeReqOperatorRequired`); `requireNode`
  calls `writeAPIError`.

## CLI / stdout — ENGLISHIZE ONLY (no envelope, no codes, no panel)

`cmd/compiler/main.go` printed Chinese + `internal/api/server.go`'s `ListenAndServe` banner print to a
CLI/server process with no `Accept-Language` and no JSON consumer — no localization channel. Translate
the string literals to English in place; do not mint codes. (`server.go:152` panic path is already
`CodeInternalPanic` — untouched.) Outside the `writeError` grep gate.

## The single coded relay (reconciliation with the shipped code)

The relay already exists as `writeCodedOr(w, fallbackMsg string, err)` (handler.go:614), used by the 3
keygen sites (:150/:198/:263). Its tail currently falls back to `writeError` (the shim). **Evolve it**
(do NOT invent a new name): change the signature to `writeCodedOr(w, fallbackBucket apierr.Code, err)`
— `errors.As` → `writeAPIError`, else `writeAPIError(apierr.New(fallbackBucket).Wrap(err))`. This kills
the second path into the shim so the final grep is reachable.

## zh-completeness — ALREADY build-enforced (reconciliation)

`zh.ts` declares `export const zh: Record<keyof typeof en, string>`, so a missing `error.*` key is a
`tsc`/`npm run build` error today. No extra gate needed (and there is no vitest runner). Each PR adds
the matching `error.<code>` keys to BOTH `en.ts` and `zh.ts`; the build proves parity.

## ~31 new codes by domain

- `compile_*` (7, all 422): transit_pool_exhausted{cidr}, transit_cidr_invalid{cidr,detail?},
  transit_cidr_not_ipv4{cidr}, listen_port_exhausted{node,base}, overlay_cidr_invalid{cidr},
  overlay_pool_exhausted{cidr}, node_unknown_domain{node,domain}.
- `render_*` (1): render_failed (500 bucket).
- `export_*` (2): export_unsafe_name{name,reason?} (400), export_io_failed (500 bucket).
- `req_*`/`http_*` (8): method_not_allowed{method} (405), req_field_required{field} (400),
  req_field_invalid{field} (400), req_unsupported_alg{alg} (400), req_invalid_body (400),
  req_body_empty (400), req_body_too_large{limit} (413), node_id_reserved (400).
- `enrollment_*` (3): enrollment_token_invalid (401), duplicate_wg_key{wg_pubkey,existing_node} (409),
  no_staged_bundle.
- `auth_*`/`session_*` (~6 net): auth_credentials_invalid (401), auth_rate_limited (429),
  auth_passkey_failed (401), forbidden (403), req_bearer_required (401), req_csrf_invalid (403),
  req_operator_required (403), totp_invalid_code (400), request_timeout (504/503).
- `notfound_*` (3): not_found{resource} (404), topology_version_not_found (404), no_topology_stored (404).
- keystone/deploy (4): no_pinned_credential, staged_manifest_mismatch, no_staged_manifest,
  keystone_no_signed_manifest.
- `internal_*` (2): internal_identity_missing (500), internal_storage (500).

Untouched: `legacy_uncoded` (DELETED at end), `internal_panic`, `custody_private_key`, `keygen_*` (4).
Every code → const + `registry` + `allCodes` (TestRegistryBijection enforces the set match).

## PR sequence (each ≤~500 lines, independently revertable; SERIAL — every PR edits apierr.go + en/zh)

| PR | scope | files | sites | codes | gate |
|----|-------|-------|------:|------:|------|
| **3.5b-0** | CLI + server banner Englishization (this plan doc rides here) | cmd/compiler/main.go, internal/api/server.go | 13 | 0 | `go build ./cmd/...`; CJK grep zero in string-literal position over the two files; CLI prints English |
| **3.5b-1** | apierr infra: evolve `writeCodedOr` fallback→bucket; rewrite the shim's second path; `TestRegistryEnglishOnly` + `TestNoChineseInApierrSource` | handler.go, apierr_test.go | 3 | 0 | `go test ./internal/apierr/... ./internal/api/...`; grep confirms `writeCodedOr` no longer calls `writeError` |
| **3.5b-2** | deep compile-constraint codes + migrate coupled tests | apierr.go(+test), compiler/peers.go, compiler/compiler.go, allocator/ip.go, en/zh, transit_pools_test.go, peers_portbounds_test.go, handler.go | 11 | 7 | compiler/allocator/apierr tests; a `HandleCompile` test feeding invalid/exhausted transit CIDR → 422 coded envelope; build green; bijection+CJK pass |
| **3.5b-3** | render + renderer + artifacts buckets | apierr.go(+test), render/render.go, renderer/wireguard.go, artifacts/export.go, handler.go, en/zh | 25 | 3 | render/artifacts/api tests; unsafe-name export → coded 400; forced render fail → CodeRenderFailed 500; build green |
| **3.5b-4** | handler.go entrypoints + readTopology body path + method-not-allowed | apierr.go(+test), handler.go, en/zh | 25 | 4 | api tests; wrong verb → method_not_allowed{method}; oversized body → coded 413; build green |
| **3.5b-5** | controller domain A — enrollment / config / topology | apierr.go(+test), handler_controller.go, en/zh | 55 | 11 | api tests incl. topology_custody + enrollment; bad token→401, dup key→409, bad JSON→400 without regressing custody; build green |
| **3.5b-6** | controller domain B — operator / deploy / keystone / sessions / audit | apierr.go(+test), handler_controller.go, en/zh | 55 | 6 | api tests incl. deploy/keystone perpetual; staged-mismatch + no-pinned-credential round-trip coded; build green |
| **3.5b-7** | auth / login / passkey / totp / bootstrap / session + auth_controller producer fix (split 7a passkey+totp / 7b login+bootstrap+auth+session if >500 lines) | apierr.go(+test), handler_login/passkey/totp/bootstrap.go, cookie_session.go, auth_controller.go, en/zh | 65 | 8 | api tests; bad-login(401)+rate-limited(429)+bad-2FA+bearer-required each distinct coded in EN+ZH; build green |
| **3.5b-8** | FINAL grep-gated shim deletion | handler.go, apierr.go, apierr_test.go | 4 | 0 | **THE gate:** `grep -rn 'writeError(' internal/ cmd/` zero non-test; `grep -rn 'CodeLegacyUncoded\|WithMessage' internal/` (incl. _test) zero; new CJK gate over internal/{api,compiler,render,artifacts,renderer} passes; `go test ./... && go vet ./...`; `npm run lint && npm run build`; bijection passes |

## Conversion patterns (the exact before→after a coder applies)

1. **method gate:** `writeError(w, 405, "…")` → `writeAPIError(w, apierr.New(apierr.CodeMethodNotAllowed).With("method", "POST"))`.
2. **sentinel map:** `if errors.Is(err, controller.ErrTokenInvalid) {...}` → `writeAPIError(w, apierr.New(apierr.CodeEnrollmentTokenInvalid).Wrap(err))`. `ErrNotFound` → `apierr.New(apierr.CodeNotFound).With("resource", "node").Wrap(err)`.
3. **required-field 400:** `writeError(w, 400, "node_id is required")` → `writeAPIError(w, apierr.New(apierr.CodeReqFieldRequired).With("field", "node_id"))`.
4. **format 400:** `writeError(w, 400, "public_agent_url: "+err.Error())` → `writeAPIError(w, apierr.New(apierr.CodeReqFieldInvalid).With("field", "public_agent_url").Wrap(err))`.
5. **internal 500:** `writeError(w, 500, "failed to load configuration")` → `writeAPIError(w, apierr.New(apierr.CodeInternalStorage).Wrap(err))`. invariant → `apierr.New(apierr.CodeInternalIdentityMissing)`.
6. **deep source (peers):** `fmt.Errorf("无效的 transit CIDR %q: %w", c, err)` → `apierr.New(apierr.CodeTransitCIDRInvalid).With("cidr", c).Wrap(err)`.
7. **deep source (allocator corrupted):** `fmt.Errorf(" CIDR: %s", cidr)` → `apierr.New(apierr.CodeOverlayCIDRInvalid).With("cidr", cidr)`.
8. **export name-validation:** `fmt.Errorf("节点名称不安全…: %w", err)` → `apierr.New(apierr.CodeExportUnsafeName).With("name", node.Name).Wrap(err)`.
9. **export I/O (bucket, log-only):** `fmt.Errorf("写入 WireGuard 配置失败: %w", err)` → `apierr.New(apierr.CodeExportIOFailed).Wrap(fmt.Errorf("write wireguard config: %w", err))`.
10. **handler relay:** `if err := render.All(...); err != nil { writeError(w, 500, err.Error()) }` → `{ writeCodedOr(w, apierr.CodeRenderFailed, err); return }` (source code wins via errors.As, else bucket). `HandleCompile` → `writeCodedOr(w, apierr.CodeCompileFailed?, err)` so the source 422 survives. *(No `CodeCompileFailed` is minted — every compile failure is now a source code; the relay's fallback bucket for compile is `CodeRenderFailed` or `CodeInternalStorage` per the seam.)*
11. **compiler prefix strip:** `fmt.Errorf("推导 … 失败: %w", err)` → `return err`.
12. **body path:** `errBodyTooLarge` → `apierr.New(apierr.CodeReqBodyTooLarge).With("limit", strconv.FormatInt(maxRequestBodyBytes, 10))`; `isBodyTooLarge(err)` → `apierr.HasCode(err, apierr.CodeReqBodyTooLarge)`.
13. **producer fix (auth_controller:132):** `authenticateNode` returns `*apierr.Error`; `requireNode` calls `writeAPIError`.
14. **test migration:** `strings.Contains(err.Error(), "地址池已耗尽")` → `apierr.HasCode(err, apierr.CodeTransitPoolExhausted)`; `"65535"` survives only if `CodeListenPortExhausted`'s template literally contains it (make it so).

## Open notes (resolved by the user-simulation lens; no owner input needed)

- No `CodeCompileFailed` minted — every compile failure is a source 422 code; the relay fallback is a
  defensive bucket only (in practice unreachable). One-line registry change if the owner ever wants a
  distinct compile-relay bucket.
- `CodeInternalStorage` wire wording is a fully-localizable generic sentence; the op verb is log-only.
  Surfacing the op to operators would need a curated localized `{op}` enum (NOT free-text) — out of
  scope, note for a future plan.

## Verification gate (subject)

`go test ./... && go vet ./...` green; `cd frontend && npm run lint && npm run build` green; an
English-locale operator sees English (and a zh operator the zh catalog) on every HTTP error; `grep -rn
'writeError(' internal/ cmd/` returns zero non-test references.
