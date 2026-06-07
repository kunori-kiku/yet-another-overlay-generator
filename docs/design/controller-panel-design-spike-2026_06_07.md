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
- **Trust-list / membership changes require a human hardware-key signature over the change content**
  (content-bound, verified against a public key pinned at install). The controller has no autonomous
  ability to admit/evict/rekey a node, so a headless breach cannot alter fleet membership — the
  catastrophic slice of the live-breach window is closed, not merely bounded. Routine config keeps
  the automated KMS key + step-up promote (two-tier; see resolved decision #1).
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

- **Live-breach signing window (now narrowed to routine config only).** With the two-tier model,
  **membership/trust-list changes are NOT in this window** — they require a human hardware-key
  signature the controller can't produce, so a headless breach cannot admit a rogue node or redirect
  trust. The residual is confined to **Tier B routine config**: a live breach could KMS-sign and
  promote a malicious *routine* recompile (e.g. shift a port/cost) for already-trusted nodes during
  the breach. KMS stops offline forgery; step-up promote + anti-rollback + instant rollback + audit
  alarms bound it; the air-gap path is the full escape. Lower stakes than membership and consciously
  accepted as the price of a one-click routine deploy.
- Signing-input determinism (must sign canonical rendered bytes, not `computeChecksum`).
- Split-render private-key leak regression (CI guard).
- Multi-tenant isolation regression (was structurally impossible; now a per-request authz check).
- Agent supply chain (root on every node of every tenant — maximal blast radius).
- Enrollment-token theft before redemption (bounded by single-use + TTL + scope + audit).
- No controller-side key recovery (lost node disk → re-enroll, not recompile-from-JSON).

## Resolved decisions (2026-06-07, user-confirmed)

1. **Promotion gate — two-tier, with a hardware-signed membership tier.** Split updates by stakes:
   - **Tier A — membership / trust-list changes** (admit, evict, rekey a node — i.e. changing *who*
     is trusted): require a **human hardware-key signature over the change content itself** (a hash
     of the canonical trust-list bytes + monotonic version). The agent verifies it against the
     hardware key's public key **pinned at install time** (under the at-deployment-safety
     assumption). Because the signature is content-bound and the token requires a human touch, a
     **headless controller breach cannot forge or alter fleet membership at all** — this closes the
     catastrophic slice of the live-breach window. The hardware key may be a PIV/PGP token (signs
     content directly) or a WebAuthn/passkey (signs a challenge committing to the content hash).
     This supersedes a mere step-up *auth* gate: the controller has **no autonomous capability** to
     change membership.
   - **Tier B — routine config recompiles** (ports, MTU, babel costs, mimic/xdp): signed by the
     automated per-tenant KMS key + anti-rollback, with a **WebAuthn/FIDO step-up on promote** by
     default. Because FIDO is not universal (headless ops, some orgs/hardware), a tenant MAY fall
     back to one-click promote for Tier B — but ONLY behind a **loud, explicitly-acknowledged
     warning** that it forfeits breach-containment for routine changes, compensated with
     **mandatory loud promote audit-alarms**. Tier A never has a one-click fallback.

   Rationale: membership changes are rare and catastrophic if forged (a rogue node admitted, trust
   redirected) → worth a human hardware touch every time; routine config is frequent and lower-stakes
   → keep it usable. (User refinement, 2026-06-07: hardware-*sign the content*, don't unlock an OTP —
   an OTP proves hardware-touch but isn't bound to the update's content and leaves a replayable
   cleartext secret on the host; a content-bound signature has neither flaw.)
2. **Existing-fleet migration: zero rotation.** The agent reads the key already in `/etc/wireguard`
   and publishes only the public half (I5 zero-rotation on-ramp inverted). Fleet-wide rotation stays
   available as an explicit operator choice.
3. **Signing keys: per-tenant + trust-set pinning, with TWO roles.** (a) An automated **config-signing
   key** in per-tenant KMS for Tier-B routine recompiles; (b) a **membership-signing hardware key**
   (human-held token) for Tier-A trust-list changes. Both are per-tenant (cross-tenant bundles can't
   verify) and both are pinned as trust *sets* so rotation = add-new / overlap / retire without
   re-pinning every node. The two roles are distinct keys so the automated key can never authorize a
   membership change.
4. **Agent auto-update: opt-in staged + dual-control signing.** Operator-approved canary→fleet
   rollout; release-signing key separate from the config-signing key, under dual control;
   verify-before-update; no silent fleet-wide root-code update. Security updates surfaced prominently
   to offset patch lag.
5. **Availability/degradation: keep last-good, fail-closed on new applies.** Controller-unreachable
   → node keeps running its last-good config (an outage must never brick a working fleet). A new
   bundle failing signature/rollback/expiry is refused. Signature expiry gates *new* applies, not
   the already-running tunnel.
6. **Data-at-rest: standard at-rest for v1.** The DB holds only public keys + topology (no private
   keys, by design); per-tenant envelope encryption is a later upgrade when an enterprise/compliance
   deal requires it.
7. **Revocation: short-lived agent certs + immediate overlay-layer eviction.** Quarantine = the
   controller stops distributing the node's public key to its peers (cut off at next pull, cert
   state aside). No CRL/OCSP — unnecessary for a pull-only fleet.

## Out of scope (v1)

Constrained Go reconciler replacing `install.sh` (defer; v1 runs the audited install.sh verbatim);
automatic NAT traversal (keep declarative public_endpoints + mimic); embedding/forking
Headscale/Netbird/Tailscale (borrow patterns, not runtimes — they'd replace the unique Babel/per-peer/
mimic compiler); browser-side keygen (keys are agent-side); any change to compiler/renderer purity or
deps (frozen; new deps quarantined in controller/agent); billing/quota/onboarding commerce;
MagicDNS/ACL-policy-engine mesh scope creep.
