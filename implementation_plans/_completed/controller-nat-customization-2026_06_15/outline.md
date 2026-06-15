# Subject: controller-nat-customization-2026_06_15

## Mission

After the live Cloudflare-fronted deployment debugging, controller mode has a cluster of gaps around
the **per-peer-interface port/IP model** and **NAT port-forwarding**. This subject makes the controller
the *visible, stable, authoritative* source of an edge's compiled ports and IPs, lets the operator
customize the NAT-relevant values per edge and have them **persist stickily** through compile → save →
deploy, and removes the now-meaningless per-node `listen_port`.

Grounded by workflow `w4i9cj6ay` (10 Opus agents: 4 investigate → 3 design → 2 critique → 1 synthesize)
plus the owner's answers to the three gating questions (2026-06-15).

## Principles (inherits PRINCIPLES.md; subject-specific)

- **Server-authoritative, operator-customizable at the NAT boundary (HIGH).** The server computes the
  authoritative allocation (keys-as-placeholder, transit IPs, link-locals, ports). The operator may
  override exactly the NAT-relevant fields per edge — external `endpoint_host:endpoint_port` and the
  internal listen port / transit IP — and those overrides must survive verbatim and be sticky-reused.
  *Violation example:* a force-Save that drops the server's pins so the next stage re-allocates and
  silently breaks a configured NAT forward.
- **Zero-knowledge custody never weakens for visibility (HIGH).** Any new compile/preview path uses
  `render.AgentHeld` (emits `PRIVATEKEY_PLACEHOLDER`), never `render.AirGap` (reconstructs real keys).
  *Violation example:* reusing `/api/compile` to populate the controller preview.
- **No meaningless knobs (MEDIUM).** Per-node `listen_port` has no effect in the per-peer model; it is
  removed outright, not deprecated-in-place. The sole internal auto-allocation base is the constant 51820.

## Current state

- main @ `b1417af` (post i18n+envelope + install-script cross-distro fix).
- Per-peer-interface model: each edge has its own WG interface + auto-allocated listen port; `node.ListenPort`
  was only the allocation floor (now being removed). `edge.EndpointPort` = external NAT dial; `alloc.toPort`/
  `fromPort` = internal listen; `edge.CompiledPort` = read-only echo. Pins (`pinned_*`) are sticky by edge ID.
- `persistAllocations` (controller/compile.go ~592) merges pins by edge ID into the stored topology and
  **skips byte-equal writes** (so re-deriving identical allocations persists nothing).

## Owner decisions log

- 2026-06-15 — KEEP `endpoint_port` (external NAT dial). Reversed earlier "remove endpoint_port".
- 2026-06-15 — Remove per-node `listen_port` outright, no migration ("just remove that place"); base=51820.
- 2026-06-15 — Compile button in controller "not actually read-only, but server-authoritative except the
  customized NAT ip:port": Compile → server allocates → operator edits ip:port + internal port per edge →
  Save → deploy adheres.
- 2026-06-15 — Operator-settable internal listen port: YES (NAT VPS forwards only a fixed port range, so the
  `>=51820` pin floor must relax). Auto-alloc still starts at 51820.
- 2026-06-15 — Persist the operator's per-edge IP choice (endpoint_host from target's public_endpoints;
  transit IP from the pool) stickily, same as ports.
- 2026-06-15 — Controller stays zero-knowledge (AgentHeld placeholder keys).
- 2026-06-15 — Legacy non-default base normalizes to 51820 (no live-fleet impact; pins are absolute).
- 2026-06-15 — SUBJECT DELIVERED & CLOSED: all 7 PRs (#98–#104) + the locator follow-up (#105) merged to
  main; reviewed all-Opus twice (fixes applied per-PR, re-review GO); main gates green. See README.md.

## Milestones (PR per milestone; verification gate each)

| PR | Title | Layer | Blocked by |
|----|-------|-------|-----------|
| PR1 | Post-deploy pin reconciliation + non-clobbering force-Save | frontend | — |
| PR2 | EdgeEditor directional NAT readout + role-change warning | frontend | — |
| PR3 | Remove per-node listen_port (backend) + narrow validateEffectivePortRanges | backend | — |
| PR4 | Remove per-node listen_port (frontend) | frontend | — |
| PR5 | Shared CompileSubgraph extraction (no behavior change) | backend | — |
| PR6 | Server-authoritative compile endpoint + Compile button + merge-to-canvas | both | PR5, PR1 |
| PR7 | Operator-settable internal port AND transit IP per edge + relax pin floor | both | PR6, PR2 |

Detail for each PR lives in `pr-N-*.md` (written as each PR is started) and in tasks #32–#38.

### PR1 — load-bearing (F1a + F2 stability + F4)
`controllerStore.deploy()`: after promote, re-GET server topology and MERGE `compiled_port` + all six
`pinned_*` (ports, transit IPs, link-locals) into existing canvas edges **by edge ID**, preserving
selection; full `hydrateFromServer` fallback only when the node/edge set diverged; re-base
`lastSyncedSnapshot` from the merged canvas so it reads clean. `saveDesign`: force-Save becomes
non-clobbering — when the only canonical diff is `pinned_*`/`compiled_port` the server HAS and the canvas
LACKS, merge the server pins into the canvas instead of conflict/blind-overwrite. Files:
`frontend/src/stores/controllerStore.ts`. Gate: `npm run lint && npm run build`; manual deploy round-trip.

### PR2 — directional NAT readout (F2/F5 presentation)
Replace the opaque `pinned_from → pinned_to` line with a directional readout: NAT target = listen port of
whichever end carries `endpoint_host` (to-node → `pinned_to_port`, from-node → `pinned_from_port`). Add a
role-change re-allocation warning. en.ts + zh.ts lockstep.

### PR3 — remove listen_port backend + critical validator narrowing (F3)
Delete `Node.ListenPort`; base=51820 in `lowestFreePort` (keep `*model.Node` param) + client wg0; delete
`validateListenPortConflicts` + schema range check + their codes; **delete the hostname-overlap rule in
`validateEffectivePortRanges` (semantic.go:382-403) + `CodeNodeEffectivePortRangeOverlap`** (always-fires
for co-hosted nodes under a uniform base — would block every multi-node-per-VPS deploy); keep overflow-only.
Do NOT touch render `PeerInfo`/`ClientPeerInfo` `ListenPort` (those are allocated ports). Heavy test churn.

### PR4 — remove listen_port frontend (F3)
Drop from `types/topology.ts`, `NodeForm`, `NodeEditor` (default → literal 51820), `AuditView` "listens on
port" line, `NODE_OMITEMPTY`, and i18n keys.

### PR5 — CompileSubgraph extraction
Extract the compile half of `CompileAndStage` into `controller.CompileSubgraph`; no behavior change.

### PR6 — server-authoritative compile (F1b)
`POST /api/v1/operator/compile-preview` → `CompileSubgraph` (AgentHeld; NO persist/stage side-effects) →
`CompileResponse` + `skipped_unenrolled`. Frontend Compile button merges the server allocation onto the
canvas (reusing PR1's merge); operator then overrides NAT ip:port + internal port; Save persists; deploy
adheres. Custody test: output carries only `PRIVATEKEY_PLACEHOLDER`, never a base64 private key.

### PR7 — operator-settable port + IP (F2)
EdgeEditor inputs for `pinned_from_port`/`pinned_to_port` AND `pinned_from_transit_ip`/`pinned_to_transit_ip`
(operator chooses firewall target port AND edge IP from the allocatable range); both persist stickily.
**Relax the pinned-port floor below 51820** so a port-restricted NAT VPS works (auto-alloc still 51820).
Client-side pair-completeness + transit-CIDR validation + a live validate before Save.

## Verification (subject-wide)

- Frontend gate: `cd frontend && npm run lint && npm run build` (no `npm test` exists).
- Backend gate: `go build ./... && go test ./internal/...` (CI; go at /snap/bin/go locally).
- PR3: a two-node-same-hostname topology validates clean (no overlap error); a stored design carrying
  `listen_port` compiles to identical pinned ports (pins absolute).
- PR6: preview writes no version/bundle/manifest/audit and emits no base64 private key.
- PR7: a paired `pinned_to_port=30050` + transit IP survives Save and is sticky-reused on the next deploy.

## Closure criteria

- All seven PRs merged; frontend lint+build + Go tests green.
- Manual NAT round-trip on the real deployment: Compile → see/edit internal port+IP → Save → deploy →
  the agent listens on exactly the chosen internal port/IP and the configured NAT forward stays valid
  across a re-deploy (no drift).
- No `listen_port` remains in model, validator, frontend, or i18n.

## Status table

| PR | Status | PR# / merge commit |
|----|--------|--------|
| PR1 | done — merged | #98 / `63ad33b` |
| PR2 | done — merged | #99 / `e4afe99` |
| PR3 | done — merged | #100 / `2ffe0e2` |
| PR4 | done — merged | #101 / `baca9a7` |
| PR5 | done — merged | #102 / `6cded0b` |
| PR6 | done — merged | #103 / `9e41716` |
| PR7 | done — merged | #104 / `899ff54` |
| follow-up | done — merged | #105 (effective-range-overflow error locator nit) |

All 7 PRs + 1 follow-up merged to main on 2026-06-15. Two all-Opus reviews: the comprehensive review
(`wpxkwp77i`, 49 agents) found 0 hard blockers + 5 should-fix + 3 nits — all fixed on their source PR
branches and propagated up the stack; the focused re-review of those fixes (`wlpo7gsfk`) returned **GO**.
Integrated main green: `go build`/`vet`/`test ./internal/...` + frontend `lint`+`build`.
