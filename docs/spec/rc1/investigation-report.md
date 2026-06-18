# YAOG pre-rc.1 Master Report

**Author:** Lead review synthesis | **Date:** 2026-06-18 | **Branch:** `test/keystone-regression-suite` | **Audience:** project owner

---

## 1. Executive summary + headline verdict

**Verdict: NOT YET rc.1. Close, but six concrete blockers must land first.** The codebase is in strong shape for a beta-to-rc transition: `go build ./...` is clean, the full `go test ./...` is green (api 5.7s, the rest cached), and the keystone/trust surface — the thing that bricks a fleet — is the single highest-value tested asset in the repo (`internal/regression/keystone_regression_test.go`, 9 adversarial black-box scenarios, no `t.Skip`, runs in CI). The recent cross-subgraph pin-collision fix is sound, and the allocation core is deterministic *by construction*.

But the investigation surfaced a small set of real, verified defects that are squarely in the path of what rc.1 is gating on (controller mode, fleet trust, availability). None is an anonymous remote auth bypass. The blockers cluster into three groups:

1. **Robustness gap the DoS work implies is closed but isn't** — `recoverPanics` wraps only the air-gap mux, NOT the operator or agent muxes (verified at `server.go:101-121` vs `handler_controller.go:195-268`). A panic in any fleet/agent handler tears the connection instead of returning a coded 5xx.
2. **Unauthenticated compile-DoS** — a `/api/compile` request with a `/8` domain CIDR + low reserved range pins a CPU core for minutes with no `context` cancellation (`allocator/ip.go:143`). Anonymous in local/air-gap mode.
3. **Enrollment-token authority is broader than "first-enrollment"** — a valid node-scoped token can hijack a live node's identity or resurrect a revoked node, and TTL is uncapped (`enrollment.go:188`, `handler_controller.go:1137`/`1358`).

Plus one functional fleet bug (refreshed-cookie operator can't keystone-sign on Deploy — `controllerClient.ts:971`) and one byte-stability bug (babeld.conf line order tracks edge-array position — `peers.go:570`).

The big strategic item — **moving LOCAL mode fully into the browser via a TypeScript reimplementation of the compiler** — is well-investigated and buildable, but it is explicitly **post-rc.1**. The skeptic's pushback is correct and load-bearing: this converts a ~5,800-line single source of truth into two implementations that must stay byte-identical forever, and the drift-control harness it depends on **does not exist yet** (zero fixtures, zero frontend test runner). It should not begin mid-beta.

---

## 2. Security findings

### 2a. Confirmed (severity, file:line, fix direction)

| # | Severity | Finding | Location | Fix direction |
|---|----------|---------|----------|---------------|
| S1 | **HIGH** | **Allocator compile-DoS.** `allocateFromCIDR` linearly scans the full domain host-space per node; a 320 KB body (2000 nodes, `10.0.0.0/8`, reserved `10.0.0.0/12`) ran `Compile()` for 2m of pure CPU and returned err=nil; `/9` reserved blew past 600s. No `context.Context` in the loop, so client disconnect / the 120s WriteTimeout does not abort it; `recoverPanics` catches panics, not CPU burn. | `internal/allocator/ip.go:143` (loop), schema accepts `>=/8` at `schema.go:160-163` | Thread `context.Context` through `AllocateIPs`/`allocateFromCIDR` and cancel on request-done; cap effective host-space (or reject CIDR+reserved combos whose scan distance exceeds a bound); cap node-count×scan product. |
| S2 | **MEDIUM** | **No cap on `domains` / `reserved_ranges`.** `topologyExceedsBounds` bounds only Nodes(2000)/Edges(10000); `Topology.Domains` and `Domain.ReservedRanges` are unbounded, counted only against the 4 MiB body. `allocateFromCIDR` iterates ALL `reservedNets` per candidate (`ip.go:160-165`), compounding S1. | `internal/validator/schema.go:76` | Add explicit caps for domains and per-domain reserved_ranges to the same DoS-guard that bounds nodes/edges. |
| S3 | **MEDIUM** | **Peer-derivation quadratic gap-fill.** `gapFillTransitPair`/`gapFillLinkLocalPair`/`lowestFreePort` rescan from index 0 per link, re-`ParseCIDR` each iteration; N same-pair backups → O(N²). The `/24` natural ceiling vanishes with a `/12` transit_cidr (accepted by `schema.go:214-227`). Measured ~11s CPU for 8000 same-pair backups. | `internal/compiler/peers.go:938` | Cache parsed transit CIDR; track a per-pool free cursor instead of restart-from-0; add context cancellation. |
| S4 | **MEDIUM** | **Enroll silently hijacks a live node's identity.** `Enroll()` builds `Node{...Status:NodeApproved}` and `UpsertNode`s it AFTER burning the token, with NO guard against the node-id already being a live approved node. The legitimate node's `WGPublicKey` is replaced, a fresh `APITokenHash` minted, and `IssueNodeAPIToken` self-cleans the prior reverse-index — instantly 401-ing the running node. `CheckWGKeyUnique` only refuses the *same key under a different id* (`enrollment.go:233-248`), not a *different key under the same id*. Gated by token possession (authz-via-token), not anonymous. | `internal/controller/enrollment.go:188` | On same-id re-enroll, treat as in-place re-provision with an explicit operator-visible signal (or require the node to be Revoked/absent first); at minimum audit it distinctly from a fresh enroll. |
| S5 | **MEDIUM** | **Revoke doesn't invalidate outstanding enrollment tokens.** `HandleRevoke` flips `Status:NodeRevoked` + clears the bearer but deletes no `tokens/<hash>.json`. `Enroll` has no `NodeRevoked` guard, so an unexpired token re-creates the node as `NodeApproved` with a fresh bearer at the unauth `/enroll` route — silently undoing the eviction (audit records it, so tamper-evident, not tamper-proof). | `internal/api/handler_controller.go:1137` | On revoke, delete/invalidate all enrollment tokens scoped to that node-id; add a `NodeRevoked` guard in `Enroll` (require explicit operator un-revoke). |
| S6 | **LOW** | **Enrollment-token TTL unbounded.** `HandleEnrollmentToken` validates only `TTLSeconds <= 0`; `NewEnrollmentToken` sets `ExpiresAt = now+ttl` with no clamp. Combined with S4/S5, a fat-fingered `ttl_seconds=31536000` is a standing year-long node-takeover/resurrection capability (single-use, but one redemption hijacks). | `handler_controller.go:1358`, `enrollment.go:75` | Server-side max TTL clamp (minutes, not years) — the durable credential is the per-node bearer, the enrollment token only needs a mint-to-bootstrap window. |
| S7 | **LOW** | **IPv4-compatible IPv6 (`::a.b.c.d`) treated as public by SSRF guard.** `isPublicUnicastIP` unwraps IPv4-mapped + 6to4 + NAT64 but not deprecated `::127.0.0.1`. Verified: `net.ParseIP("::127.0.0.1").To4()==nil`, `IsLoopback()==false` → returns public. Practical exploitability LOW (deprecated format; modern kernels don't route it to loopback; needs an AAAA-rebind). | `internal/api/release_pins.go:78` | One-line: also unwrap/`embeddedIPv4` the `::a.b.c.d` (high-96-zero) form and re-check. |
| S8 | **LOW** | **SSRF dial-guard reflects resolved internal IP to caller (DNS oracle).** `fetchSidecar` maps transport errors to `.With("detail", err.Error())`, and the guard message carries the resolved IP (`release_pins.go:347`/`61`); serialized into the JSON error body. Operator-only, so disclosure is to a trusted actor; residual risk is recon if an operator session is compromised. | `internal/api/release_pins.go:347` | Collapse all dial/transport failures to a generic "fetch failed" detail for this endpoint; do not echo `err.Error()`/resolved IP. |
| S9 | **LOW** | **Per-username login lockout DoS.** `registerAttempt` reserves a slot on BOTH the (attacker-controllable) username key and IP key; 10 wrong-password POSTs lock an operator out of ALL login paths (password/TOTP/passkey share `h.loginLimiter`) for 15 min, repeatable. Inherent to account-lockout; break-glass token bypasses `/login`. | `internal/api/loginratelimit.go:85` | Accept as a known property OR temper username-key lockout (e.g. soft delay vs hard block on the username dimension) while keeping IP-key hard. Document break-glass as the escape hatch. |
| S10 | **INFO** | **CSRF double-submit not server-bound to session.** `csrfValid` only checks header==cookie (constant-time); no server-side binding of csrf-value↔session. Safe for the intended single-controller exact-origin-allowlist + TLS-proxy deployment; residual is the textbook double-submit caveat (same-registrable-domain sibling / non-Secure MITM if `YAOG_SECURE_COOKIE=false`). | `internal/api/cookie_session.go:97` | Document the threat boundary (TLS proxy + no untrusted sibling subdomains). Optionally bind csrf to session server-side. |
| S11 | **INFO** | **OCI metadata `192.0.0.192` treated as public.** Single-cloud completeness gap; other metadata IPs (AWS/GCP/Alibaba/AWS-v6) ARE blocked. Body is 512-byte LimitReader + must be 64-hex SHA-256, so OCI JSON can't be exfiltrated — at most an existence/timing signal to an authenticated operator. | `internal/api/release_pins.go:70` | Add `192.0.0.192/32` (optionally `192.0.0.0/24`) to the deny set if OCI is in scope. |
| S12 | **INFO (positive)** | **SSRF egress guard is sound.** The ONLY server-side egress is `HandleReleasePins` via `h.releaseClient`. `blockPrivateAddr` runs post-DNS on every dial incl. each redirect hop (defeats DNS-rebinding); blocks loopback/link-local/multicast/unspecified/RFC1918+ULA/CGNAT, unwraps 6to4+NAT64; scheme-abuse blocked; proxies off; 5-redirect cap; 512-byte body validated to 64-hex. Operator-only, CSRF-protected. Evidence-backed strong result. | `internal/api/release_pins.go:51` | None — keep as-is; address S7/S8/S11 as hardening. |

**Reachability scoping (sets fix priority correctly):** The three compile-DoS findings (S1–S3) are **anonymous only in LOCAL/air-gap mode**. In CONTROLLER mode the `/compile`,`/validate`,`/export` routes are operator-auth-gated (`gateAirgap` arms when `EnableController` sets `s.operatorAuth`), and the node-facing `/config` path serves a *pre-staged* bundle — `CompileAndStage` is operator-only. So the DoS trigger surface is: anonymous in local mode, authenticated-operator in controller mode, **never a lower-trust fleet node**. S1 is a self-DoS / availability issue for a local design tool, not a fleet-node-reachable break. This does not remove it from the blocker list (a public local-mode deployment is a real footgun), but it scopes the blast radius.

### 2b. Uncertain / needs human review

The security review returned **no uncertain findings** (empty list). The completeness critic, however, raised four areas that were *outside* the security review's scope and need an explicit decision before rc.1 (these are not "uncertain bugs" but un-assessed acceptance calls):

- **B1 — Panic-recovery does NOT cover the operator/agent muxes.** VERIFIED: only the 4 air-gap routes + `/health` are wrapped (`server.go:116-121`). `RegisterOperatorRoutes` (`op = cors(operatorAuth(...))`) and `RegisterAgentRoutes` (`requireNode`/`HandleEnroll`/`HandleBootstrap`) have NO `recoverPanics`; `ListenAndServeAgent` mounts `s.agentMux` directly (`server.go:228-237`). A panic in `HandleConfig`/`HandleEnroll`/`HandleStage` yields a torn connection (net/http per-request recover, no `CodeInternalPanic` JSON) — exactly the protection the `recoverPanics` doc comment claims. **This is the cheapest blocker to fix** (wrap both muxes) and the one the DoS coverage implies is already closed.
- **B2 — FileStore durability (fsync).** `writeJSONAtomic` (`filestore.go:217-231`) does `os.WriteFile(tmp)`+`os.Rename` with NO `tmp.Sync()` and NO parent-dir fsync; the audit JSONL append is bare `O_APPEND` unsynced. The comment claims "a crash can never leave a half-written record" — true for torn writes, NOT for reorder-on-rename without fsync. The FileStore holds node identities, bearer hashes, the operator credential, the signing anchor, and the served trust-list. **Decision needed:** fix (add fsync) for rc.1, or document as a known limitation alongside the already-acknowledged SPOF (deferred to rc.2/GA).
- **B3 — Passkey login origin binding is config-gated.** The clientDataJSON `origin` check is conditional on `pin.Origin` being non-empty (`webauthn.go:170`); an operator credential pinned WITHOUT an Origin has NO relying-party origin binding for the *login* path (only rpIdHash). Signature counter is intentionally never checked (synced-passkey support). **Decision needed:** require non-empty Origin for login pins, or record both as explicit accepted properties.
- **B4 — Export ZIP in-memory amplification.** `createExportZip`/`tarGzDirectory` build the entire archive into a `bytes.Buffer` before responding; a max-size topology buffers all per-node bundles in RAM at once on the unauthenticated `/api/export` (local mode). A fourth DoS vector in the same anonymous-local-mode class as S1–S3.

---

## 3. Architecture & structure

### 3a. The local-mode-fully-frontend (TypeScript) plan + conformance harness

**Today's reality:** LOCAL mode is NOT browser-resident. The Zustand frontend is a design tool that POSTs the topology to the Go backend's unauthenticated air-gap routes for *every* compute action (`handler.go:99-310`). The owner's goal is to make local design/validate/compile/export run entirely client-side. The owner chose a **TypeScript reimplementation over Go→WASM**.

**The plan is technically sound and honestly scoped.** The local compile path is a clean downward dependency graph (model ← allocator/validator ← compiler ← renderer ← render ← artifacts) with **one true browser blocker**: WireGuard keygen (`render/render.go:89,109,126` via `wgtypes` — X25519 32-byte CSPRNG + clamp + scalar-base-mult + base64). The plan proposes mirroring the Go layering leaf-first in `frontend/src/compiler/`, using `@noble/curves` for keygen (sync, audited; WebCrypto X25519 is async + browser-inconsistent for raw scalar-base-mult), JSZip for export, and a **CI conformance harness** (`internal/conformance/`) that runs a fixture corpus through BOTH compilers with FIXED per-node private keys (so only public-key *derivation* must match) and asserts byte-equality of every rendered file + allocated value.

**The risk concentrate is `peers.ts`** (porting the 1215-line two-phase reserve-then-gap-fill allocator). The determinism contract is `sort.Slice(links, linkKey)` at `peers.go:484` — VERIFIED as the only allocation-order sort. Any divergence in sort/reservation/primary-unify/backup-discrimination produces a different-but-internally-consistent allocation, so the TS local compile and a later controller recompile of the same topology silently disagree on ports/IPs — breaking incremental-growth stability. Port it LAST, lean hardest on fixtures.

### 3b. Backend-shrink + security delta

If local mode goes fully client-side, the **unauthenticated air-gap compute routes (`/api/validate`, `/api/compile`, `/api/export`, `/api/deploy-script`) can eventually be removed from a local-mode deployment** — which *eliminates the anonymous attack surface for S1–S3 and B4 entirely*. That is a genuine security delta in the rewrite's favor (and an argument for the rewrite independent of UX). The Go pipeline stays the authoritative oracle for controller mode regardless. Net: the backend doesn't shrink in code, but its *anonymous* exposure does.

### 3c. Structural-redesign proposal, tempered by the skeptic

- **`internal/render` vs `internal/renderer`** is a real naming/structure smell, but the skeptic is right: **do NOT touch it mid-beta.** It's a cosmetic rename with churn risk against an active PR stream (#109–#135 in days). Defer.
- **`frontend/node_modules` is being scanned by `go test ./...`** (a vendored `flatted/golang/pkg/flatted` package surfaces in the test output) — a minor build-hygiene smell; a `go` build-tag or module boundary should exclude it.
- **The skeptic's core verdict (`drift_risk_adequately_mitigated = FALSE`) is correct and must be heeded.** Three concrete points: (1) coverage is unbounded-by-construction — "~20-40 fixtures" is an aspiration against a 1067-line semantic.go (~20 sub-validators), a 1215-line allocator (I1-I10 invariants), and 190 `Code*` references; (2) only ONE structural (non-fixture) guard is proposed (the new-`CodeXxx`-needs-a-TS-constant check); (3) **drift is ALREADY happening uncaught** — `frontend/src/lib/normalizeEdges.ts` hand-mirrors `linkKey` + `PIN_FIELDS` from `peers.go`/`linkid.go` with NO test pinning them equal. The harness must retroactively pin that existing duplication, define a Go-side coverage FLOOR (fail if a pipeline package's branch coverage drops below threshold during the conformance run), and be a permanent required gate — not a one-time cutover check.
- **The WASM-vs-TS decision was under-argued.** The skeptic's strongest point: the entire conformance-harness apparatus exists ONLY because TS was chosen — WASM compiles keygen/templates/allocator/validators from the SAME authoritative source (zero dual-maintenance, zero drift surface except marshalling). The owner should make this choice with eyes open: TS = readable/debuggable in-browser + zero-Go-in-bundle for a future pure-frontend product, BUT a permanent byte-exact dual-maintenance tax + `@noble` crypto dep + a hand-rolled `text/template` `{{- -}}` reimpl across ~1500 lines of embedded bash. WASM's real costs (1-3 MB bundle, slower cold start, and the `wgtypes`/`wgctrl` netlink import needing a `js/wasm` build-tag shim) are the legitimate tip factors and should be quantified before committing. **Recommendation: force an explicit written WASM-vs-TS tradeoff with the owner before any code.**

---

## 4. Code hygiene plan

The investigation found a **clean debt signal**: ZERO TODO/FIXME/XXX/HACK markers in non-test Go or frontend code; no commented-out code blocks; only ONE lint suppression in the whole frontend (`CanvasToolbar.tsx:63`, behavior-bearing — leave it); zero `//nolint` in Go. The work is comment/string normalization, not debt triage.

- **Mixed CJK/English comments (~28% of Go files + many .tsx).** ~89 files enumerated. One mechanical pass: Englishize comments only. Watch the JSX-comment form `{/* ... */}` in 10 .tsx files (LoginPage, CustomEdge, TopologyCanvas, DeployBar, EnrollmentFlow, NodeRegistry, EdgeEditor, BottomBar, NodeForm, SettingsPage) — a `^\s*//` regex misses these.
- **Stub comments** (~30): `server.go:52 "// NewServer  API"`, `server.go:123 "// cors CORS"`, `handler.go:36 "// NewHandler  API"`, `allocator/ip.go:12-20`, etc. — fill in the truncated English.
- **Two legitimate-CJK exclusions (do NOT touch):** `LanguageToggle.tsx:21` (`中文` is the visible UI label) and the VALUES in `i18n/messages/zh.ts` (real translations) — only the 12 CJK section-header comment lines in zh.ts get Englishized.
- **One CJK-in-test-string:** `babel_announce_test.go:166` (`t.Errorf("默认路由...")`) — test-only diagnostic; Englishize in the same pass. All production error strings are already English (verified).

This is a low-risk, high-readability pass best done as ONE PR, post-rc.1 (it touches ~90 files and would collide with any active feature branch).

---

## 5. Frontend (type-drift + phone-readiness)

**Structure is healthy:** `tsc -b` is GREEN; two well-split Zustand stores; the local-vs-server-authoritative split is implemented carefully (`canvasFromServer` secret-mirror, `partialize` blanks design data from localStorage in controller mode, a single reused `clearServerCanvasAtGate` predicate); secrets never persisted (verified custody: `stripPrivateKeys`/`dropAllKeys`, in-flight mode-flip guard in `compile()`); keystone status is SERVER-authoritative (closes the cleared-browser false-"Not enrolled" footgun); EN/ZH have exact 631-key parity.

**Confirmed defects:**
- **F1 (functional fleet bug) — `getTrustlist` auth.** VERIFIED at `controllerClient.ts:971-985`: the ONLY authed route that bypasses the shared `request()` helper — raw `fetch` with `Authorization: Bearer ${cfg.operatorToken}`, NO `credentials:'include'`, NO CSRF. After a refresh the in-memory `sessionToken` is gone, so a cookie-only session sends `Bearer ` (empty) with no cookie → 401. GET `/trustlist` IS `operatorAuth`-wrapped (`handler_controller.go:266`) and `operatorAuth` accepts cookie+CSRF. **So a refreshed cookie-session operator's Deploy keystone-signing step fails on a keystone-ON tenant.** This is a real fleet operability bug, not cosmetic. **Fix:** route `getTrustlist` through the shared `request()` helper (gets `credentials:'include'` + CSRF). **Candidate rc.1 blocker.**
- **F2 (type drift) — `router_id` missing in TS.** VERIFIED: Go `Node.RouterID` (`topology.go:87`) is schema-validated, semantically warned, and consumed by the babel renderer, AND both error codes are localized (en.ts:725/zh.ts:707) — so the UI can DISPLAY a `router_id` validation error it can never have CAUSED. `NODE_OMITEMPTY` lists `router_id` so it round-trips, but the typed model can't represent it. Matters doubly for the TS-compiler plan (a browser compiler would drop it and regenerate via `GenerateRouterID`, changing babeld config for any node with a pinned router-id). **Fix:** add `router_id?` to the TS `Node` + a NodeEditor input.
- **F3 (drift HAZARD) — five hand-synced field lists.** `ALLOCATION_PIN_FIELDS`, `PIN_FIELDS`, and `PROJECT/DOMAIN/NODE/EDGE/PUBLIC_ENDPOINT_OMITEMPTY` enumerate Go json tags with KEEP-IN-SYNC comments and NO automated check. Any new model field silently drifts the omitempty-aware canonical diff (phantom save-conflicts or dropped pins). This is the same duplication-drift class the skeptic flagged in `normalizeEdges.ts`.

**Smells (non-blocking):** `controllerStore.ts` (1637 ln) and `controllerClient.ts` (1063 ln) are god-files; cross-store import cycle held together by discipline (lazy `getState()`); dead `zod` dep (no runtime validation — all server JSON via `as` casts); `as unknown as Record<string,unknown>` escape hatches precisely on the hand-synced pin fields; 5 deploy components import `controllerClient` directly (layer leak); **ZERO frontend tests** (no runner — the Go-mirroring pure helpers are unverified by anything).

**Phone-readiness:** the frontend is a React Flow canvas design tool + an app-shell dashboard; this report has no evidence of a mobile/touch layout pass. **Treat phone-readiness as un-assessed and post-rc.1** — there is no finding either way in the inputs, and a canvas topology editor is not obviously phone-usable without dedicated work.

---

## 6. Compiler correctness

**The allocation core is deterministic and order-independent by construction** (VERIFIED: `sort.Slice(links, linkKey)` at `peers.go:484` is the only allocation-order sort; values are a pure function of link identity + current set, satisfying I2/I3/I9). The cross-subgraph reservation and `HealCollidingPins` prevent/clean split is coherent. Bundle digest is iteration-safe (`bundlesig.Canonicalize` sorts paths by byte order).

**Findings:**
- **C1 (HIGH, byte-stability) — babeld.conf line order tracks edge-array position.** VERIFIED: Pass 2 appends `PeerInfo` into `peerMap[nodeID]` in `topo.Edges` array order (`peers.go:570`, `for i := range topo.Edges`), and the babel renderer concatenates that slice into ONE babeld.conf (interface lines `babel.go:118-134`, client-/32 lines `babel.go:167-171`) in slice order. So two topologies equal as SETS but with edges in different array order produce byte-different babeld.conf → different file hash / bundle digest for an unchanged node — violating I2/I8. WireGuard is immune (one file per interface). **Fix:** sort each node's peer slice by `InterfaceName` (or link identity) before rendering. **This is the single most important byte-exact hazard for the TS reimplementation** — the conformance harness will flag babeld.conf diffs on edge reorder unless both sides agree on the (currently incidental) ordering. **Candidate rc.1 blocker** (it can corrupt incremental-deploy stability today, not just the future port).
- **C2 (MEDIUM) — re-enable-disabled-edge pin collision.** Disabling edge A-B frees its slot; A-C gap-fills into it; re-enabling A-B → both enabled with the same pinned values, different identities. Caught at compile by `CodePin*DuplicateCrossLink` (FAILS loudly, no wrong config — good), but it's an operator surprise (re-enabling an untouched link bricks the compile) and heal strips the LATER edge in slice order, possibly A-B (the one wanted back). **Fix:** targeted test + auto-heal-on-reenable.
- **C3 (MEDIUM) — reverse-endpoint fallback keys on `HasPublicIP`, not endpoint presence.** `peers.go:783` gates the reverse-peer Endpoint on `HasPublicIP && len(PublicEndpoints)>0`; `HasPublicIP` is raw operator-supplied and never derived from PublicEndpoints. A node with a real endpoint but `has_public_ip=false` produces a reverse peer with EMPTY Endpoint — a wrong-but-valid one-directional link (compiles clean; NAT validator only warns). **Fix:** derive/normalize `HasPublicIP` from `len(PublicEndpoints)>0`, or warn on the mismatch.
- **C4 (LOW) — `computeChecksum` uses `fmt.Sprintf("%v", topo)`.** `compiler.go:234-238` — not a canonical encoding (sensitive to fmt formatting; would differ from any TS reimpl). It's a display-only manifest checksum, not the security-bearing bundle digest, so low impact — but the TS port must NOT try to reproduce it byte-for-byte (scope it out of conformance).

---

## 7. Tests / CI

**The Go suite is heavyweight and load-bearing:** 109 test files, ~26.8k test LOC vs ~23.4k non-test (1.15:1); `go test ./...` green; a fresh `-race` run of the concurrency trio (~14s) passes clean; gofmt/`go vet`/eslint clean. Per-package coverage: linkid 100%, naming 97.4%, normalize 96.4%, allocator 91.9%, validator 91.6%, compiler 86.7%, renderer 86.6%, controller 82.9%, api 69.1%; **all `cmd/*` mains 0%.**

**Highest-value assets (high rc.1 confidence):** the keystone regression suite (9 adversarial scenarios incl. the `<` vs `<=` epoch guard, algorithm-confusion, the two beta.5 repros, and a `-race`-correct concurrent-promote reader); self-update SAFETY decision logic (`compareVersions`/`decideSelfUpdate`/`isForced`/`parseAgentCatalog` 100%); the agent↔controller wire contract end-to-end; store dual-impl parity (`store_compat_test.go`, 1441 ln); allocation pin logic (`allocation_stability_test.go` 574 ln).

**Untested areas (rc.1-relevant):**
- **FRONTEND HAS ZERO AUTOMATED TESTS.** No runner; CI frontend job is lint+build only. The Go-mirroring pure helpers (`normalizeEdges.ts healCollidingPins`, `updateStatus.ts deriveUpdateState`, `custody.ts`, `webauthn.ts`) and the deploy orchestration are verified only by manual/backend parity. This is the structural enabler for the F3/normalizeEdges drift class.
- **`cmd/*` mains 0%** — the two-port boot, SIGTERM drain, create-operator argon2 write, and the agent's load-bearing self-update reconcile PhaseA/B/Finalize call-ORDER are exercised only indirectly.
- **Server lifecycle wiring uncovered** (`ListenAndServeAgent`/`AgentHandler`/`EnableStatic` 0%) — the two-mux/two-port boot is untested above the handler level. (This is the same surface as B1.)
- **Host-side self-update I/O glue** (`renameOrCopy` 7.7% — the literal brick moment) thin; the decision is fully tested, the swap mechanics aren't.
- **No doc-vs-code gate:** 5 docs/spec files link to an archived path (broken); `repository-structure.md` is stale (8 of 17 entries wrong).

---

## 8. Completeness-critic gaps

Folded into the report above where they bear on rc.1: **B1 (panic-recovery on operator/agent muxes — VERIFIED, blocker)**, **B2 (FileStore fsync durability)**, **B3 (passkey origin binding config-gated)**, **B4 (export ZIP in-memory amplification)**, and the **DoS reachability scoping** (anonymous local-mode only; never fleet-node-reachable — §2a). The standout is B1: the DoS/robustness narrative *implies* allocator panics become clean 5xx everywhere, but that protection lives only on the air-gap mux — the fleet-bearing controller/agent handlers fall back to a torn connection.

---

## 9. Prioritized split — rc.1 blockers vs post-rc.1 roadmap

### BLOCKS rc.1 (fix before tagging)
1. **B1 — Wrap `s.mux` operator routes and `s.agentMux` agent routes in `recoverPanics`** (`server.go:101-121` / `handler_controller.go:195,213`). Cheapest blocker; closes the robustness hole the DoS work implies is closed.
2. **S1 — Bound the allocator compile-DoS** (`allocator/ip.go:143`): thread `context.Context` + cancel on request-done, and cap effective host-space / node×scan. Anonymous in local mode.
3. **S4 + S5 — Close the enroll identity-takeover + revoke-resurrection** (`enrollment.go:188`, `handler_controller.go:1137`): guard same-id hostile re-enroll; invalidate enrollment tokens on revoke + add a `NodeRevoked` guard in `Enroll`. (Fix together — same authority model.)
4. **S6 — Clamp enrollment-token TTL server-side** (`handler_controller.go:1358`). One-line guard that bounds the S4/S5 window.
5. **F1 — Route `getTrustlist` through the shared `request()` helper** (`controllerClient.ts:971`) so a refreshed cookie-session operator can keystone-sign on Deploy. Real fleet-operability bug.
6. **C1 — Sort each node's peer slice before babel rendering** (`peers.go:570` / `babel.go`) to restore I2/I8 byte-stability for babeld.conf. Affects incremental-deploy stability today.

**Decision-required before rc.1 (not code, but gate):** B2 (fsync — fix or document), B3 (passkey origin — require or accept), S9 (login lockout — accept or temper), S10 (CSRF — document threat boundary).

### Post-rc.1 roadmap
- **Local-mode TS compiler rewrite** — only after an explicit written WASM-vs-TS tradeoff with the owner; build the conformance harness FIRST (Phase 0), retroactively pin the existing `normalizeEdges.ts` duplication, define a Go-side coverage floor + structural guards, make it a permanent required CI gate. Removes the anonymous air-gap attack surface (S1-S3, B4) as a bonus.
- **Remaining DoS hardening:** S2 (cap domains/reserved_ranges), S3 (quadratic gap-fill cursor + context), B4 (stream the export ZIP instead of buffering).
- **SSRF completeness:** S7 (`::a.b.c.d`), S8 (generic dial-error detail), S11 (OCI `192.0.0.192`).
- **Compiler correctness:** C2 (auto-heal-on-reenable + test), C3 (normalize `HasPublicIP` from endpoints).
- **Frontend test runner** (vitest) covering the Go-mirroring pure helpers; add the F3 hand-synced-field-list guard.
- **Code hygiene pass** (CJK comments + stubs) as one isolated PR.
- **Coverage:** `cmd/*` mains (boot/drain/reconcile-order), server lifecycle wiring, `renameOrCopy`; doc-vs-code link gate.
- **Deferred to rc.2/GA (already acknowledged):** FileStore SPOF, bootstrap-TOFU, KMS/OIDC/multi-tenant.

### Top risks
- **R1 — B1 panic gap is a live fleet-availability hole** masked by a doc comment that claims the opposite; a single nil-deref in a hot agent handler degrades the controller without a clean error.
- **R2 — TS rewrite drift is structurally unmitigated today** (zero fixtures, zero FE runner, an already-drifting `normalizeEdges.ts` mirror). Starting it mid-beta would compound risk against the #109-#135 PR stream.
- **R3 — C1 babel ordering can silently churn deployed-node config hashes** on a benign edge reorder, eroding the incremental-deploy stability the project's whole pin/heal apparatus exists to protect.
- **R4 — Enrollment-token authority (S4/S5/S6) is broader than the mental model** ("one-shot first-enrollment"); a single leaked/over-broad token is a standing node-takeover/resurrection capability for its full TTL.
- **R5 — The DoS findings' anonymous reach is local-mode-only**; mis-scoping them (over- or under-prioritizing) risks either delaying rc.1 for a self-DoS or shipping a public local-mode endpoint that pins a core.