# YAOG Unified Audit Dossier

**Date:** 2026-06-07 · **Target:** `main` @ `d5065ed` (v1.2.0) · **Method:** static analysis (Go/npm toolchains unavailable locally)

**Provenance:** three multi-agent audit workflows, all findings adversarially verified:
- **Deep bug audit** — 78 confirmed findings (7 critical / 37 high / 23 medium / 11 low), each surviving a 3-lens refute/reproduce/impact panel; 13 candidate claims refuted and excluded.
- **UX audit** — 6 confirmed friction findings (1 blocker / 5 major), each surviving 2-lens grounding; plus a verified end-to-end "bridge two servers" journey trace.
- **Incremental-growth audit** — 14-claim stability matrix (13 verified, 1 overturned), 3 competing designs judged by a 3-judge panel (unanimous winner), 10 normative invariants.

This dossier consolidates all of it into root-cause themes. Each theme lists its findings
(severity, location), the single root cause, and where the normative fix text belongs in
`docs/spec/`.

---

## Part I — Root-Cause Themes

The 84 confirmed findings + stability matrix collapse into **14 themes**. Four of them
(T1, T3, T2, T13) account for the majority of user-visible breakage.

### T1. Port/endpoint ownership confusion — THE HEADLINE BUG
**Root cause:** nobody specified who owns `endpoint_port`. The frontend silently turns a node's
*reachability hint* (`public_endpoints[0]`) into a per-edge *NAT dial override* at edge-draw time
(`TopologyCanvas.tsx:243-251`); the backend honors any nonzero override and suppresses its own
correct per-peer port auto-allocation (`peers.go:294-296` vs auto path `:298-304`), while the
remote interface always binds `base+offset` (`peers.go:174-191`).

| # | Sev | Finding | Where |
|---|---|---|---|
| D1 | CRIT | Frontend stamps `public_endpoints[0].port` as `endpoint_port` → backend dials a port nothing listens on | TopologyCanvas.tsx:243-251 |
| D8 | HIGH | All parallel edges into a hub inherit the SAME port; at most one tunnel can establish | same + peers.go:294-296 |
| D9 | HIGH | Snapshotted endpoint never re-synced when node's `public_endpoints` change → stale dial target | same; no reconciler |
| D21–23 | HIGH | All three shipped examples hard-code `endpoint_port:51820` → simple-mesh: 3 of 6 directed edges dead; nat-hub & relay: 2nd client/peer permanently dead | examples/*/topology.json |
| D57 | MED | Examples teach the anti-pattern (always set endpoint_port) | examples/* |
| D51 | MED | `CompiledPort` write-back ignores the override → UI shows a port that differs from the rendered Endpoint | compiler.go:112-126 |
| UX-2 | MAJOR | One drawn edge is asymmetric: only target's endpoint auto-fills; drag direction covertly decides who can dial | TopologyCanvas.tsx + peers.go:379-396 |

**Fix kernel:** stop auto-stamping `endpoint_port` (stamp `endpoint_host` only); make NAT override
explicit opt-in; reconcile/lazily resolve endpoints; backend derives reverse endpoint from
`fromNode.PublicEndpoints` when no reverse edge exists (UX-2's backend-only fix); fix examples;
make `CompiledPort` reflect the actually-dialed port.
**Spec home:** `docs/spec/data-model/edge.md` (ownership contract) + `docs/spec/compiler/peer-derivation.md` (resolution rule).

### T2. Routing-mode / Babel semantic gaps — silent dead overlays
**Root cause:** Babel is the only route installer, but its gates and kernel-route prerequisites
are unspecified and partially wrong.

| # | Sev | Finding | Where |
|---|---|---|---|
| D2 | CRIT | `Table = off` unconditional while `RoutingMode != "babel"` (incl. empty `""`) suppresses babeld → compiles green, zero routes, dead overlay | wireguard.go:59; babel.go:160-169; schema.go:104 |
| D40 | HIGH | Gateway default route (`redistribute local ip 0.0.0.0/0`) can never match a kernel route → internet egress silently never propagates | babel.go:107-108 |
| D41 | HIGH | Domain-CIDR / extra-prefix `redistribute local` rules match no connected route → aggregates and LAN prefixes never announced (only the dummy0 /32 works) | babel.go:99-105 |
| D72 | LOW | Empty enum values (`routing_mode`, `transport`, `allocation_mode`) bypass schema enum checks | schema.go:97,104,195 |
| D63 | MED | `Edge.Priority`/`Weight` exist but are never read — no effect on Babel costs | babel.go:84-90 |
| D73 | LOW | Router's babeld.conf declares the client tunnel as a peering interface (client runs no Babel) | babel.go:84-92 |
| D78 | LOW | `local-port 33123`, `hello-interval 4`, `update-interval 16` hardcoded; preset fields exist but are dead | babel.go:44,56 |

**Fix kernel:** default `routing_mode: "babel"` at normalization; gate `Table = off` on babel mode
or hard-error non-babel modes; pair every announced prefix with a matching kernel route (or babel
static route); wire Priority/Weight → rxcost; skip IsClientPeer interfaces.
**Spec home:** NEW `docs/spec/compiler/routing-modes.md` (or section in `artifacts/babel.md`).

### T3. Artifact naming & deploy identity — silent skips and WRONG-IDENTITY deploys
**Root cause:** two divergent naming functions (raw vs `safeInstallerFileName`) plus no
name-uniqueness invariant anywhere.

| # | Sev | Finding | Where |
|---|---|---|---|
| D3/D32 | CRIT | ZIP entry uses RAW node name; deploy script looks up SANITIZED name → every node with uppercase/space/special char is silently skipped | handler.go:394,407 vs deploy.go:47,216,609-624 |
| D5 | CRIT | Two names sanitizing identically ("Web 1"/"web-1") → deploy runs the OTHER node's installer: wrong keys/IPs, reported SUCCESS | deploy.go:216,298,324 |
| D31 | HIGH | Colliding sanitized names clobber the same remote `/tmp/<name>.install.sh` path | deploy.go:324,540 |
| D13/D14 | HIGH | Duplicate (or identically-sanitizing) node names collide on `wg-<name>` interface names → one config silently overwrites the other; duplicate babel interface lines | peers.go:492-522; wireguard.go:166,198 |
| Spec gap | — | `wgInterfaceName` >15-char branch uses `clean[:8]+sha256[:4]`, but frontend reconstructs by plain truncation → "Compiled values" lookup silently misses for names >12 chars | peers.go:492-522 vs RightPanel.tsx:622 |

**Fix kernel:** ONE canonical naming function shared by ZIP writer + deploy renderer; semantic
validation error on raw-name AND sanitized-name collisions; de-collide with short node-ID hash;
frontend receives interface names from compile response instead of reconstructing.
**Spec home:** NEW `docs/spec/artifacts/naming.md`.

### T4. Unvalidated input → crash, injection, broken configs
**Root cause:** validator coverage is a fraction of the model surface; renderers interpolate
user text into root-executed shell without escaping.

| # | Sev | Finding | Where |
|---|---|---|---|
| D4/D35 | CRIT | IPv6 domain CIDR passes validation, panics allocator (`nil[12:16]`) → aborted request, no recover middleware | ip.go:129,164-169 |
| D20 | HIGH | Schema accepts IPv6/any-family CIDR though allocator is IPv4-only (root gap behind D4) | schema.go:88-93 |
| D56 | MED | `/0` CIDR: `uint32(1)<<32` overflows → ~4.29B-iteration CPU spin per request | ip.go:116,121,131 |
| D7 | CRIT | `SSHTarget` interpolated unquoted into bash deploy script → local command injection on the OPERATOR's machine | deploy.go:282-325 |
| D43 | HIGH | Same for PowerShell variant (arg-splitting + quote injection) | deploy.go:507-578 |
| D44 | HIGH | `ssh_host`/`ssh_user`/`ssh_alias` validated NOWHERE end-to-end | (absence) |
| D15 | HIGH | `{{ .NodeName }}` unescaped in root-executed install.sh `echo` → root command injection via node name | script.go:61 |
| D16 | HIGH | Single quote in node name breaks deploy-script heredoc | deploy.go:237 |
| D11 | HIGH | `base+offset` listen port can exceed 65535, rendered verbatim | peers.go:175-191 |
| D47 | MED | Port-conflict check compares base ports only; co-hosted effective-range overlaps undetected (warning-only) | semantic.go:160-181 |
| D48 | MED | /24 transit pool: only 127 pairs; index 127 hands out the .255 broadcast | peers.go:458-468 |
| D64–67 | MED | MTU, SSHPort, RouterID, ExtraPrefixes all unvalidated → non-deployable configs (wg-quick/babeld reject) | schema.go (absence) |
| D70 | LOW | Link-locals formatted decimal but parsed hex (`fe80::11` = 17) — non-sequential, contradicts spec | peers.go:474-479 |

**Fix kernel:** IPv4-family guard + CIDR-size bound; shell-quote every interpolation (mirror the
existing `%q` on SSHKeyPath); strict charsets for ssh fields and node names; effective-port
validation; full field-coverage validation pass.
**Spec home:** `docs/spec/compiler/validation.md` (validation coverage contract).

### T5. API robustness & feedback
| # | Sev | Finding | Where |
|---|---|---|---|
| UX-1 | BLOCKER | `/api/compile` never runs semantic validation; `CompileResponse` has no `Warnings` → NAT/endpoint-less-edge warnings generated but discarded; user ships dead tunnels on a green compile | handler.go HandleCompile; nat.go:25-38 |
| D33 | HIGH | Bare `http.ListenAndServe`, zero timeouts → Slowloris/slow-body DoS | server.go:63 |
| D34 | HIGH | `io.ReadAll` with no size cap on every POST → OOM DoS | handler.go:249 |
| D60 | MED | No panic-recovery middleware → panics become connection aborts, not 500s | server.go:34 |
| D42 | HIGH | Store `validate()` ignores `res.ok` → server errors render a fully blank validation panel | topologyStore.ts:305-311 |
| — | — | nat.go warning strings have garbled/empty i18n at lines 27/35/61 | nat.go |

**Spec home:** `docs/spec/api/http-api.md` (compile contract: validation + Warnings; limits).

### T6. CLI/API entrypoint divergence — CLI output is non-deployable
| # | Sev | Finding | Where |
|---|---|---|---|
| D6 | CRIT | CLI writes literal `FAKE_PRIVKEY_*`/`FAKE_PUBKEY_*` into every conf + checksums; wg-quick rejects; no warning, undocumented | cmd/compiler/main.go:108-117 |
| D27–29 | HIGH | CLI never renders client wg0.conf and uses the per-peer install template for clients → client bundles empty/wrong | main.go:59-92; export.go:43-99 |
| D59 | MED | CLI never renders deploy-all.sh/.ps1 | main.go:83-99 |

**Fix kernel:** extract `handler.go`'s `generateKeys` + `renderAll` into a shared package; both
entrypoints call it. Single change eliminates the whole theme.

### T7. FE↔BE wire-contract drift
| # | Sev | Finding | Where |
|---|---|---|---|
| D10/D37 | HIGH | `route_policies`: declared on both sides, never sent by FE, consumed by NO renderer — dead end-to-end; silently dropped on import + compile round-trip (D45/D55), zero validation (D62) | topologyStore.ts:224-227; compiler.go:94 |
| D46 | MED | `Domain.transit_cidr` absent from TS type + UI — feature unreachable, dropped on round-trip | topology.ts:18-26 |
| D68 | LOW | `transport`/`priority`/`weight`/`notes` have no editor; transport default relies on FE stamping 'udp' | TopologyCanvas.tsx:252 |
| D69 | LOW | NodeForm can't create `client` role; FE-stamped capabilities can contradict role inference | NodeForm.tsx:9-46 |
| Stale docs | — | "TS types mirror Go exactly" is false (transit_cidr); "Auto:<port> button" doesn't exist (README:69, wiki:110) | docs |

**Decision needed:** wire `route_policies` fully or remove/reserve it. Same for transit_cidr UI.
**Spec home:** NEW `docs/spec/api/wire-contract.md` (field-by-field parity table, round-trip rules).

### T8. Compiler/renderer semantics
| # | Sev | Finding | Where |
|---|---|---|---|
| D30 | HIGH | Client wg0 AllowedIPs = own domain CIDR only → cross-domain overlay, router's out-of-domain /32, and transit net all black-hole from the client | peers.go:658-662; wireguard.go:127-130 |
| D49 | MED | Router/gateway never get `CanAcceptInbound` from inference (only relay does) → spurious keepalives; inconsistent with `DeriveRoleSemantics` | roles.go:111-118 |
| D50 | MED | Both-NAT endpoint-less edge = provably dead link, but only a WARNING | nat.go:31-38 |
| D71 | LOW | Duplicate edges for the same pair: later edge silently ignored (its endpoint override dropped) | peers.go:150-152,225-228 |
| D12 | HIGH | Global `transitPairIndex` vs per-domain `transit_cidr` pools → small custom pool "exhausts" while empty | peers.go:132,155-166,443-468 |
| D38/D39 | HIGH | SNAT rule + overlay-snat.service hardcode `10.10.0.0/24`; custom transit_cidr breaks source-address fix permanently (incl. across reboots) | script.go:349-370 |

### T9. Install/deploy script behavior
| # | Sev | Finding | Where |
|---|---|---|---|
| D52 | MED | iptables SNAT cleanup keys on the NEW overlay IP → stale rule survives IP change | script.go:337 |
| D53 | MED | `set -e` + un-guarded `wg-quick up` → one failed interface aborts install, babeld never starts | script.go:426 |
| D36 | HIGH | Standalone `/api/deploy-script` passes nil peerMap → uninstall lacks per-interface teardown entirely | handler.go:222; deploy.go:71-78 |
| D61 | MED | Same endpoint: `HasBabel` role-only fallback ignores routing_mode → wrong Babel teardown | deploy.go:88 |

### T10. Integrity chain
| # | Sev | Finding | Where |
|---|---|---|---|
| D24 | HIGH | `install.sh` itself is NOT in checksums.sha256 — the root-executing trust anchor is unverified | export.go:101-121 |
| D25 | HIGH | Self-extracting wrapper decodes + runs payload as root with NO hash over the payload | handler.go:477-519 |
| D76 | LOW | Client README claims per-peer architecture (contradicts its own manifest) | export.go:159-161 |

**Fix kernel:** embed a Go-computed SHA-256 of the tar.gz into the wrapper; verify before extract/run.

### T11. Frontend state correctness
| # | Sev | Finding | Where |
|---|---|---|---|
| D17 | HIGH | `edge-${Date.now()}` IDs collide on fast draws → edits/deletes hit both edges | TopologyCanvas.tsx:241 |
| D18 | HIGH | `setNodes`/`setEdges` inside `useMemo` (side effects in render) → nodes jump to stale positions | TopologyCanvas.tsx:220-234 |
| D19 | HIGH | Stale `compiled_port` never cleared on endpoint edit; canvas label prefers it → UI contradicts intent | RightPanel.tsx:580-600; TopologyCanvas.tsx:130 |
| D26 | HIGH | Security audit reads stale LOCAL capabilities, not backend-inferred → under-reports exposed relays | AuditView.tsx:63 |
| D54 | MED | Role change re-derives caps only for `client` → role/caps diverge | RightPanel.tsx:238-251 |
| D58 | MED | `:`-delimited selector parsing breaks for IDs containing `:` | AuditView.tsx:45 |
| D74/75/77 | LOW | Literal `\n` in previews; import doesn't clear history; diff keeps zero context | RightPanel/AuditView |

### T12. UX: the bridging journey (verified end-to-end)
The natural gesture — draw an edge between two servers — produces a **silently dead tunnel**
(no Endpoint line, warning never surfaced). The working path forces:
Domain+CIDR → node A/B → tick "Publicly Reachable" → type endpoint "mappings" → drag in the
right DIRECTION → manually fix/reverse the edge → compile (green either way).

| # | Sev | Finding |
|---|---|---|
| UX-1 | BLOCKER | Compile never surfaces warnings (also T5) |
| UX-2 | MAJOR | Single edge → asymmetric config; drag direction decides who dials (also T1) |
| UX-3 | MAJOR | Domain-with-CIDR is a hard prerequisite for the first node (disabled button, opaque) — seed a default domain (NOT 10.10.0.0/24; it collides with transit pool) |
| UX-4 | MAJOR | Pre-compile handles are invisible 8px gray dots; instructive colored handles only appear AFTER first compile (chicken-and-egg); no isValidConnection |
| UX-5 | MAJOR | Public IP entry hidden behind a checkbox, labeled "endpoint mappings" jargon |
| UX-6 | MAJOR | LAN bridging (extra_prefixes/RoutePolicy) has ZERO UI — entire use-class unreachable |

### T13. Allocation stability & incremental growth (verified matrix)
The user's claim — "after compiling you only have ports for existing servers; no room to add new
ones" — is **substantively right**, dominated by one mechanism:

| Artifact | On add-server | Mechanism |
|---|---|---|
| Overlay IP | ✅ STABLE | `ip.go:62-64` skips set IPs — the proven pattern |
| Listen port / transit pair / link-local | ⚠️ stable on APPEND, SHIFTS on any reorder/delete-readd/enable | positional counters (`nodePortOffset`, `transitPairIndex`) over `topo.Edges` order; nothing binds value→link identity; CompiledPort write-only |
| **WireGuard keys** | ❌ **ROTATE EVERY COMPILE** (default) | non-fixed branch zeros node keys; random key lives only in ephemeral render map (`handler.go:308-314`) → full-fleet redeploy on every recompile |
| deploy-all scope | ❌ always whole fleet (no selector) | deploy.go:44,213 |
| install.sh | ❌ down-all/up-all → bounces unrelated tunnels | script.go:139-151,424-428 |
| Port headroom | ❌ field doesn't exist | topology.go:60-61 |

**Winning design (3/3 judges): Sticky/Pinned Allocation** — generalize the OverlayIP pattern:
- Six `pinned_*` fields on Edge (ports, transit pair, link-locals), round-tripped via the existing
  compile write-back + localStorage persistence (zero new transport)
- Compiler Pass 1 becomes **reserve-all-pins-first, then gap-fill unpinned** (order-independence
  by construction, not by sorting)
- `generateKeys`: non-empty `WireGuardPublicKey` ⇒ treated as fixed (reuse), only new nodes get
  fresh keys
- Grafts: hash-seeded gap-fill (delete/re-add idempotence), canonical `pinKey(a,b)=sorted min|max`,
  `AllocSchemaVersion`, order-independence property test
- Result: adding C via A leaves B with a byte-identical bundle — **zero action on B**
- Migration: one-time pin-seeding compile; operators either paste live keys (FixedPrivateKey) or
  accept a one-time rotation

**Normative invariants I1–I10** (full text in growth report; skeleton already in
`docs/spec/compiler/allocation-stability.md`): superset stability, order independence, identity
binding, additive growth, key persistence, headroom/exhaustion, validated pins + explicit
renumber, change observability, GC/no-leak, schema versioning.
**Dependencies flagged out-of-scope but required for full zero-touch growth:** additive
install-apply (T9/D53 area) and per-node deploy selector (T8/D36 area).

### T14. Stale documentation (confirmed against code)
- README:69 + wiki:110 — "Auto:<port> button" does not exist
- "TS types mirror Go exactly" — false (`transit_cidr`, route_policies wiring)
- wiki role tables — router/relay DO announce extra_prefixes; `client` missing from wiki role list
- Babel fixed timers (hello 4 / update 16) undocumented
- Link-local "sequential" claim contradicted by decimal/hex bug (D70)

---

## Part II — Spec Work Program (maps to `docs/spec/`)

Priority order (contract-before-code):

| Spec | Status | Home | Settles |
|---|---|---|---|
| **A. Port & Endpoint Contract** | NEW, highest priority | `data-model/edge.md` + `compiler/peer-derivation.md` | ownership (backend sole port authority; FE never auto-stamps), resolution rule, CompiledPort semantics, NAT-override opt-in |
| **B. Allocation Stability & Growth** | DRAFT stub exists | `compiler/allocation-stability.md` | I1–I10 + sticky-pin mechanism + migration |
| **C. Routing-Mode & Route Installation** | NEW | `compiler/routing-modes.md` (new file) | mode enum + default, Table=off gate, kernel-route prerequisites for every announced prefix |
| **D. Artifact Naming & Deploy Identity** | NEW | `artifacts/naming.md` (new file) | canonical name function, uniqueness invariant, collision de-confliction, interface-name algorithm incl. hash branch |
| **E. Wire Contract** | NEW | `api/wire-contract.md` (new file) | field parity table, round-trip rules, key-blanking behavior, route_policies/transit_cidr decisions |
| **F. API Contract amendments** | amend | `api/http-api.md` | compile runs semantic validation + returns Warnings; body limits; timeouts |
| **G. Validation coverage** | amend | `compiler/validation.md` | full field-coverage table (MTU, SSH*, RouterID, ExtraPrefixes, effective ports, IPv4-only) |
| **H. Determinism notes** | amend | `compiler/ip-allocation.md`, `compiler/peer-derivation.md` | order-dependence facts until B lands; CIDR-edit renumbering |
| **I. Key persistence** | amend | `data-model/node.md`, `security/security.md` | non-empty key = fixed; rotation explicit only |

## Part III — Open Design Decisions (need owner sign-off)

1. **route_policies**: implement (babel filter/static-route rendering + UI + validation) or remove/reserve? (D10/D37/D62/UX-6 hinge on this)
2. **Non-babel routing modes**: build a static-route renderer, or reject `static`/`none` at validation until implemented? (D2)
3. **Client AllowedIPs**: all-domains+transit union, or 0.0.0.0/0? (D30)
4. **Sticky-pin migration**: one-time key rotation acceptable for existing deployments, or require live-key capture flow? (T13)
5. **transit_cidr**: expose in UI (with per-domain transit index fix D12) or document backend-only?
6. **Canonical artifact name**: sanitized form wins (ZIP renamed) — confirm; de-collision via node-ID hash suffix.

## Part IV — Consolidated Fix Order (dependency-respecting)

- **Phase 0 — Specs A–E** (contract freeze; no code)
- **Phase 1 — Frontend headline fix** (T1: stop auto-stamping; reconcile stale endpoints; fix examples) — highest impact, lowest risk, independent
- **Phase 2 — Backend correctness, no contract change**: naming unification + uniqueness validation (T3); routing-mode gate + default (T2); IPv4 guard + CIDR bounds (T4); effective-port validation; per-domain transit index + SNAT parameterization (T8); compile-returns-warnings (UX-1/T5)
- **Phase 3 — Security**: shell-quoting everywhere + ssh/node-name charset validation (T4); integrity chain (T10); HTTP hardening (T5)
- **Phase 4 — Allocation stability** (T13: pinned fields, reserve-then-gap-fill, key reuse, validateAllocationPins, property test) — the growth feature
- **Phase 5 — Shared entrypoint** (T6: extract keys+renderAll; CLI parity)
- **Phase 6 — Contract features per decisions** (T7: route_policies, transit_cidr UI, edge field editors)
- **Phase 7 — Frontend state + UX** (T11 bugs; UX-3/4/5/6: default domain, visible handles + isValidConnection, top-level public-address field, extra_prefixes editor)
- **Phase 8 — Docs/spec sync** (T14 + reconcile with shipped behavior)

Cross-phase invariant: every fix lands with its spec text (the spec is now `docs/spec/`,
restructured 2026-06-07; `DEVELOPMENT_SPEC.md` is a redirect stub).

## Appendix — Refuted claims (do NOT act on these)
13 candidate findings were adversarially refuted, including: "compile overwrites local nodes
with key-blanked nodes **and that is itself a bug**" (the blanking is real — see T13 — but the
refuted framing had wrong mechanism), "router_id unreachable from frontend", "Zustand persist
lacks version/migrate", "uninstall/deploy.go SNAT hardcode variants" (only the install-path
hardcodes D38/D39 are real), "babeld IPv6 redistribution gap", ZIP ':0' dial-target, and the
RightPanel '__manual__' no-op. Full list in the deep-audit output.

---

## Appendix B — Disposition of every finding (closure record, 2026-06-07)

Program: `implementation_plans/audit-remediation-and-allocation-stability-2026_06_07/`.
Stacked PR chain: #3 (specs+CI+plan-1.5) ← #4 ← #5 ← #6 ← #7 ← #8 ← #9 ← #10 ← #11 ← #12.

| Disposition | Findings |
|---|---|
| **PR #3** (plan-1.5 insertions) | D18 (pulled forward from Plan 9; ESLint independently flagged it) |
| **PR #4** — port ownership | D1, D8, D9, D19, D21, D22, D23, D51, D57, UX-2 |
| **PR #5** — compile feedback + hardening | UX-1, D4, D20, D33, D34, D35, D42, D56, D60 (+ CJK string restoration) |
| **PR #6** — naming & deploy identity | D3, D5, D13, D14, D31, D32, D36, D61 (+ FE interface-name reconstruction spec-gap) |
| **PR #7** — security & integrity | D7, D11, D15, D16, D24, D25, D43, D44, D47 |
| **PR #8** — routing & Babel | D2, D30, D40, D49, D50, D63, D72, D73, D78; **D41 narrowed**: extra_prefixes + gateway default fixed via non-local `redistribute ip`; domain-CIDR aggregate **deferred to plan-6.5** (no kernel route exists; pre-declared stop-loss) |
| **PR #9** — sticky-pin allocation | T13 (growth matrix), D12, D38, D39, D48, D70; invariants I1–I10 implemented, G4 property gate perpetual |
| **PR #10** — shared entrypoint + install robustness | D6, D27, D28, D29, D52, D53, D59, D76 |
| **PR #11** — wire contract + frontend state | D10, D17, D26, D37, D45, D46, D54, D55, D58, D62, D64, D65, D66, D67, D68, D69, D74, D75, D77 |
| **PR #12** — bridging UX + docs sync | UX-3, UX-4, UX-5, UX-6, T14, D71 |
| **Deferred by decision** | route_policies implementation (Decision 2 — reserved, validator rejects non-empty); domain-CIDR aggregate announcement (plan-6.5 marker); additive-apply installer + per-node deploy selector + IPv6 overlay (Decision 10 — future subjects) |
| **Refuted (never bugs)** | the 13 claims in Appendix A — no action taken, by design |

Every confirmed finding D1–D78 and UX-1–6 is accounted for above; none silently dropped
(closure criterion 3 of the outline).
