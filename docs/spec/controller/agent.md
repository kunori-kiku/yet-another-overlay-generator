# Node Agent (Phase 1b static-source + Phase 2c-c controller mode)

This document defines the single-tenant node agent (`cmd/agent`). It has **two modes** that share the
same custody, verify, and apply core:

- **Static-source mode (Phase 1b — air-gap).** The agent pulls a signed bundle from a directory or a
  plain HTTP GET, verifies it, and hands off to `install.sh`. There is no controller protocol — this is
  the mode an air-gap operator uses (and it is what the rest of this document's Phase 1b sections
  describe). This content is **unchanged**; see [Static-source mode (Phase 1b)](#static-source-mode-phase-1b)
  below.
- **Controller mode (Phase 2c-c — plan-4.3c).** The agent **enrolls** with the networked controller
  ([controller-api.md](controller-api.md), [enrollment.md](enrollment.md)) over TLS, then runs a
  continuous **mTLS** control loop: long-poll for a new generation, fetch the signed bundle over
  `/config`, **reuse the same verify + apply** as Phase 1b, then report what it applied. This closes the
  single-tenant control loop end to end. See [Controller mode (Phase 2c-c)](#controller-mode-phase-2c-c)
  below.

The common thread across both modes: the agent's sole job is to prove the custody+signing chain
end-to-end on a real host — a node generates its own WireGuard private key, the controller renders
against the **public** key only ([key-custody.md](key-custody.md)), the rendered bundle is **signed**
([signing.md](signing.md)), and the node verifies and applies it without the private key ever leaving
the host. The agent is a **thin wrapper over `install.sh`**, not a reconciler: it verifies a bundle
Go-side and then hands off to the bundle's own `install.sh`
([../artifacts/install-script.md](../artifacts/install-script.md)), which re-verifies, splices the
locally-held private key, and brings the overlay up. Controller mode adds the network protocol (enroll
+ poll/config/report) **around** that core; it does **not** change the verify or apply step — the agent
reuses Plan 1b's `VerifyBundle` + `install.sh` hand-off **verbatim** (see
[The thin-wrapper boundary](#the-thin-wrapper-boundary)).

## Static-source mode (Phase 1b)

The sections from [Lifecycle](#lifecycle) through [The thin-wrapper boundary](#the-thin-wrapper-boundary)
describe the **static-source / air-gap** mode. Controller mode reuses this mode's `verify` →
`anti-rollback` → `apply` → `report` stages over a different transport; the only mode-specific parts
are the **source** (a `ControllerClient` instead of a directory/HTTP GET) and the **enroll** bootstrap
that precedes the loop. Read this first, then [Controller mode (Phase 2c-c)](#controller-mode-phase-2c-c).

## Lifecycle

The agent has two subcommands. **`keygen`** is a one-time prerequisite that establishes the node's
local key (below). **`run`** then performs a single linear pass: **pull → verify → anti-rollback →
apply → report.** Each stage is fail-closed; a failure at any stage leaves the previous good install
untouched (see [Fail-closed and keep-last-good](#fail-closed-and-keep-last-good)). `run` does **not**
itself keygen — the operator runs `keygen` first (it is idempotent); if `/etc/wireguard/agent.key` is
absent when a custody bundle is applied, `install.sh` fails closed.

### 1. keygen (prerequisite subcommand)

Running `agent keygen` generates a WireGuard keypair **locally** via
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

In the full system the controller will store **public keys only** (the persistence half of the
zero-knowledge guarantee that [key-custody.md](key-custody.md) renders against); that registry is
Plan 2. In Phase 1b registration is the manual out-of-band step above.

### 2. pull

The agent fetches **this node's** signed bundle from a static source — a directory or a plain HTTP
GET of the per-node bundle directory (the `checksums.sha256`, `bundle.sig`, rendered configs, and
`install.sh`). The bundle is the `AgentHeld` split-render: every `[Interface] PrivateKey =` line
carries `PRIVATEKEY_PLACEHOLDER`, never a real key. Phase 1b is **read-only pull**; authenticated
mutual-TLS pull, bound `{tenant,node,version,expiry}` headers, and long-poll change-notify are Plan 4.

The agent fetches `<source>/<--node-id>/` and, after verifying, cross-checks the bundle's
`manifest.json` `node_id` against `--node-id` (a mismatch is refused, so a misconfigured or malicious
source cannot get the agent to apply another node's bundle). **Operator note (Phase 1b layout):** the
air-gap export currently names each node's bundle directory by the node's *name* (`node.Name`) while
`manifest.json` records `node_id` (`node.ID`); since `--node-id` keys both the fetch path and the
`node_id` cross-check, the operator must stage the static source with each bundle under a directory
matching its `manifest.json` `node_id` (i.e. `node.ID`). Unifying the export directory name on
`node.ID` end-to-end is a Plan 4 cleanup, when the controller owns the serving layout.

### 3. verify (Go-side, fail-closed)

Before anything is applied, the agent verifies the bundle in-process, mirroring the install-time
order. The verifying key is chosen by policy:

1. Reconstruct the canonical `checksums.sha256` bytes and check `bundlesig.Verify(canonical, sig,
   pub)` where `sig` is the decoded `bundle.sig` and `pub` is:
   - **With an operator-pinned key** (`--pubkey`, parsed via `crypto/x509.ParsePKIXPublicKey`; see
     [signing.md](signing.md) for the PEM form): the pin is the trust anchor, held by the agent
     **independently of the bundle** — *not* the bundle's shipped `signing-pubkey.pem`, which an
     attacker rewriting the bundle could swap. Pinning is what makes the signature an **authenticity
     (provenance)** check and is **strongly recommended** for any custody (AgentHeld) fleet.
   - **Without a pin but with a signature**: verified against the bundle's own `signing-pubkey.pem`
     (trust-on-first-supply — proves internal consistency, not provenance; the limitation in
     [signing.md](signing.md)).
   - **Without a pin and without a signature**: treated as unsigned — only per-file hashes are checked
     (back-compatible). When a key **is** pinned, an unsigned bundle is **refused**.
2. Recompute each shipped file's SHA-256 and confirm it matches the signed `checksums.sha256` entry;
   `install.sh` must be present and covered.

Any mismatch — bad signature, a **pinned-but-unsigned** bundle, or a file whose digest does not match
— is a **hard refusal**: the agent aborts before touching `/etc/wireguard`. (Inside `install.sh`, a
signed bundle with a missing `bundle.sig` is likewise treated as signature-stripping tamper.)

### 4. anti-rollback

The agent refuses to apply a bundle **older than the last successfully-applied one**, keyed on the
compiler's `compiled_at` (the bundle/manifest compile timestamp). The last-applied value is recorded
locally after a successful apply; on the next pull the agent compares and **refuses** any bundle whose
`compiled_at` is not strictly newer (equal is a no-op re-apply, which is safe and idempotent). Phase
1b ships this as a **stub** — a monotonic local high-water mark. **Honest limitation:** `compiled_at`
lives in `manifest.json`, which export deliberately leaves **out** of the signed/checksummed set, so
the stub guards only against an **honest source accidentally serving a stale bundle** — **not** an
active attacker or MITM, who could forge `compiled_at` (the manifest is unsigned) to force a rollback
to any previously-signed bundle. Cryptographically **bound** anti-rollback (version + expiry inside
the *signed* header, so a stale-but-validly-signed bundle cannot be replayed) belongs to Plan 4; the
stub establishes the contract and the on-disk state shape so Plan 4 only has to move the comparison
under the signature.

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

The agent thus never reads, parses, or moves the private key during apply — it relies on
`/etc/wireguard/agent.key` existing (created by the separate `keygen` step; `install.sh` fails closed
if it is absent) and invokes the script. Cross-references:
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

# Controller mode (Phase 2c-c)

Controller mode (plan-4.3c) connects the agent to the **networked controller** of
[controller-api.md](controller-api.md). It does **not** fork the agent: it adds a network transport in
front of the **same** verify+apply core the static-source mode uses, plus an `enroll` bootstrap that
establishes the agent's mTLS identity. The agent still never renders, never routes, and never splices —
`install.sh` still owns the custody-gated splice ([key-custody.md](key-custody.md),
[../artifacts/install-script.md](../artifacts/install-script.md)).

There are two cooperating pieces:

- **`internal/agent/controller_client.go`** — a `ControllerClient{baseURL, caPEM, clientCert}` over
  `net/http` + `crypto/tls` (stdlib only, no new `go.mod` dependency). It speaks the four
  `/api/v1/controller/` routes the agent needs: `Enroll` (certless TLS), `Poll`, `Config`, and `Report`
  (all mTLS). It implements the existing `agent.Source` (and `Reporter`) so the shared `Run` loop
  drives it the same way it drives `DirSource`/`HTTPSource`.
- **two `cmd/agent` subcommands** — `enroll` (the one-time bootstrap) and `run --controller …` (the
  control loop). They are documented below.

## Bootstrap trust — the out-of-band CA pin

The agent's **first** call is `/enroll`, which it must make over TLS **before** it holds anything the
controller issued — so it has no way to validate the controller's TLS **server** cert from the
controller itself. The resolution (adopted in plan-4.3c) is an **out-of-band CA pin**:

- The operator hands the agent the controller's **CA cert PEM** out-of-band — alongside the single-use
  enrollment token and the node id — and the agent is configured with **`--controller-ca <pem-path>`**.
- The enroll TLS handshake trusts **only** that pinned CA as its `RootCAs` (and presents **no** client
  cert — the certless shape `/enroll` accepts under the server's `VerifyClientCertIfGiven`; see
  [controller-api.md](controller-api.md) §The TLS 1.3 + mTLS model).
- The enroll **response** carries `ca_cert_pem`. The agent **MUST** check that it equals the pinned CA
  cert, byte-for-byte; **a mismatch is a hard refusal** (the agent aborts enrollment and writes
  nothing). This closes the loop: the agent only ever trusts one CA, and it refuses to proceed if the
  controller hands back a different anchor than the one the operator pinned.

**Operational cost — ephemeral CA.** The controller's `DevCA` is **ephemeral**: its key lives only in
memory and is discarded on restart ([enrollment.md](enrollment.md) §The dev controller-CA). So a
controller restart invalidates **both** the server cert and every issued client cert — the operator
must **re-distribute the (new) CA cert** out-of-band **and re-issue enrollment tokens**, and every node
must **re-enroll**. This is the same "re-enroll on restart" model as Phase 2b; it is an honest cost of
not persisting a CA key at rest, and a persisted/HSM-backed CA is the documented Plan 5 swap that would
remove it. The agent's pinned `--controller-ca` is therefore a per-CA-epoch artifact, not a
once-forever one.

## The enroll subcommand

`agent enroll --controller <url> --controller-ca <pem> --token <plaintext> --node-id <id>` runs the
node side of the enrollment ceremony ([enrollment.md](enrollment.md) §The ceremony at a glance) once:

1. **Ensure the WireGuard key.** It reuses Phase 1b's idempotent `EnsureKey(/etc/wireguard/agent.key)`
   (`agent keygen`) so the node's WG identity is the **same stable private key** it has always held; only
   the WG **public** key is surfaced into the enroll request. The WG private key never leaves the host
   and is never sent to the controller.
2. **Generate the mTLS keypair + CSR.** It generates a fresh **Ed25519** keypair (`crypto/ed25519`) and
   a self-signed **CSR** (`crypto/x509`) whose Common Name is exactly `"<tenant>:<node-id>"` — the CN the
   controller's `IssueClientCert` requires ([enrollment.md](enrollment.md) §Proof-of-possession). The
   CSR's self-signature **is** the proof-of-possession of the mTLS private key. (PoP is on the mTLS key,
   not the WG key, because WireGuard's Curve25519 is DH-only and cannot sign — see
   [enrollment.md](enrollment.md).)
3. **Call `/enroll`** via `ControllerClient.Enroll(token, nodeID, csrDER, wgPub)`: a `POST` to
   `/api/v1/controller/enroll` over the CA-pinned TLS, body
   `{token, node_id, csr_der (base64), wg_public_key}` (`enrollRequestJSON`,
   [controller-api.md](controller-api.md) §`POST /enroll`).
4. **Verify the response CA pin.** The response is `{client_cert_pem, ca_cert_pem, fingerprint}`
   (`enrollResponseJSON`). The agent verifies `ca_cert_pem == --controller-ca` (the hard refusal above)
   **before** persisting anything.
5. **Persist the mTLS identity, 0600.** It writes the issued **client cert** and the locally-generated
   **mTLS private key** to `/etc/wireguard/agent-mtls.crt` and `/etc/wireguard/agent-mtls.key`,
   **mode 0600** (the mTLS key is a control-plane secret, held to the same custody standard as the WG
   key; it is written exactly once at enroll and never re-emitted). The pinned CA is what the subsequent
   `run` loop trusts as both `RootCAs` (to validate the controller's server cert) and the anchor the
   issued client cert chains to.

Enrollment is the **only** step that uses the token, and the token is **single-use** — burned by the
controller on the first successful `/enroll` ([enrollment.md](enrollment.md) §burn before issue). A
re-enroll (e.g. after a controller restart) requires a **fresh** token; the agent's WG key is reused
(idempotent keygen) but a new mTLS keypair + cert is issued.

## The controller run loop

`agent run --controller <url> --controller-ca <pem> [--pubkey <pinned-signing-pem>]` loads the stored
mTLS client cert + key (and the pinned CA), constructs a `ControllerClient`, and drives the **same**
`agent.Run` pull→verify→anti-rollback→apply→report core as static-source mode — only the `Source` is
the `ControllerClient`. The loop:

1. **Poll (long-poll, mTLS).** `ControllerClient.Poll(after)` issues `GET /poll?after=<last-applied
   generation>` over mTLS ([controller-api.md](controller-api.md) §`GET /poll`). The call **blocks**
   until the controller promotes a generation strictly greater than `after`, returning `(gen, changed)`;
   a **204** (server-side ~55s deadline with no advance) returns `changed = false` and the agent simply
   re-polls with the same watermark. `after` is the agent's applied-generation high-water mark (`0` on
   first boot), so a promote that happened between polls is caught immediately.
2. **Fetch on change (mTLS).** On a new generation, `ControllerClient.Config()` issues `GET /config`
   over mTLS and base64-decodes the `{generation, files}` body (`configResponseJSON`) into the
   `map[string][]byte` the verify+stage core expects — exactly the shape `DirSource`/`HTTPSource`
   return. A **404** ([controller-api.md](controller-api.md) §`GET /config`) means "nothing promoted
   for this node yet" and is treated as a benign no-op (keep polling), not a failure.
3. **Verify (fail-closed) — reused verbatim.** The fetched files go through the **same**
   `VerifyBundle(files, pinnedPubPEM)` ([signing.md](signing.md)): the `--pubkey` operator-pinned
   signing key (strongly recommended for any custody fleet) verifies `bundle.sig` over the canonical
   `checksums.sha256`, then every listed file's SHA-256 is re-checked and `install.sh` must be present
   and covered. Any mismatch is a **hard refusal** before anything root-side runs. This is **not** a new
   code path — it is the identical Go-side gate Phase 1b uses; the controller channel does not relax it.
4. **Apply — reused verbatim.** On a verified bundle the agent stages it to disk and runs the bundle's
   own **`install.sh`** as root, which **re-verifies** the pristine bundle and performs the
   **custody-gated splice** of `/etc/wireguard/agent.key` into the copied confs
   ([../artifacts/install-script.md](../artifacts/install-script.md)). The agent never splices, never
   parses the WG private key, and never mutates the signed bundle bytes — apply is idempotent and the
   bundle stays pristine, exactly as in Phase 1b.
5. **Report (mTLS).** After a successful apply, `ControllerClient.Report(appliedGen, checksum, health)`
   issues `POST /report` over mTLS (`reportRequestJSON`,
   [controller-api.md](controller-api.md) §`POST /report`), so the controller's registry records the
   node's `AppliedGeneration` / checksum / health and the panel can diff desired-vs-applied. Reporting
   is **best-effort** (the `Reporter` contract): a failed report is logged but never fails an
   otherwise-successful apply. The agent then advances its local `after` watermark to the applied
   generation and re-polls.

### Anti-rollback on the bundle generation

In controller mode the anti-rollback high-water mark is the **bundle generation** delivered over the
authenticated mTLS channel (the `generation` in `/poll` and `/config`), **not** the unsigned-manifest
`compiled_at` stub of Phase 1b. This is the upgrade the static-source `anti-rollback` section flags as a
later item: the generation is a **monotonic** counter the controller increments only on promote
([deploy.md](deploy.md) §Generation arithmetic), and it arrives over a **channel the agent
authenticates** (the controller's server cert chains to the pinned CA, and the agent presents its own
mTLS cert). The agent applies only a generation **strictly greater** than its last-applied watermark, so
a replayed older `/config` cannot roll the node back over this channel.

**Honest scope.** The generation is carried in the mTLS **response envelope**, not bound *inside* the
signed `checksums.sha256` bytes — so the authenticity guarantee here is **channel-level** (TLS 1.3 +
mTLS to a CA-pinned controller), which is materially stronger than the Phase 1b unsigned-manifest stub
(which guarded only an honest source serving a stale file). A generation/version **bound into the signed
content itself** — so a stale-but-validly-signed bundle cannot be replayed even by an actor on the
authenticated channel — remains the strongest form and is the Plan 5 direction; controller mode reaches
the channel-authenticated rung, which is sufficient for the single-tenant v1 trust model.

## Keep-last-good in controller mode

The fail-closed / keep-last-good guarantee of the [Fail-closed and keep-last-good](#fail-closed-and-keep-last-good)
section holds **identically** in controller mode — it is the same `Run` core. A controller that is
unreachable (poll/config transport error), a bundle that fails `VerifyBundle`, a rolled-back generation,
a missing `/etc/wireguard/agent.key`, or a nonzero `install.sh` exit each **aborts the pass without
disturbing the running overlay**: the node keeps its last successfully-applied generation and re-polls.
Verification is fully completed *before* `install.sh` runs, and `install.sh` re-verifies again as root,
so a tampered bundle never reaches an applied state. *Automated rollback to a prior generation* and
*instant fleet-wide rollback* remain Plan 5 (stage→promote step-up + instant rollback).

## The agent reuses Plan 1b verify+apply verbatim

The defining property of controller mode is that it is **still a thin wrapper**. Everything new is
**network plumbing** (`ControllerClient` + the `enroll` bootstrap); the **trust-critical** steps are
untouched:

- **Verify** is the byte-identical `VerifyBundle` — same pinned-key policy, same per-file SHA-256, same
  `install.sh`-must-be-covered guard ([signing.md](signing.md)).
- **Apply** is the byte-identical `install.sh` hand-off — same custody-gated splice, same
  verify-before-Phase-2 ordering, same pristine-bundle idempotency
  ([../artifacts/install-script.md](../artifacts/install-script.md), [key-custody.md](key-custody.md)).

So controller mode inherits Phase 1b's custody and signing guarantees for free; the only divergences
from static-source mode are the **transport** (mTLS to the controller) and the **identity** (an enrolled
client cert instead of a configured `--node-id` against a static path). The agent owns no new
control-plane logic.

# Deferred

**Delivered since Phase 1b.** Plan 4 (the controller program) has since landed enrollment (single-use
tokens + CSR proof-of-possession, [enrollment.md](enrollment.md)), per-node **mTLS** + controller TLS
1.3 ([controller-api.md](controller-api.md)), durable file-backed persistence (public-keys-only
registry, [persistence.md](persistence.md)), **long-poll** change-notify, and status **reporting** — all
consumed by [Controller mode (Phase 2c-c)](#controller-mode-phase-2c-c) above. Anti-rollback has moved
off the unsigned-manifest stub onto the channel-authenticated **bundle generation**
([Anti-rollback on the bundle generation](#anti-rollback-on-the-bundle-generation)).

Still explicitly **out of scope**, by program plan:

- **Plan 4.4** — the **frontend** controller panel (the Deploy/registry UI that drives
  `/update-topology` → `/stage` → `/promote` and surfaces desired-vs-applied per node).
- **Plan 5** — a **persisted / KMS-backed CA** (sign-only key handle, removing the ephemeral-CA
  re-enroll-on-restart cost), a generation/version **bound into the signed content** (so a
  stale-but-validly-signed bundle cannot be replayed even on the authenticated channel),
  **multi-tenant** isolation, per-tenant **KMS** sign-only key handles, **stage→promote** with
  out-of-band approval + **instant rollback**, OIDC/RBAC operator login, and **hardware-signed
  membership** (WebAuthn/FIDO step-up on trust-list changes).

## Verification

**Static-source mode (Phase 1b).** The end-to-end proof is a **real-host two-node smoke test** — two
hosts each run keygen, pull their signed bundle, verify, apply via `install.sh`, and confirm the overlay
comes up (each node splices its own `/etc/wireguard/agent.key`, the placeholder bundles stay pristine,
and `ping` across the overlay succeeds).

**Controller mode (Phase 2c-c).** CI covers the full loop **in-process**
(`internal/agent/controller_client_test.go`, reusing the httptest + TLS + dev-CA harness pattern of
`internal/api/controller_http_test.go`): a real `ControllerHandler` over an httptest TLS 1.3 + mTLS
server with the ephemeral dev CA and a `MemStore`; the agent `ControllerClient` **enrolls** certless, an
operator **stages + promotes**, the agent **polls** (gets the generation), **fetches** the config,
`VerifyBundle` **passes**, the **apply step is mocked** (asserting the staged bundle is what would be
applied — root + kernel WireGuard cannot run in CI), and **`Report`** updates the registry. The negative
cases are covered too: a **CA-mismatch** enroll is refused, and a poll/config **without the mTLS cert**
fails. The actual `install.sh` apply and the live mTLS handshake against a real controller are still the
**real-host two-node smoke**, which is the **owed manual gate** — it requires two real Linux hosts with
kernel WireGuard and root, so it **cannot be run in CI** and is recorded as owed in the PR.

CI continues to enforce the custody guard and the AgentHeld↔AirGap diff
([key-custody.md](key-custody.md)) and the signing tests ([signing.md](signing.md)); the on-host smoke
is the human sign-off that the chain those gates protect actually holds on a live host — for **both**
modes.
