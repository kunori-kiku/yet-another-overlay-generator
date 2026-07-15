# Node Agent (Phase 1b static-source + Phase 2 controller mode)

This document defines the single-tenant node agent (`cmd/agent`). It has **two modes** that share the
same custody, verify, and apply core:

- **Static-source mode (Phase 1b — air-gap).** The agent pulls a signed bundle from a directory or a
  plain HTTP GET, verifies it, and hands off to `install.sh`. There is no controller protocol — this is
  the mode an air-gap operator uses (and it is what the rest of this document's Phase 1b sections
  describe). This content is **unchanged**; see [Static-source mode (Phase 1b)](#static-source-mode-phase-1b)
  below.
- **Controller mode (Phase 2 — plan-4.5).** The agent **enrolls** with the networked controller
  ([controller-api.md](controller-api.md), [enrollment.md](enrollment.md)) to obtain a **per-node bearer
  API token**, then runs a control cycle authenticated by that token (one poll→apply→report pass in v1;
  a daemon repeats it): long-poll for a new generation, fetch the signed bundle over `/config`, **reuse
  the same verify + apply** as Phase 1b, then report what it applied. The transport is **plain HTTP**;
  confidentiality is provided by the operator's reverse proxy (nginx/caddy), not by the agent
  ([controller-api.md](controller-api.md) §Plain HTTP + proxy TLS). This closes the single-tenant
  control loop end to end. See [Controller mode (Phase 2)](#controller-mode-phase-2) below.

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
that precedes the loop. Read this first, then [Controller mode (Phase 2)](#controller-mode-phase-2).

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
source cannot get the agent to apply another node's bundle). Every export and staging surface uses
that same `node.ID` as the canonical per-node directory key; `node.Name` is display text and is never
a bundle lookup fallback. See [../artifacts/naming.md](../artifacts/naming.md).

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

Root application is supported on Linux. Before starting the synchronous installer, the agent writes
a crash-durable `PendingApply` record containing the exact verified bundle digest, manifest checksum,
apply/uninstall action, signing and keystone anchors, `compiled_at` floor, and membership epoch while
retaining the prior last-known-good fields. An exact retry re-saves and directory-syncs that same
intent before root runs again; a strictly newer candidate may replace it only under the same action
and trust anchors. Success advances last-known-good and clears the intent in one atomic state-file
replacement. A failed or interrupted installer leaves the intent in force, so recovery cannot accept
an older epoch, substitute different same-version root bytes, or silently drop a trust anchor.

The state-directory `flock` covers rekey, apply, and self-update. The Linux apply command runs beneath
a small guardian that inherits the exact locked open-file description. If the Go parent dies while
`install.sh` is still running, a restarted daemon or manual kit remains excluded until the installer
exits. The real installer closes the inherited descriptor, preventing a service it starts from
retaining the lease indefinitely. Windows root apply is refused before mutation because its
`LockFileEx` ownership cannot be transferred with the same guarantee; portable `kit verify`, key
generation, and release-inspection commands remain available.

### 6. report

After apply the agent records the new last-applied `compiled_at` and reports outcome (success, or the
fail-closed reason) to its log / exit code. Phase 1b reporting is local only; pushing status back to
the controller (and the controller-side registry/UI) is Plan 4.

## Fail-closed and keep-last-good

Every pre-root stage is **fail-closed**: a failed verify, a rolled-back bundle, or a missing
`agent.key` aborts the pass **without** disturbing the currently-running overlay. A nonzero
`install.sh` exit also fails the pass and never advances last-known-good, but may follow partial host
mutation by the root script. The verification layer never partially applies: it completes *before* `install.sh` is
invoked, and `install.sh` itself verifies the pristine bundle again before Phase 2 touches
`/etc/wireguard`. Once the synchronous root script begins, a host/process/storage failure can leave a
partial mutation; the durable `PendingApply` record deliberately treats that candidate's security
floors as effective and authorizes only a convergent exact retry or a strictly newer verified
candidate. The prior last-known-good record is not falsely advanced. Automated rollback to a prior
signed bundle and instant fleet-wide rollback remain separate operations.

## The thin-wrapper boundary

What the agent **is** (Phase 1b):

- local keygen + private-key custody at `/etc/wireguard/agent.key` (0600);
- a single keygen→pull→verify→anti-rollback→apply→report pass;
- Go-side `bundlesig.Verify` + per-file SHA-256 against a **pinned** public key;
- a last-known-good high-water mark plus a crash-durable pending root-mutation intent;
- delegation of all config application to the bundle's `install.sh`.

What the agent is **not**: it owns no rendering, no routing, no WireGuard/Babel control-plane logic,
and no continuous reconciliation. It deliberately reuses `install.sh` so the proof exercises the
**same** artifact the air-gap path ships, with custody as the only divergence.

# Controller mode (Phase 2)

Controller mode (plan-4.5) connects the agent to the **networked controller** of
[controller-api.md](controller-api.md). It does **not** fork the agent: it adds a network transport in
front of the **same** verify+apply core the static-source mode uses, plus an `enroll` bootstrap that
obtains the agent's **per-node bearer API token**. The agent still never renders, never routes, and never
splices — `install.sh` still owns the custody-gated splice ([key-custody.md](key-custody.md),
[../artifacts/install-script.md](../artifacts/install-script.md)).

> **Retraction (2026-06-08).** An earlier revision described an **mTLS** client (an out-of-band **CA
> pin**, an enroll-time **CSR** + Ed25519 mTLS keypair, persisted `agent-mtls.crt` / `agent-mtls.key`,
> and a `ca_cert_pem`-equality check on the enroll response). **All of that is withdrawn** in favour of a
> **bearer API token** over **plain HTTP** (see [controller-api.md](controller-api.md) §Retraction). The
> agent imports **no `crypto/tls`, no `crypto/x509`** for the controller channel; the only secret it
> persists from enroll is the bearer token.

There are two cooperating pieces:

- **`internal/agent/controller_client.go`** — a `ControllerClient` over **plain `net/http`** (stdlib
  only, no `crypto/tls`/`x509`/CA/cert, no new `go.mod` dependency):

  ```go
  type ControllerClient struct {
      baseURL        string
      nodeToken      string        // the per-node bearer API token ("" before enroll)
      httpClient     *http.Client  // ordinary calls
      pollClient     *http.Client  // long-poll calls (long client timeout for /poll)
      lastFetchedGen int64
      priorGen       int64
  }
  ```

  `NewControllerClient(baseURL, nodeToken string) (*ControllerClient, error)` builds it. It speaks the
  four `/api/v1/agent/` routes the agent needs: `Enroll` (no auth), `Poll`, `Fetch` (the `Source`
  side, `GET /config`), and `Report` (the `Reporter` side). It implements the existing `agent.Source`
  (and `Reporter`) so the shared `Run` loop drives it the same way it drives `DirSource`/`HTTPSource`.
- **two `cmd/agent` subcommands** — `enroll` (the one-time bootstrap) and `run --controller …` (the
  control loop). They are documented below.

## Bootstrap trust — the reverse proxy, not a CA pin

The agent's **first** call is `/enroll`, which it makes carrying only the single-use enrollment token —
it holds no controller-issued secret yet. There is **no in-app TLS and no CA to pin**: transport
confidentiality (and authentication of the controller endpoint itself) is the **reverse proxy's** job
([controller-api.md](controller-api.md) §Plain HTTP + proxy TLS). The operator points the agent at the
proxy's public URL; the proxy's standard TLS (a publicly-trusted cert, or the operator's own pinned cert
managed by the proxy) protects the enrollment token and the API token in transit.

- The operator hands the agent the **single-use enrollment token** + the **node id** + the **controller
  base URL** out-of-band — there is **no** CA cert to distribute (the old per-CA-epoch artifact is gone).
- The enroll request carries `{enrollment_token, node_id, wg_public_key}` (no CSR, no cert).
- The enroll **response** carries `{api_token, node_id}`. The agent persists the `api_token` and presents
  it as `Authorization: Bearer <token>` on every subsequent call. There is no CA-equality check to
  perform — the agent trusts the channel the proxy secured.

**Operational simplification vs the withdrawn mTLS model.** Because there is no ephemeral CA, a
controller **restart no longer invalidates the agent's credential**: the durable `FileStore`
([persistence.md](persistence.md)) keeps the node's record and its API-token index across restarts, so
the agent's stored bearer token keeps working. A node only re-enrolls if its token is **revoked** (the
operator clears it, [enrollment.md](enrollment.md) §Revocation) — not on every controller bounce. This
removes the "re-enroll on restart + re-distribute the CA" cost the mTLS model carried.

## The enroll subcommand

`agent enroll --controller <agent-base-url> --node-id <id> --token <enrollment-token>
[--key <wg-key-path>] [--token-out <path>]` runs the node side of the enrollment ceremony
([enrollment.md](enrollment.md) §The ceremony at a glance) once. `--controller` is the **agent
control-channel** base URL (the proxy in front of the controller's agent port). `--token-out` defaults to
**`/etc/wireguard/agent-controller.token`**.

1. **Ensure the WireGuard key.** It reuses Phase 1b's idempotent `EnsureKey(/etc/wireguard/agent.key)`
   (`agent keygen`; `--key` overrides the path) so the node's WG identity is the **same stable private
   key** it has always held; only the WG **public** key is surfaced into the enroll request. The WG
   private key never leaves the host and is never sent to the controller.
2. **Call `/enroll`** via `NewControllerClient(url, "").Enroll(enrollmentToken, nodeID, wgPub)`: a `POST`
   to `/api/v1/agent/enroll` with **no auth header** (the node has no token yet), body
   `{enrollment_token, node_id, wg_public_key}` (`enrollRequestJSON`,
   [controller-api.md](controller-api.md) §`POST /enroll`). The client is constructed with an **empty**
   `nodeToken` precisely because enroll is the unauthenticated bootstrap.
3. **Persist the bearer API token, 0600.** The response is `{api_token, node_id}` (`enrollResponseJSON`
   → `EnrollResult{APIToken string}`). The agent writes the plaintext `api_token` to `--token-out`
   (default `/etc/wireguard/agent-controller.token`), **mode 0600 under a 0700 directory** — the same
   custody standard as the WG key. The token is written exactly once and **never logged** (the subcommand
   prints nothing secret). This is the only secret the agent persists from enroll; there is no cert and
   no key file beyond the WG key.

Enrollment is the **only** step that uses the enrollment token, and that token is **single-use** — burned
by the controller on the first successful `/enroll` ([enrollment.md](enrollment.md) §burn before issue). A
re-enroll (e.g. after the operator revokes the API token) requires a **fresh** enrollment token; the
agent's WG key is reused (idempotent keygen) and a new API token is issued.

## The controller run loop

`agent run --controller <agent-base-url> --token <token-path> [--pubkey <pinned-signing-pem>] [--after N]`
reads the stored bearer token from `--token` (default `/etc/wireguard/agent-controller.token`),
constructs `NewControllerClient(url, token)`, and drives the **same** `agent.Run`
pull→verify→anti-rollback→apply→report core as static-source mode — only the `Source` is the
`ControllerClient`. The v1 `run` performs **one** poll→apply→report cycle (a production daemon repeats it
— the single-shot form keeps v1 simple and trivially testable); the steps:

1. **Poll (long-poll, bearer).** `ControllerClient.Poll(after)` issues `GET /poll?after=<last-applied
   generation>` with `Authorization: Bearer <nodeToken>` ([controller-api.md](controller-api.md) §`GET
   /poll`). The call **blocks** until the controller promotes a generation strictly greater than `after`,
   returning `(gen, changed)`; a **204** (server-side ~55s deadline with no advance) returns
   `changed = false` and the v1 single-shot `run` exits 0 with nothing to do (a daemon would re-poll with
   the same watermark). `after` is the agent's applied-generation high-water mark (`0` on first boot;
   `--after` overrides for a one-shot). `SetPriorGeneration(after)` records the watermark the report falls
   back to on a failed apply.
2. **Fetch on change (bearer).** On a new generation, `ControllerClient.Fetch(nodeID)` issues `GET
   /config` with the bearer header and base64-decodes the `{generation, files}` body (`configResponseJSON`)
   into the `map[string][]byte` the verify+stage core expects — exactly the shape `DirSource`/`HTTPSource`
   return (it also records `lastFetchedGen`, the bundle's own generation, for the report). A **404**
   ([controller-api.md](controller-api.md) §`GET /config`) means "no bundle promoted for this node yet" —
   e.g. a node enrolled but not in the current deploy's enrolled subgraph (render-what's-ready). `Fetch`
   surfaces it as an error, which the cycle treats as **keep-last-good**: the running overlay is untouched
   and a daemon keeps polling; it is not a corruption or auth failure.
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
   bundle stays pristine, exactly as in Phase 1b. The same `PendingApply` write-ahead record and
   crash-surviving Linux installer lease cover controller and manual-kit application.
5. **Report (bearer).** After a successful apply, `ControllerClient.Report(nodeID, payload)` issues `POST
   /report` with the bearer header (`reportRequestJSON`, [controller-api.md](controller-api.md) §`POST
   /report`), so the controller's registry records the node's `AppliedGeneration` / checksum / health and
   the panel can diff desired-vs-applied. Reporting is **best-effort** (the `Reporter` contract): a failed
   report is logged but never fails an otherwise-successful apply. The applied generation reported is the
   **bundle's own generation on success** (`lastFetchedGen`), or the **unchanged prior watermark on a
   failed apply** (`priorGen`, set by `SetPriorGeneration`) — a failure never tells the controller the
   node advanced. A production daemon then advances its watermark to the applied generation and re-polls;
   the v1 `run` is single-shot.

**The bearer guard.** Every authenticated call (`Poll`, `Fetch`, `Report`) sets `Authorization: Bearer
<nodeToken>` only when `nodeToken != ""` — a client constructed without a token (the enroll-only client)
never sends an auth header, and the run loop fails fast if its token file is empty rather than calling the
controller anonymously.

### Single-shot vs `--daemon`

`run --controller` is **single-shot by default**: it runs exactly one poll→apply→report cycle and exits
(`0` on a successful apply or a timed-out long-poll with nothing to do, non-zero on a fatal error). That
cycle is the **deterministic unit** the daemon loops over and the unit the e2e test exercises — keeping
the default single-shot makes v1 trivially testable and lets an operator drive cadence from an external
scheduler (cron/systemd timer) if they prefer.

Passing **`--daemon`** keeps the process running and loops that same cycle continuously for
**near-real-time** updates:

1. **Long-poll → apply → report, repeated.** Each iteration long-polls `/poll?after=<watermark>`; the call
   returns within a round-trip of an operator **promote** (push-like, no new transport), so a freshly
   promoted generation is fetched, verified, applied, and reported within one round-trip of going live. A
   **204** (server-side long-poll deadline with no advance) simply re-polls with the **same** watermark —
   no busy-wait, no churn.
2. **Watermark advance — the generation actually applied.** On a successful apply the loop advances its
   resume cursor to `ControllerClient.LastFetchedGeneration()` — the generation of the bundle the cycle
   actually **fetched and applied** — **not** the generation the poll merely observed. This closes a
   **poll→fetch race**: if a promote lands between `Poll` returning gen `N` and `Fetch` returning the newer
   gen `N+1`, the bundle carried `N+1`, so the watermark advances to `N+1` and the next poll asks for
   anything strictly newer than `N+1`. Advancing only to the *polled* `N` would leave the loop re-fetching
   and re-applying the same generation next cycle; keying the cursor on the fetched generation keeps the
   watermark from lagging the fleet under that race.
   The cycle is the exported, unit-tested `agent.RunControllerCycle(client, CycleConfig) (resumeGen,
   applied, err)`; the daemon and single-shot loops both call it, so the watermark/skip/keep-last-good
   semantics below are covered once.
3. **Rekey wake — rotate, re-register, SKIP apply, advance PAST the wake.** When the `Fetch` envelope
   carries `rekey_requested=true` (the operator's `POST /rekey-all` flagged this node **and**
   `BumpGeneration`-woke the fleet — see [deploy.md](deploy.md) §Fleet-wide key rotation), the cycle does
   **not** apply the woken bundle (it was compiled with peers' OLD public keys). It `RegenerateKey`s the
   local private key, `POST /rekey`s the new **public** key (clearing the flag), and resumes from the
   **wake generation** — `max(polled, LastFetchedGeneration())`, `applied=false`. The bump advanced the
   tenant generation without re-compiling the bundle, so `/config` still reports the OLD bundle's smaller
   generation; resuming from that smaller value (or the unchanged watermark) would leave the bumped
   generation strictly greater than the cursor and the next poll would re-fire this branch in a tight loop
   — or re-apply the stale bundle. Resuming from the polled wake guarantees the next generation the agent
   applies is **strictly greater**: the operator's post-rekey Deploy carrying everyone's new public keys.
4. **Keep-last-good with backoff.** A transport error (poll/config), a `VerifyBundle` refusal, a
   rolled-back bundle, a missing `/etc/wireguard/agent.key`, or a non-zero `install.sh` exit **does not
   advance the watermark and does not tear down the running overlay** — the loop logs the failure, keeps
   the last-good configuration, sleeps a short fixed backoff, and retries from the unchanged watermark. The
   daemon therefore never crashes a node off the overlay on a single bad cycle; it converges once the
   controller (or the bundle) is healthy again. On a **failed apply** the auto-`Report` still fires,
   reporting the **unchanged prior watermark** (`SetPriorGeneration`), so the registry never shows a
   generation the node did not actually apply.

The daemon owns **no** new trust-critical logic: it is the single-shot cycle in a loop with a watermark and
a backoff. Verify and apply are byte-identical to single-shot (and to static-source mode); the only
addition is the loop and the fetched-generation cursor. A production deployment runs the daemon under a
process supervisor (systemd) so a hard crash still restarts; `--daemon` is the in-process near-real-time
path, the supervisor is the crash-recovery backstop.

### Anti-rollback in controller mode (honest)

Controller mode **reuses Phase 1b's anti-rollback decision unchanged**: the `agent.Run` core still
refuses a bundle whose `manifest.json` `compiled_at` is older than the last applied (the Phase 1b stub).
The bundle **generation** is NOT (yet) the rollback gate — it operates at two *other* layers:

- **Poll watermark.** `Poll(after)` uses the generation as a cursor for *which* generation to fetch — a
  monotonic counter the controller increments only on promote ([deploy.md](deploy.md) §Generation
  arithmetic). That is which-bundle-to-pull, not the rollback decision.
- **Channel confidentiality.** The generation and the bundle arrive over a channel whose confidentiality
  is provided by the **reverse proxy's TLS**, and the agent authenticates *itself* to the controller with
  its bearer token. An off-path attacker who cannot break the proxy TLS cannot read or inject a stale
  `/config`. (Note the honest weakening vs the withdrawn mTLS model: the agent no longer validates a
  pinned server cert in-app — it relies on the proxy's TLS instead. See
  [controller-api.md](controller-api.md) §the honest trade-off.)

**Honest scope.** Controller mode's protection against a stale bundle therefore rests on (a) the Phase
1b `compiled_at` decision plus (b) the proxy-secured channel — NOT on a generation/version bound *inside*
the signed `checksums.sha256` bytes. That signed-content binding (so a stale-but-validly-signed bundle
cannot be replayed even by an actor on the channel) is the strongest form and is the **Plan 5**
direction. For the single-tenant v1 trust model — one operator, a TLS-terminating proxy — the
compiled_at-plus-proxy-channel rung is sufficient; this section claims no more.

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

So controller mode inherits Phase 1b's custody and signing guarantees for free; the only divergences from
static-source mode are the **transport** (plain HTTP to the controller, TLS handled by the proxy) and the
**identity** (an enrolled **bearer API token** instead of a configured `--node-id` against a static
path). The agent owns no new control-plane logic.

# Deferred

**Delivered since Phase 1b.** Plan 4 (the controller program) has landed enrollment (single-use tokens →
per-node **bearer API tokens**, [enrollment.md](enrollment.md)), the networked controller over **plain
HTTP with two ports** and TLS delegated to a reverse proxy ([controller-api.md](controller-api.md)),
durable file-backed persistence (public-keys-only registry + the API-token index,
[persistence.md](persistence.md)), **long-poll** change-notify, and status **reporting** — all consumed
by [Controller mode (Phase 2)](#controller-mode-phase-2) above. (The withdrawn mTLS/CSR/DevCA model was
replaced by token auth in plan-4.5; see the retraction notes.)

Still explicitly **out of scope**, by program plan:

- **Plan 4.4** — the **frontend** controller panel (the Deploy/registry UI that drives
  `/update-topology` → `/stage` → `/promote`, reads `/nodes` / `/audit` / `/topology`, mints
  `/enrollment-token`, and surfaces desired-vs-applied per node).
- **Plan 5** — an **optional in-app TLS toggle** (so the app can terminate TLS itself instead of always
  delegating to a proxy), a **stronger per-request proof-of-possession** than a replayable bearer token,
  a generation/version **bound into the signed content** (so a stale-but-validly-signed bundle cannot be
  replayed even on the channel), **multi-tenant** isolation, per-tenant **KMS** sign-only key handles,
  **stage→promote** with out-of-band approval + **instant rollback**, **OIDC/RBAC** operator login with
  per-operator audit identity (replacing the env operator token), and **hardware-signed membership**
  (WebAuthn/FIDO step-up on trust-list changes).

## Verification

**Static-source mode (Phase 1b).** The end-to-end proof is a **real-host two-node smoke test** — two
hosts each run keygen, pull their signed bundle, verify, apply via `install.sh`, and confirm the overlay
comes up (each node splices its own `/etc/wireguard/agent.key`, the placeholder bundles stay pristine,
and `ping` across the overlay succeeds).

**Controller mode (Phase 2).** CI covers the full loop **in-process**
(`internal/agent/controller_client_test.go`, reusing the **plain** `httptest.NewServer` harness pattern
of `internal/api/controller_http_test.go`): a real `ControllerHandler` over a plain-HTTP httptest server
with a `MemStore`; the agent `ControllerClient` **enrolls** (no auth) and receives its bearer token, an
operator (operator-token) **stages + promotes**, the agent **polls** with its bearer token (gets the
generation), **fetches** the config, `VerifyBundle` **passes**, the **apply step is mocked** (asserting
the staged bundle is what would be applied — root + kernel WireGuard cannot run in CI), and **`Report`**
updates the registry. The negative cases are covered too: a `poll`/`config` call **without the bearer
token** is **401**, a call with a **revoked** node's token is **403**, and a **node token on an operator
route** is **403**. The actual `install.sh` apply against a real controller (behind a real TLS proxy) is
still the **real-host two-node smoke**, the **owed manual gate** — it requires two real Linux hosts with
kernel WireGuard and root, so it **cannot be run in CI** and is recorded as owed in the PR.

CI continues to enforce the custody guard and the AgentHeld↔AirGap diff
([key-custody.md](key-custody.md)) and the signing tests ([signing.md](signing.md)); the on-host smoke
is the human sign-off that the chain those gates protect actually holds on a live host — for **both**
modes.
