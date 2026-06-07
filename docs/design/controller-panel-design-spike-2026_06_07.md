# Design spike — hosted controller panel (agent-pull, zero-knowledge key custody)

<!-- design spike, 2026-06-07 — PRE-DECISION. Not a contract; open decisions listed at the end. -->

A design-only exploration of evolving YAOG from a config generator (+ local `deploy.sh`) into a
**controller panel** where a Deploy button pushes configs to nodes. Produced by a 6-agent spike
(3 architecture tracks + threat model + codebase-fit + synthesis). Full per-track analysis lives in
the workflow transcript (`wf_c640acd0-c5c`); this doc is the synthesis.

## User-confirmed decisions (the design is anchored on these)

- **Control model: agent-pull** — a node agent authenticates *outbound* to the controller, pulls
  only its own config, applies it. Not SSH-push.
- **Augment, not replace** — the air-gapped `deploy.sh` / bundle-export path stays byte-for-byte
  intact as the zero-standing-access mode; the controller is an *additional* deploy mode.
- **Hosted multi-tenant SaaS** — one service serves many independent fleets (the high-bar case).
- **Signed configs** — agents verify a signature before applying.
- **Zero-knowledge key custody** (user's proposal, now fixed): nodes generate their WireGuard
  keypair locally and register only the **public** key; the controller never holds private keys.

## Recommended architecture

A **zero-knowledge agent-pull controller wrapped around the existing stateless compiler**, with the
compiler core and the air-gap path left byte-for-byte intact.

The key enabler — verified in the code: a node's WireGuard **private key appears in exactly one
place**, its own `[Interface] PrivateKey` line (`internal/renderer/wireguard.go`); every `[Peer]`
section references only public keys. So the controller can compile and render the *entire* fleet
from public keys alone, emitting a `PRIVATEKEY_PLACEHOLDER` in each node's own `[Interface]`. The
agent generates its keypair locally, keeps the private key in `/etc/wireguard` (0600, where it
already lives), and **splices it into the placeholder at apply time.** This resolves the I5
private-key round-trip cleanly: I5's *guarantee* (stable key, identified by public key) is preserved
by persisting only the public key; I5's *mechanism* (private key in JSON) is downgraded to
public-key-only for controller fleets. Existing fleets migrate with **zero rotation** — the agent
reads the key already in `/etc/wireguard` and publishes only its public key.

Components (all new code quarantined from the frozen compiler/renderer):
1. **Controller API** — new tenant-scoped routes beside today's untouched `/api/compile|export|deploy-script`,
   reusing the existing server scaffolding. Operator routes (OIDC) + agent routes (mTLS):
   `/enroll`, `/poll` (long-poll, monotonic generation), `/config` (own signed bundle only), `/report`.
2. **Persistence wrapper** (new — deliberately breaks "no DB" *at the service boundary only*): one
   Postgres, every row keyed by `tenant_id` derived from the authenticated principal (never a request
   param), single data-access chokepoint. Stores public-keys-only topology, agent registry,
   staged+current signed bundles, hash-chained audit log. **Not a private-key vault.**
3. **Split-render shim** (new, thin) — wraps `render.All` to emit `PRIVATEKEY_PLACEHOLDER`; the
   `GenerateKeys` public-key-present/private-absent case (today a hard error) becomes the normal
   controller path behind an explicit custody-mode flag, leaving the air-gap path byte-identical.
4. **Node agent** (new Go static binary, `cmd/agent`) — local keygen via wgctrl, enroll with public
   key only, outbound mTLS, long-poll, pull own signed bundle, verify Ed25519 against a trust anchor
   **pinned at install time**, splice local key, run the **existing `install.sh` verbatim**, report.
5. **Signing** — per-tenant Ed25519 key in **KMS/HSM as a non-exportable sign-only handle** (not in
   the controller process, not in the DB). Signs a **canonical serialization of the per-node rendered
   bundle bytes + a bound header `{tenant_id, node_id, version, expiry}`** — NOT the existing
   `computeChecksum` (`fmt.Sprintf %v`, non-canonical, unsafe to sign).

Deploy flow: **stage → promote.** Operator edits topology → Compile/Stage (runs the unchanged
pipeline in public-key mode, KMS-signs each changed node's canonical bundle, stores as *staged*,
shows exact per-node diff — exact because bundles are byte-stable per I1/I4) → review → **Promote**
(flips the current pointer; production: behind an out-of-band approval factor). Agents long-poll,
pull only their own signed bundle, verify signature + bound header + anti-rollback, splice key, run
`install.sh`, report. Rollback = promote a prior signed version.

## Non-negotiable security controls

- Zero-knowledge WG private-key custody + a **CI guard** asserting no controller-emitted bundle ever
  contains a parseable WireGuard private key (catches a split-render regression that reintroduces the vault).
- End-to-end Ed25519 signing verified **before root apply**, over a **canonical** bundle serialization
  (not `computeChecksum`), header binding `{tenant, node, version, expiry}`.
- Signing key in **KMS sign-only**, per-tenant — a memory-scrape breach can sign only while live and
  can never exfiltrate the key; a cross-tenant bundle can't verify against another tenant's anchor.
- **Stage → Promote with an out-of-band promotion factor** the breached web tier alone can't satisfy
  + instant rollback. (This is what keeps "breach ≠ fleet takeover".)
- Deny-by-default per-tenant authz: `tenant_id` from the authenticated principal only, one chokepoint,
  `/config` returns only the caller's node; cross-tenant access CI gate.
- Per-node mTLS device identity (outbound only); single-use, short-TTL, tenant+node-scoped enrollment
  tokens with proof-of-possession (CSR + agent-generated public key), atomically burned, operator-reviewable.
- Anti-rollback (refuse older versions / mismatched bound header).
- Agent supply chain: reproducible builds, hash-pinned deps, **release-signing key distinct from the
  config-signing key under dual control**, verify-before-update, no silent fleet-wide auto-update, SBOM.
- Hash-chained tamper-evident audit log (enroll/stage/sign/promote/apply).
- Real TLS 1.3 on the controller; new deps quarantined; signing uses stdlib `crypto/ed25519`.
- The air-gap `deploy.sh` path stays byte-for-byte as the high-assurance escape hatch.

## Phased program (security-first ordering; cheap wins first)

- **Phase 0 — Sign the existing bundle path** (no DB, no agent, no custody change). Lock a canonical
  per-node bundle serialization; add Ed25519 detached signing on export; install.sh verifies signature
  + SHA-256 against a Go-emitted pinned key. Strengthens *both* paths; stdlib only; no principle break.
- **Phase 1 — Split-render + agent-side custody** (single-tenant, read-only pull). Custody-mode flag,
  `PRIVATEKEY_PLACEHOLDER` shim + no-private-key CI guard, `cmd/agent` (keygen→pull→verify→splice→
  install.sh→report). Resolves the key-custody tension at lowest blast radius.
- **Phase 2 — Enrollment, mTLS, persistence, deploy state** (still single-tenant). Postgres registry,
  single-use enrollment tokens + PoP, per-node mTLS, controller TLS 1.3, anti-rollback, long-poll
  generation, frontend Deploy + status/enrollment UI.
- **Phase 3 — Hosted multi-tenant + KMS + stage/promote** (the high-bar, intentionally last).
  Per-tenant KMS signing, structural tenant isolation + cross-tenant CI gate, OIDC + RBAC, stage→promote
  with out-of-band approval + rollback, per-tenant audit export, supply-chain hardening.

## Top residual risks

- **Live-breach signing + promotion window** — the single largest deviation from today's "server
  compromise can't act on a node": a deep compromise of the *running* controller can request a valid
  signature AND flip the pointer during the breach. KMS stops offline forgery; the out-of-band
  promotion factor + anti-rollback + instant rollback + audit alarms bound it; the air-gap path is the
  full escape. **This residual power is the price of a controller and must be accepted consciously.**
- Signing-input determinism (must sign canonical rendered bytes, not `computeChecksum`).
- Split-render private-key leak regression (CI guard).
- Multi-tenant isolation regression (was structurally impossible; now a per-request authz check).
- Agent supply chain (root on every node of every tenant — maximal blast radius).
- Enrollment-token theft before redemption (bounded by single-use + TTL + scope + audit).
- No controller-side key recovery (lost node disk → re-enroll, not recompile-from-JSON).

## Open decisions for the user (needed before a plan)

1. **Promotion gate strength** — out-of-band/multi-party approval (keeps "breach ≠ fleet takeover")
   vs one-click automated promote (convenience, but collapses the guarantee to "breach = transient
   fleet control"). *Most consequential.*
2. **Existing-fleet migration default** — agent reads the live `/etc/wireguard` key (zero rotation)
   vs one fleet-wide rotation.
3. **Signing-key granularity + rotation** — per-tenant (recommended) vs per-fleet; cadence; single
   pinned key vs trust set.
4. **Agent auto-update policy** — silent (patching) vs operator-opt-in staged (supply-chain safety;
   recommended).
5. **Availability/degradation contract** — agent keeps last-good config on controller-unreachable /
   expired-signature, fail-closed on apply (recommended).
6. **Data-at-rest** — per-tenant envelope encryption for stored topologies/public keys vs standard
   at-rest encryption for v1.
7. **Revocation latency** — short-lived cert rotation vs CRL/OCSP for the pull-only fleet.

## Out of scope (v1)

Constrained Go reconciler replacing `install.sh` (defer; v1 runs the audited install.sh verbatim);
automatic NAT traversal (keep declarative public_endpoints + mimic); embedding/forking
Headscale/Netbird/Tailscale (borrow patterns, not runtimes — they'd replace the unique Babel/per-peer/
mimic compiler); browser-side keygen (keys are agent-side); any change to compiler/renderer purity or
deps (frozen; new deps quarantined in controller/agent); billing/quota/onboarding commerce;
MagicDNS/ACL-policy-engine mesh scope creep.
