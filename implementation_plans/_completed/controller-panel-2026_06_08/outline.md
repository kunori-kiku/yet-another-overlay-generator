# Outline — controller-panel (2.0 program) — 2026-06-08

## 1. Mission

Evolve YAOG into an **agent-pull controller panel**: a Deploy button distributes WireGuard+Babel
configs to nodes with no local `deploy.sh`, for hosted multi-tenant fleets — while **preserving the
air-gapped path byte-for-byte** and keeping the compiler/renderer frozen. Full architecture + threat
model: [`docs/design/controller-panel-design-spike-2026_06_07.md`](../../docs/design/controller-panel-design-spike-2026_06_07.md).

Success criteria:
- Nodes generate their own keypairs; the controller is **zero-knowledge of WG private keys** (stores
  public keys + config only).
- Configs are **Ed25519-signed and verified before root apply**; routine config uses an automated
  per-tenant key, **membership/trust-list changes require a human hardware-key signature** the
  controller can't forge.
- The **air-gap `deploy.sh`/bundle path is unchanged** and remains the zero-standing-access mode.
- Each phase ships incrementally to `main` as new opt-in packages; 1.x stays releasable throughout.

## 2. Principles (subject-specific; inherits /PRINCIPLES.md)

- **[STATED, HIGH] Air-gap path frozen.** The compiler, renderers, `deploy.sh`, and bundle export
  render byte-for-byte as today. Violation: refactoring the export/render path to share the
  controller's stateful code; changing `GenerateKeys` case-b behavior on the non-custody path.
- **[STATED, HIGH] Zero-knowledge key custody.** No WG private key ever reaches the controller, its
  DB, or its bundles. Violation: a split-render bug that lets a real private key into a controller
  bundle (guarded by a perpetual CI check).
- **[STATED, HIGH] Authenticity before root apply.** An agent applies config only after verifying an
  Ed25519 signature over a *canonical* bundle serialization (NOT `compiler.go computeChecksum`).
  Membership changes additionally require a hardware-key signature. Violation: trusting hash-only;
  signing the non-canonical `%v` checksum.
- **[STATED, HIGH] Tenant isolation is structural.** `tenant_id` derives from the authenticated
  principal (never a request param), enforced at one chokepoint; per-tenant signing keys mean a
  cross-tenant bundle cannot even verify. Violation: a tenant scope missing on any agent endpoint.
- **[INFERRED, HIGH] Generated/agent code runs as root.** The agent and install.sh obey the existing
  shell-escaping + integrity discipline; no silent fleet-wide root-code auto-update.
- **[INFERRED, MED] New deps + state quarantined.** Postgres/OIDC/KMS clients live only in
  `internal/controller` / `cmd/agent`; the compiler/renderer dep set stays frozen.
- **[STATED, MED] Honest residual.** Agent-pull is not zero-standing-access; a live controller breach
  can sign+promote *routine* config during the breach. Bounded, documented, escapable via air-gap.

## 3. Current state of the world (2026-06-08)

- `main` after v1.3.2 (mimic xdp_mode override). Three subjects shipped this program era:
  audit-remediation (v1.3.0), parallel-links (v1.3.0), mimic-tcp-transport (v1.3.1/1.3.2).
- Stateless compiler; per-peer WG interfaces; sticky-pin allocation; signed-ish integrity chain
  (`checksums.sha256` incl. install.sh; self-extracting installer payload SHA-256).
- No controller, no agent, no DB, no server-side state. Design spike complete + decision-complete.

## 4. Must-read references

Design: `docs/design/controller-panel-design-spike-2026_06_07.md` (architecture, threat model, all
decisions). Memory: [[mimic-tcp-transport-closed]], [[parallel-links-subject-closed]],
[[audit-plan-pipeline-state]] (method + stacked-PR merge gotchas).

Code anchors (verified 2026-06-08):
- `internal/artifacts/export.go:31-169` (bundle assembly; `checksums.sha256` at 101-126)
- `internal/compiler/compiler.go:211-215` (`computeChecksum` — DO NOT sign this)
- `internal/render/render.go:38-86` (`GenerateKeys` cases a/b/c at 44/55/63; `All` at 86)
- `internal/renderer/wireguard.go:56-57,93-94` (`[Interface] PrivateKey` — the split point),
  `:18,116,134,155` (config structs)
- `internal/renderer/script.go:271-274,906-909` (install.sh `sha256sum -c`)
- `internal/api/handler.go:439-475` (self-extracting installer + `EXPECTED_PAYLOAD_SHA256`)
- `internal/api/server.go` (wrap/recover/timeouts to reuse), `cmd/server`, `cmd/compiler`
- `go.mod` (module path, Go 1.25, wgctrl)

Specs to amend: `docs/spec/artifacts/export-bundle.md`, `install-script.md`, `security/security.md`,
`api/wire-contract.md`, and new `docs/spec/controller/*` (added per phase).

Web (current): Tailscale/Headscale + Netbird agent-pull/enrollment patterns (borrow, don't embed);
WireGuard key-handling; Ed25519 (stdlib `crypto/ed25519`).

## 5. Standing rules

Per `/PRINCIPLES.md` + memory. Stacked-PR discipline: retarget-child→merge-parent→delete-branch
(merge gotcha in memory). Workflows ≤5 agents, disjoint partitions, seam-verify, independent review,
**user-gated merge + release**. Real-host smoke required before any apply-path phase.

## 6. Decisions log

Design decisions (frozen in the design doc, 2026-06-07): agent-pull; augment (air-gap intact);
hosted multi-tenant; zero-knowledge custody (public-key-only registration); two-tier signing
(routine = KMS + step-up promote with warned one-click fallback; membership = human hardware-key
signature over content); standard at-rest v1; per-tenant + trust-set pinning; opt-in staged agent
updates; keep-last-good/fail-closed; short-lived certs + overlay eviction (no CRL/OCSP).

Planning decisions (2026-06-08): one subject `controller-panel`; near phases detailed (P0+P1), far
milestone-level (P2/P3); incremental-additive to main (controller = new opt-in packages, 1.x stays
releasable); out-of-scope confirmed (reconciler/NAT-traversal/embed-Headscale/browser-keygen/
compiler-purity-change/commerce/MagicDNS).

## 7. Milestones

### Plan 1 — Phase 0: sign the existing bundle path → `plan-1-2026_06_08.md`
Goal: end-to-end authenticity on the bundle, standalone, no architecture change. Canonical bundle
serializer + Ed25519 detached signature (stdlib) + install.sh verifies against a Go-emitted pinned
key. Hazards: signing a non-canonical/non-deterministic artifact. Gate: canonical-serializer golden
test + install.sh verify test, CI green. Stop-loss: signing opt-in (hash-only still works).

### Plan 2 — Phase 1a: split-render + custody mode → `plan-2-2026_06_08.md`
Goal: controller renders the fleet from public keys only; private keys never needed server-side.
Custody-mode flag; `PRIVATEKEY_PLACEHOLDER`; case-b becomes supported path; no-private-key CI guard.
Hazard: leaking a private key into a custody bundle, or drifting the air-gap path. Gate: byte-diff
(controller output == air-gap output except the placeholder line) + CI guard. Stop-loss: agent
templates the whole `[Interface]` locally.

### Plan 3 — Phase 1b: node agent → `plan-3-2026_06_08.md`
Goal: a node agent that local-keygens, pulls its own signed bundle, verifies, splices its key, runs
install.sh, reports. Single-tenant, static config source, no enrollment yet. Hazard: turning the
agent into a second source of truth. Gate: real-host smoke (pull→verify→apply→report). Stop-loss:
keep the agent a thin install.sh wrapper.

### Plan 4 — Phase 2: enrollment + mTLS + persistence + deploy state + frontend → `plan-4-2026_06_08.md`
Milestone-level. Postgres registry (public-key-only), single-use enrollment tokens + PoP, per-node
mTLS, controller TLS 1.3, anti-rollback, long-poll generation, frontend Deploy/status/enrollment.
Detailed into sub-plans when Plan 3 lands.

### Plan 5 — Phase 3: hosted multi-tenant + KMS + stage/promote + OIDC → `plan-5-2026_06_08.md`
Milestone-level. Per-tenant KMS config-signing + hardware-key membership signing; structural tenant
isolation + cross-tenant CI gate; OIDC + RBAC; stage→promote (step-up + warned one-click fallback +
hardware-signed membership + rollback); supply-chain hardening. The security-load-bearing phase; last.

## 8. Insertion-point markers

- **plan-1.5** — canonical serializer reveals a renderer-output non-determinism that must be fixed
  before signing is trustworthy.
- **plan-3.5** — agent apply-path hardening if real-host testing shows install.sh-as-apply is
  insufficient (the curl|bash root surface).
- **plan-4.x / plan-5.x** — Phase 2/3 detailed into numbered sub-plans when prerequisites land.
- **plan-N.5 (any time)** — design conflict surfaces; the frozen design doc + this outline win.

## 9. Closure criteria

- [ ] P0–P3 phases shipped to main, CI green; 1.x remained releasable throughout.
- [ ] Perpetual gates live: no-private-key custody guard; cross-tenant access gate; canonical-
  serializer golden; install.sh signature-verify.
- [ ] Air-gap path proven byte-identical (diff test) across all phases.
- [ ] Real-host smoke recorded for each apply-path phase.
- [ ] Design doc + specs consistent with shipped behavior; PRINCIPLES.md principle-changes recorded.
- [ ] STATUS refreshed; subject archived to `_completed/`.

## 10. Plan status table

| Plan | Status | PR | Notes |
|---|---|---|---|
| plan-1 (P0 signing) | pending | — | detailed; ships standalone |
| plan-2 (P1a split-render) | pending | — | detailed |
| plan-3 (P1b agent) | pending | — | detailed |
| plan-4 (P2 enrollment/persistence) | pending | — | milestone-level; detail when plan-3 lands |
| plan-5 (P3 multi-tenant/KMS) | pending | — | milestone-level; detail when plan-4 lands |
