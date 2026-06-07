# Node Agent (Phase 1b — single-tenant custody+signing proof)

This document defines the **minimal single-tenant node agent** (`cmd/agent`). Its sole job is to
prove the custody+signing chain end-to-end on a real host: a node generates its own WireGuard
private key, the controller renders against the **public** key only ([key-custody.md](key-custody.md)),
the rendered bundle is **signed** ([signing.md](signing.md)), and the node verifies and applies it
without the private key ever leaving the host.

The agent is a **thin wrapper over `install.sh`**, not a reconciler. It pulls a signed placeholder
bundle, verifies it Go-side, and then hands off to the bundle's own `install.sh`
([../artifacts/install-script.md](../artifacts/install-script.md)), which re-verifies, splices the
locally-held private key, and brings the overlay up. There is no continuous reconcile loop, no daemon
state machine, no controller protocol — those arrive later (see [Deferred](#deferred) below).

## Lifecycle

The agent runs a single linear pass: **keygen → pull → verify → anti-rollback → apply → report.**
Each stage is fail-closed; a failure at any stage leaves the previous good install untouched
(see [Fail-closed and keep-last-good](#fail-closed-and-keep-last-good)).

### 1. keygen

On first run the agent generates a WireGuard keypair **locally** via
`golang.zx2c4.com/wireguard/wgctrl/wgtypes` (the one existing dependency; no new go.mod entry):

- The **private key** is written to **`/etc/wireguard/agent.key`** with mode **0600** — its base64
  text is exactly `wgtypes.Key.String()`. This is the only place the private key is ever persisted,
  and it never leaves the host.
- The **public key** is the only material the agent surfaces to the controller. In Phase 1b
  "registration" is out-of-band/manual (the operator copies the public key into the controller's
  topology); single-use enrollment tokens and proof-of-possession are deferred to Plan 4.
- keygen is **idempotent**: if `/etc/wireguard/agent.key` already exists and parses as a valid
  `wgtypes.Key`, the agent reuses it (so re-runs and existing-fleet migration require **zero key
  rotation** — the stable private key the node has always held remains its identity).

The controller stores **public keys only**; this is the persistence half of the zero-knowledge
guarantee that [key-custody.md](key-custody.md) renders against.

### 2. pull

The agent fetches **this node's** signed bundle from a static source — a directory or a plain HTTP
GET of the per-node bundle directory (the `checksums.sha256`, `bundle.sig`, rendered configs, and
`install.sh`). The bundle is the `AgentHeld` split-render: every `[Interface] PrivateKey =` line
carries `PRIVATEKEY_PLACEHOLDER`, never a real key. Phase 1b is **read-only pull**; authenticated
mutual-TLS pull, bound `{tenant,node,version,expiry}` headers, and long-poll change-notify are Plan 4.

### 3. verify (Go-side, fail-closed)

Before anything is applied, the agent verifies the bundle in-process, mirroring the install-time
order but with the **pinned** public key as the trust anchor:

1. Reconstruct the canonical `checksums.sha256` bytes and check `bundlesig.Verify(canonical, sig,
   pub)` where `sig` is the decoded `bundle.sig` and `pub` is the **pinned** Ed25519 public key
   (parsed from a PEM the agent holds, via `crypto/x509.ParsePKIXPublicKey`; see
   [signing.md](signing.md) for `MarshalPublicKeyPEM`/PEM form). The pin is held by the agent
   **independently of the bundle** — it is **not** the bundle's shipped `signing-pubkey.pem`, which an
   attacker rewriting the bundle could swap (the limitation called out in [signing.md](signing.md)).
2. Recompute each shipped file's SHA-256 and confirm it matches the signed `checksums.sha256` entry.

Any mismatch — bad signature, missing `bundle.sig`, or a file whose digest does not match — is a
**hard refusal**: the agent aborts before touching `/etc/wireguard`. A signed bundle with a missing
signature is treated as signature-stripping tamper, never as an unsigned bundle.

### 4. anti-rollback

The agent refuses to apply a bundle **older than the last successfully-applied one**, keyed on the
compiler's `compiled_at` (the bundle/manifest compile timestamp). The last-applied value is recorded
locally after a successful apply; on the next pull the agent compares and **refuses** any bundle whose
`compiled_at` is not strictly newer (equal is a no-op re-apply, which is safe and idempotent). Phase
1b ships this as a **stub** — a monotonic local high-water mark — because the cryptographically
**bound** anti-rollback (version + expiry inside the *signed* header, so a stale-but-validly-signed
bundle cannot be replayed) belongs to Plan 4. The stub establishes the contract and the on-disk state
shape so Plan 4 only has to move the comparison under the signature.

### 5. apply (hand off to install.sh — the agent does NOT splice)

On a verified, non-rolled-back bundle the agent runs the bundle's own **`install.sh`** as root. The
script — not the agent — performs the private-key splice. This split is **deliberate and
user-approved (2026-06-08)**:

> Phase 0 makes `install.sh` verify the **pristine signed placeholder bundle** (`bundle.sig` over
> `checksums.sha256`, then `sha256sum -c`) *before applying*. If the agent spliced the real private
> key into the bundle files **before** that verification, the spliced file's hash would no longer
> match the signed `checksums.sha256` and `sha256sum -c` would **fail**. If it spliced **after**
> verification it would mutate bundle bytes the next re-run re-verifies, breaking idempotency.

So the splice lives **inside `install.sh`**, custody-gated, and targets the **copied** confs in
`/etc/wireguard` — **not** the bundle confs:

- The signed bundle stays **pristine** on disk, so a re-run re-verifies the identical bytes and
  `sha256sum -c` keeps passing — apply is **idempotent**.
- During Phase 2 (config deployment) `install.sh` copies the WG confs into `/etc/wireguard`, then —
  only in `AgentHeld` custody — reads the node's private key from **`/etc/wireguard/agent.key`** and
  substitutes it for `PRIVATEKEY_PLACEHOLDER` in the **copied** confs. The `AirGap` `install.sh` has
  no splice block and stays **byte-identical** to today's air-gap output (the custody gate is what
  keeps the air-gap path frozen).
- `install.sh` then brings the overlay up (dummy0, WG interfaces, SNAT, Babel) as specified in
  [../artifacts/install-script.md](../artifacts/install-script.md).

The agent thus never reads, parses, or moves the private key during apply — it only ensures
`/etc/wireguard/agent.key` exists (from keygen) and invokes the script. Cross-references:
[key-custody.md](key-custody.md) (the placeholder contract), [signing.md](signing.md) (what is
signed and the verify order), [../artifacts/install-script.md](../artifacts/install-script.md) (the
splice step inside Phase 2 and the verify-before-apply order).

### 6. report

After apply the agent records the new last-applied `compiled_at` and reports outcome (success, or the
fail-closed reason) to its log / exit code. Phase 1b reporting is local only; pushing status back to
the controller (and the controller-side registry/UI) is Plan 4.

## Fail-closed and keep-last-good

Every stage is **fail-closed**: a failed verify, a rolled-back bundle, a missing `agent.key`, or a
nonzero `install.sh` exit aborts the pass **without** disturbing the currently-running overlay. The
agent never partially applies — verification is fully completed *before* `install.sh` is invoked, and
`install.sh` itself verifies the pristine bundle again before Phase 2 touches `/etc/wireguard`. The
result is **keep-last-good**: on any failure the node keeps the configuration from its last
successful apply. (Automated *rollback to a prior signed bundle* and instant fleet-wide rollback are
Plan 4/Plan 5; Phase 1b only guarantees it does not break what is already working.)

## The thin-wrapper boundary

What the agent **is** (Phase 1b):

- local keygen + private-key custody at `/etc/wireguard/agent.key` (0600);
- a single keygen→pull→verify→anti-rollback→apply→report pass;
- Go-side `bundlesig.Verify` + per-file SHA-256 against a **pinned** public key;
- a monotonic anti-rollback high-water mark on `compiled_at`;
- delegation of all config application to the bundle's `install.sh`.

What the agent is **not**: it owns no rendering, no routing, no WireGuard/Babel control-plane logic,
and no continuous reconciliation. It deliberately reuses `install.sh` so the proof exercises the
**same** artifact the air-gap path ships, with custody as the only divergence.

## Deferred

Explicitly **out of scope** for Phase 1b, by program plan:

- **Plan 4** — enrollment (single-use tokens + proof-of-possession), per-node **mTLS** and
  controller TLS 1.3, controller-issued tokens, **Postgres** persistence (public-keys-only registry),
  **long-poll** change-notify, status reporting, the **frontend** controller panel, and bound-header
  (signed `{tenant,node,version,expiry}`) anti-rollback replacing the stub.
- **Plan 5** — **multi-tenant** isolation, per-tenant **KMS** sign-only key handles, **stage→promote**
  with out-of-band approval + instant rollback, and **hardware-signed membership** (WebAuthn/FIDO
  step-up on trust-list changes).

## Verification

The end-to-end proof is a **real-host two-node smoke test** — two hosts each run keygen, pull their
signed bundle, verify, apply via `install.sh`, and confirm the overlay comes up (each node splices its
own `/etc/wireguard/agent.key`, the placeholder bundles stay pristine, and `ping` across the overlay
succeeds). This is a **manual gate**: it requires two real hosts with kernel WireGuard and root, so it
**cannot be run in CI**. CI continues to enforce the custody guard and the AgentHeld↔AirGap diff
([key-custody.md](key-custody.md)) and the signing tests ([signing.md](signing.md)); the on-host
smoke is the human sign-off that the chain those gates protect actually holds on a live host.
