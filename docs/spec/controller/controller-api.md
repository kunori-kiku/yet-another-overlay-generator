# Controller HTTP API

<!-- last-verified: 2026-07-17 -->

This document describes the controller's current network boundary. `yaog-server` is a
controller-only binary with separate operator/panel and agent listeners. Both listeners speak plain
HTTP; production deployments must terminate TLS at a trusted reverse proxy.

The API is intentionally not a remote version of the offline compiler. The operator listener has
one general public route, `GET /api/health`, plus the password/passkey endpoints required to
establish a session. The retired anonymous `/api/{validate,compile,export,deploy-script}` routes are
absent. Local design uses the Go/WASM engine, while controller design uses authenticated
preview/stage/promote operations.

## Runtime and listeners

`cmd/server` requires both `YAOG_CONTROLLER_STATE_DIR` and `YAOG_TENANT_ID` and exits with an error
when either is absent. It opens the durable `FileStore`, registers both route families, and serves
them concurrently (`cmd/server/main.go:109-176,220-278`):

- The operator/panel listener uses `-addr` (default `:8080`). It serves `/api/health`, the operator
  API, and optionally the panel SPA from `YAOG_WEB_DIR`.
- The agent listener resolves `-agent-addr`, then `YAOG_CONTROLLER_AGENT_ADDR`, then `:9090`. It
  serves only the agent API.

`internal/api.Server` owns a mux and `http.Server` for each audience. Both servers have bounded
header/read/write/idle timeouts; the agent write timeout exceeds the long-poll deadline. Shutdown
cancels the shared base context before draining both listeners, so `/poll` requests unblock promptly
(`internal/api/server.go:19-79,181-269`).

The relevant environment variables are:

| Variable | Contract |
| --- | --- |
| `YAOG_CONTROLLER_STATE_DIR` | Required `FileStore` root. |
| `YAOG_TENANT_ID` | Required single tenant used to scope every store operation. |
| `YAOG_CONTROLLER_OPERATOR_TOKEN` | Optional break-glass bearer credential. Named operator sessions are the normal path. |
| `YAOG_CONTROLLER_AGENT_ADDR` | Agent listener fallback address; `-agent-addr` wins. |
| `YAOG_OPERATOR_PATH_PREFIX` | Optional prefix before `/api/v1/operator/`. |
| `YAOG_AGENT_PATH_PREFIX` | Optional prefix before `/api/v1/agent/`. |
| `YAOG_WEB_DIR` | Optional built panel directory served on the operator listener. |
| `YAOG_PANEL_ORIGIN` | Comma-separated exact origins allowed to use credentialed CORS. |
| `YAOG_SECURE_COOKIE` | Defaults true; disable only for local non-TLS development. |
| `YAOG_TRUSTED_PROXIES` | IP/CIDR allowlist whose `X-Forwarded-For` value may key rate limits. |

The removed `YAOG_CONTROLLER_PATH_PREFIX` is a startup error. The two current prefixes are
independent and are scanner-obscurity/routing aids, not authentication boundaries
(`cmd/server/main.go:149-170`; `internal/api/routes_controller.go:352-389`).

## Authentication and request identity

`internal/api/auth_controller.go` is the shared authentication chokepoint. Successful middleware
stores tenant and actor in the request context; the typed `op`/`opRaw` adapters then enforce the HTTP
method and require that identity before dispatching a handler (`internal/api/adapter.go:32-84`).

### Agent identity

Agent-authenticated routes accept only `Authorization: Bearer <node-token>`. The plaintext is hashed
with `controller.HashToken`, then `Store.LookupNodeByAPIToken` resolves an approved node. Invalid,
stale, and revoked credentials all fail as the same opaque 401. The resolved node ID, never a URL or
body field, is the actor used by `/config`, `/poll`, `/report`, `/telemetry`, and `/rekey`
(`internal/api/auth_controller.go:92-161`). A per-node fixed-window limiter is applied after
authentication.

### Operator identity

Protected operator routes accept either:

1. A valid named-operator session, supplied as a bearer token or the `yaog_session` httpOnly cookie.
2. The optional `YAOG_CONTROLLER_OPERATOR_TOKEN`, compared by hash in constant time as a break-glass
   recovery credential.

Cookie-authenticated state-changing requests must also present the double-submit CSRF value:
`X-CSRF-Token` must match the readable `yaog_csrf` cookie. Bearer-header requests do not use ambient
browser credentials and are exempt (`internal/api/auth_controller.go:163-227`;
`internal/api/cookie_session.go:27-119`).

The middleware preserves whether authentication came from a named session or break-glass. This is
load-bearing even when the configured break-glass actor name matches a real account: account-bound
TOTP and login-passkey management require a named session, while fleet recovery and keystone
operations can still use break-glass.

`POST /login` and the two passwordless passkey-login endpoints are reachable before a session.
`POST /login` verifies a named account password and its configured second factor before minting a
session. Passwordless login uses a single-use server challenge. The server rate-limits login attempts
by username and source IP.

Every successful password or passkey login response, and the authenticated `GET /session` response,
also carries the additive `controller_capabilities` list. A controller that understands the
successor telemetry topology fields advertises the exact `telemetry-policy-v2-topology` token. The
panel treats an absent token as an older controller and refuses to write a topology containing URL
probes or automatic-device policy, rather than allowing an old canonicalizer to silently discard
those fields.

### WebAuthn enrollment compatibility

New browser login-passkey and browser keystone credentials use the authenticated
`POST /webauthn/enrollment/begin` endpoint. Its challenge is bound to the current actor and a declared
purpose (`login` or `keystone`), is short-lived and single-use, and must be consumed by the matching
credential-persistence endpoint. The server verifies that the exact candidate credential signed the
challenge with user presence and user verification before accepting a new pin
(`internal/api/handler_webauthn_enrollment.go:20-124`).

That stronger check is an enrollment rule, not a retroactive login/deploy rule. Existing stored
credentials remain usable: ordinary login and trust-list signing validate a correct assertion with
user presence and do not newly require the UV bit. This avoids locking out operators whose existing
authenticator cannot satisfy a newly imposed UV policy. The panel warns that a credential without
user verification may be copyable or synchronized; WebAuthn attestation/non-exportability is not
claimed.

## Route inventory

The optional audience prefix is omitted below. Registration is authoritative in
`RegisterAgentRoutes` and `RegisterOperatorRoutes` (`internal/api/routes_controller.go:244-365`).

### Agent listener: `/api/v1/agent/`

| Method | Path | Authentication | Purpose |
| --- | --- | --- | --- |
| `POST` | `enroll` | Enrollment token in body; no bearer yet | Burn a node-scoped token, register a valid WireGuard public key, and return the node bearer once. |
| `GET` | `bootstrap` | None | Return the generic install/enroll script; the operator supplies the enrollment token as a flag. |
| `GET` | `config` | Node bearer | Return the caller's promoted bundle and rekey flag. |
| `GET` | `poll?after=N` | Node bearer | Wait for a generation greater than `N`; timeout/cancellation returns 204. |
| `POST` | `report` | Node bearer | Update the caller's applied generation/checksum/health and curated Fleet state. |
| `POST` | `telemetry` | Node bearer | Record live conditions/metrics without changing deployment state. |
| `POST` | `rekey` | Node bearer | Register the caller's replacement WireGuard public key. |

`enroll` and `bootstrap` are deliberately pre-authentication surfaces. Enrollment is protected by a
short-lived, single-use node-scoped token and a pre-body per-IP limiter; bootstrap contains public
configuration and public trust material only. See [controller-agent-api.md](../../../specs/controller-agent-api.md)
for their detailed state transitions.

`POST /report` is authenticated deployment/Fleet state, but it is not a durable security/operator
audit event. Apply failures and retries can report every few seconds, so the controller updates the
node's applied generation, checksum, health, version, conditions, and last-seen without appending a
content-free `report` row to the hash chain. `POST /telemetry` remains the higher-frequency volatile
observability path and likewise does not append audit entries. Older controllers did append
`action:"report"`; those legacy rows remain part of the raw `GET /audit` response and hash-chain
verification. The panel verifies the complete raw chain first, then hides only those legacy report
rows from the operator-facing table.

#### Reliable `POST /telemetry` extension

The request body remains the legacy `{conditions, metrics, agent_version}` JSON shape. Protocol v2
adds optional headers so a new agent remains compatible with a strict old controller and an old agent
remains compatible with a new controller:

| Header | Meaning |
|---|---|
| `X-YAOG-Telemetry-Protocol: 2` | Opt in to sequenced delivery. Absence selects legacy handling. |
| `X-YAOG-Telemetry-Boot-ID` | 16 random process bytes encoded as hex. |
| `X-YAOG-Telemetry-Sequence` | Positive, per-boot monotonically increasing integer. |
| `X-YAOG-Telemetry-Sampled-At` | Agent observation time in RFC3339Nano. |
| `X-YAOG-Telemetry-Interval-Millis` | Optional advisory sampling cadence; invalid values are ignored. |

On success the controller echoes protocol/boot ID, the **exact submitted sequence** in
`X-YAOG-Telemetry-Ack-Sequence`, and its receipt time. A duplicate may additionally carry
`X-YAOG-Telemetry-Duplicate: true`. A reliable receipt may advertise additive behavior through
`X-YAOG-Telemetry-Capabilities`; the current `probe-samples-v1` token is the sole authority for the
agent to add its bounded recent-attempt window. The JSON response remains `{ "status": "ok" }`.

Receipt time, never the node clock, controls `LastSeen` and live condition age. A bounded sample time
may place resource, probe, and device measurements in retained history without allowing an agent
clock to escape the accepted window. Sequence cursors and live telemetry are volatile so a heartbeat
never rewrites/fsyncs the node record. The extensible metrics map carries the agent's currently
authenticated capability set, latest probe results, negotiated recent probe attempts, and live
device inventory/samples; the catalog decides which values have chart history and which are
intentionally live-only. See
[../operations/telemetry-history.md](../operations/telemetry-history.md) for queue, retry, deduplication,
cadence, and intermediary-header-stripping behavior; authenticated live `probe_results` for a signed
policy are defined in
[../operations/active-telemetry.md](../operations/active-telemetry.md).

### Operator listener: `/api/v1/operator/`

The following endpoints are unauthenticated because they establish a session:

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `login` | Password/TOTP/passkey login and session mint. |
| `POST` | `login/passkey/begin` | Issue a real or decoy passwordless-login challenge. |
| `POST` | `login/passkey/finish` | Verify the assertion and mint a session. |

Every remaining operator endpoint goes through `operatorAuth`:

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `logout` | Revoke the presented session and expire cookies. |
| `GET` | `session` | Return current actor/session metadata, CSRF token, controller version, and additive controller capabilities. |
| `GET` | `totp/status` | Read the named account's TOTP state. |
| `POST` | `totp/enroll`, `totp/confirm`, `totp/disable` | Manage the named account's TOTP factor. |
| `GET` | `passkey/status` | Read the named account's login-passkey state. |
| `POST` | `webauthn/enrollment/begin` | Begin actor/purpose-bound browser credential enrollment. |
| `POST` | `passkey/register`, `passkey/disable` | Manage the named account's login passkey. |
| `POST` | `update-topology` | Canonicalize and version a private-key-free topology. |
| `POST` | `compile-preview` | Compile the current design's admitted subgraph without writes. |
| `POST` | `deploy-preview` | Report which nodes an unforced deploy would restage; the optional `telemetry_policy_mode=upgrade-agents-first` query returns any node IDs whose successor policy is deliberately omitted. |
| `POST` | `stage` | Compile/export/stage the stored topology, optionally forcing nodes or selecting `telemetry_policy_mode`; the response reports any successor-policy omissions. |
| `POST` | `promote` | Promote the valid staged generation. |
| `GET` | `nodes`, `node-history` | Read registry/live history projections. |
| `GET` | `manual-node-bundle?node=ID` | Download a promoted manual-node bundle. |
| `POST` | `revoke` | Revoke a node and invalidate its bearer. |
| `POST` | `enrollment-token` | Mint one node-scoped enrollment-token plaintext. |
| `POST` | `rekey-all`, `clear-rekey` | Manage agent WireGuard-key rotation flags. |
| `GET` | `audit` | Return the audit entries and hash-chain verification result. |
| `GET` | `topology[?version=N]` | Return current or retained topology JSON. |
| `GET` | `topology/versions` | List retained topology-version metadata. |
| `GET, POST` | `settings` | Read or replace controller/bootstrap/rollout settings. |
| `POST` | `release-pins`, `release-assets` | Assisted discovery only; never an automatic trust decision. |
| `GET, POST` | `operator-credential` | Read, pin, or explicitly rotate the keystone credential. |
| `GET` | `trustlist` | Return the exact staged manifest bytes to sign. |
| `POST` | `trustlist-signature` | Verify and install the signature for the exact staged manifest. |

`GET /api/health` sits outside this namespace on the operator listener. It is the only anonymous
route unrelated to establishing an operator session; the three login endpoints above are the other
intentional pre-auth surfaces.

## Wire and error contracts

- JSON DTOs are centralized in `internal/api/wire_controller.go`; the panel mirrors them in
  `frontend/src/types/controller.ts` and the wire-drift gate checks the hand-maintained contract.
- JSON decoders are body-size capped and reject unknown fields (`internal/api/helpers_controller.go:26-56`).
- API failures use the registered envelope `{"error":{"code":"...","message":"...","params":{...}}}`.
  `internal/apierr` owns code/status/default-message registration; `internal/api/errmap.go` owns
  context-free controller-sentinel mappings.
- Successful JSON handlers normally return 200 through `op`. `opRaw` is reserved for the bodyless
  long-poll 204, verbatim topology JSON, and ZIP download responses.
- Bundle files in `/config` are base64 values keyed by bundle-relative path. When keystone is on, the
  atomically read served snapshot also includes `trustlist.json` and `trustlist.sig`; those files are
  outside the bundle's own checksum set because the trust list binds that set's digest.
- Deploy preview/stage of any ready-node active policy—probes or automatic-device collection—requires a
  pinned keystone. Absence uses the historical registered 412
  `telemetry_probes_require_keystone` code; the controller never silently emits unsigned active
  telemetry policy.
- A normal deploy of successor URL/device policy requires the latest exact capability advertisement
  received from the node through authenticated telemetry. Missing capability returns registered 412
  `telemetry_policy_upgrade_required`, with a bounded node list and total count. The explicit
  `upgrade-agents-first` mode leaves the saved topology intact, emits legacy ICMP/TCP policy where
  applicable, omits only successor fields from that deployment copy, and returns
  `telemetry_policy_omitted_node_ids`. Covered nodes can receive the configured signed self-update;
  uncovered nodes must be upgraded out of band. In either case, the operator waits until each affected
  node has advertised the required capabilities before a normal deploy activates the retained policy.
- A stale allocation write-back loses the topology compare-and-set and returns registered 409
  `topology_changed`; it must not overwrite a newer Save. Invalid or colliding manual-node public
  identity uses registered 422 `manual_node_invalid` rather than enrollment-skipped reporting.
- Compiler schema/semantic failures from compile preview, deploy preview, and stage use the
  registered 422 `topology_validation_failed` envelope with the first stable validator finding in
  params. Validation is against the ready deployment subgraph, so an unfinished draft on an
  unenrolled managed node does not block ready nodes. Operational storage/render/export faults remain
  500. This distinction lets the panel block an incomplete ready-node telemetry draft without
  offering the old-controller preview fallback.

## Structural invariants

- `internal/api/routes_controller.go` is the route-registration source of truth; handler behavior is
  split by domain across `handler_agent.go`, `handler_login.go`, `handler_totp.go`,
  `handler_passkey.go`, `handler_topology.go`, `handler_deploy.go`, `handler_keystone.go`, and their
  siblings. Do not add a parallel monolithic route path.
- Every stateful operation is tenant-scoped through `controller.Store`. Read-dependent mutations use
  atomic store primitives or controller-level tenant serialization; handler-level read/modify/write
  is not an acceptable substitute.
- `POST /update-topology` rejects any non-empty `wireguard_private_key`, heals colliding allocation
  pins, and stores the canonical re-marshaled model rather than unchecked raw JSON
  (`internal/api/handler_topology.go:23-78`).
- Agent routes derive the node from the bearer, operator routes derive the actor from a verified
  session or break-glass credential, and typed handlers cannot run without the context identity.
- The app does not terminate TLS. Because passwords, session/node bearer tokens, and enrollment
  tokens are replayable secrets, a TLS-terminating front is mandatory in production.

Related detail: [persistence.md](persistence.md), [enrollment.md](enrollment.md),
[deploy.md](deploy.md), [bootstrap.md](bootstrap.md), and
[the controller stage/promote spec](../../../specs/controller-stage-promote.md).
