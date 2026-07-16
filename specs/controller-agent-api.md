# Controller agent API

<!-- last-verified: 2026-07-15 -->

## Responsibility

Expose the controller's node-facing protocol on the dedicated agent listener: bootstrap and
single-use enrollment before a node has credentials, then authenticated config retrieval,
generation polling, apply reports, live telemetry, and WireGuard public-key rotation.

## Files

- `internal/api/routes_controller.go:231-260` registers the agent namespace under
  `[YAOG_AGENT_PATH_PREFIX]/api/v1/agent/` and makes the authentication boundary explicit.
- `internal/api/handler_agent.go:17-336` implements enroll, config, poll, report, telemetry, and
  rekey.
- `internal/api/handler_bootstrap.go:3-76` renders the unauthenticated one-shot installer.
- `internal/api/wire_controller.go:21-77` defines the enrollment, config, poll, report, and
  telemetry JSON contracts.
- `internal/api/auth_controller.go:92-161` extracts per-node bearer tokens, resolves only approved
  nodes, applies the per-node request limiter, and pins tenant plus node identity into context.
- `internal/controller/enrollment.go:128-350` owns the enrollment, public-key uniqueness, and
  rekey ceremonies.

## Route contract

All paths below are relative to the agent base path.

| Method and path | Authentication | Result |
|---|---|---|
| `POST /enroll` | Single-use enrollment token in the JSON body | Returns `{api_token,node_id}` once |
| `GET /config` | Per-node bearer | Returns the caller's promoted generation and base64 file map |
| `GET /poll?after=N` | Per-node bearer | Returns a newer generation or bodyless `204` on timeout |
| `POST /report` | Per-node bearer | Records an apply result and structured conditions |
| `POST /telemetry` | Per-node bearer | Records live conditions, metrics, version, and last-seen only |
| `POST /rekey` | Per-node bearer | Rebinds the caller to a new WireGuard public key |
| `GET /bootstrap` | None | Returns the generic install/enroll/apply shell script |

`/enroll` and `/bootstrap` are intentionally public on the agent listener. The bootstrap script
contains only public/default configuration; the operator supplies the secret enrollment token at
execution time. The optional path prefix is routing obscurity, not an authentication control.

## Enrollment and identity

The request is `{enrollment_token,node_id,wg_public_key}`. Before consuming the token, the
controller validates that `wg_public_key` is a standard base64 encoding of a 32-byte WireGuard
public key (`internal/controller/enrollment.go:157-163`). It then atomically consumes the token,
which checks its hash, expiry, node scope, and single-use state. A later failure does not restore
the token; the operator must issue a fresh one.

Under the tenant operation lock, enrollment refuses a revoked node ID and enforces one WireGuard
public key per node across both approved registry nodes and manual nodes in the stored topology
(`internal/controller/enrollment.go:172-216,266-295`). Same-ID re-enrollment remains supported for
a reinstall, but is recorded with a distinct audit action. On success a 256-bit per-node bearer is
returned once and only its SHA-256 is retained (`internal/controller/enrollment.go:218-263`).

Every authenticated agent route derives the node from that bearer; no route accepts a target node
in its URL or body. Invalid, stale, and revoked credentials all collapse to the same opaque
unauthorized response (`internal/api/auth_controller.go:109-161`). This preserves the invariant
that a node can act only as itself.

## Served configuration and polling

`GET /config` uses `Store.GetServedConfig` for one atomic snapshot of the promoted bundle and, when
keystone is enabled, the promoted signed trust list. This prevents a concurrent promote from
pairing an old bundle with a new manifest (`internal/api/handler_agent.go:87-136`). Bundle values
are base64-encoded by relative path and `rekey_requested` is returned from the registry record.
`trustlist.json` and `trustlist.sig` are appended to the served map, outside the bundle checksum
set. A configured keystone without a signed served manifest fails closed.

`GET /poll` waits for a generation strictly greater than `after`. Invalid or negative values are
rejected. A generation advance returns JSON; cancellation or the roughly 55-second server deadline
returns `204`, allowing the agent to open a fresh poll. Last-seen updates on config and poll are
best effort and never deny a node its bundle or polling response.

## Report, telemetry, and rekey

`POST /report` updates the caller's applied generation, checksum, health, agent version,
server-stamped conditions, and last-seen (`internal/api/handler_agent.go`). Condition count and field
sizes are bounded at the HTTP boundary. This is operational Fleet state rather than a durable
security/operator audit event: failed applies can retry every few seconds, and a bare `report` action
does not capture the useful report fields. Current controllers therefore do not append it to the
hash-chained audit log.

`POST /telemetry` is deliberately separate from deployment custody. It updates only live
conditions, bounded metrics, agent version, and last-seen; it cannot advance or regress an applied
generation and is not audited because it is a high-frequency heartbeat
(`internal/api/handler_agent.go:273-301`).

`POST /rekey` always targets the bearer-authenticated node. `controller.Rekey` validates the new
public key, enforces the same registry-plus-manual-node uniqueness invariant as enrollment, swaps
the durable public key, clears `RekeyRequested`, and audits the result under the tenant lock
(`internal/controller/enrollment.go:297-350`). Private WireGuard keys never cross this API.

## Bootstrap and errors

`GET /bootstrap` reads controller settings, composes the public agent URL with the agent path
prefix, and bakes only public defaults into the shell script. If keystone is configured it embeds
the pinned public credential; a store failure other than genuine absence is fatal rather than
silently emitting a keystone-off script (`internal/api/handler_bootstrap.go:39-76`).

JSON failures use the common `{ "error": { "code", "message", "params" } }` envelope. Important
boundary mappings include invalid/consumed enrollment tokens to `401`, malformed public keys to
`400`, duplicate keys or revoked-ID re-enrollment to `409`, missing promoted config to `404`, and
per-node or enrollment rate limits to `429`.

## Invariants and gotchas

- Enrollment and API tokens are stored only as hashes; plaintext is delivered exactly once.
- The controller stores and transports WireGuard public keys only. Private keys remain on nodes.
- Enrollment validates a public key before burning a legitimate token; uniqueness/lifecycle
  conflicts occur after the burn so only an authorized caller reaches those checks.
- `/report` may update durable deployment/Fleet state and `/telemetry` is volatile observability, but
  neither routine path appends to the durable audit chain. Legacy `report` rows written by older
  controllers remain in the raw chain for compatibility and verification.
- The listeners are plain HTTP. Production must provide TLS at a reverse proxy because bearer
  credentials are replayable if exposed on the wire.

Deep documentation: [controller API](../docs/spec/controller/controller-api.md),
[enrollment](../docs/spec/controller/enrollment.md), and
[bootstrap](../docs/spec/controller/bootstrap.md).
