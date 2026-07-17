# Controller agent API

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the controller's dedicated node-facing HTTP protocol: pre-authenticated bootstrap and one-time
enrollment, then bearer-authenticated configuration, generation polling, apply reporting, telemetry,
and public-key rotation (`internal/api/routes_controller.go:244-274`).

## Files

- `internal/api/handler_agent.go:18-383` — handles enrollment and authenticated node operations.
- `internal/api/handler_bootstrap.go:29-76` — renders the generic one-shot installer.
- `internal/api/wire_controller.go:21-80` — defines node-facing JSON contracts.
- `internal/controller/enrollment.go:128-350` — owns enrollment and rekey identity mutations.

## Inputs

The agent listener supplies the configured tenant and optional agent path prefix. Enrollment accepts
a single-use token, requested node ID, and WireGuard public key; authenticated routes derive the
node exclusively from a per-node bearer rather than a body or path parameter
(`internal/api/handler_agent.go:18-83`, `internal/api/auth_controller.go:92-161`). Bootstrap reads
public rollout settings and the pinned public keystone credential, if present
(`internal/api/handler_bootstrap.go:29-76`).

## Outputs

Enrollment returns the plaintext node API token once while retaining its hash. Configuration returns
one atomic promoted bundle/trust-list snapshot, and polling returns only a generation newer than the
caller's cursor (`internal/controller/enrollment.go:218-263`,
`internal/api/handler_agent.go:85-183`). Report updates deployment/Fleet state, telemetry updates
receipt-authoritative live/history state, and rekey stores only a replacement public key
(`internal/api/handler_agent.go:240-268,336-383`,
`internal/controller/enrollment.go:297-350`).

## Decision points (if any)

- `/enroll` and `/bootstrap` are deliberately bearer-free: the former uses a bounded single-use
  enrollment credential, while the latter contains only public/default material
  (`internal/api/routes_controller.go:250-274`, `internal/api/handler_agent.go:18-83`).
- `/poll` uses the raw authenticated adapter because its timeout response must be a bodyless `204`;
  the other authenticated routes use the ordinary JSON adapter
  (`internal/api/routes_controller.go:258-271`, `internal/api/handler_agent.go:138-183`).
- Telemetry without protocol-v2 headers follows the legacy contract; valid v2 headers add reliable
  acknowledgement and negotiated capabilities without changing the JSON body. Detailed ingestion
  semantics belong to `controller-telemetry` (`internal/api/handler_agent.go:288-334,336-383`).
- Enrollment is rate-limited by trusted request source before body parsing, while authenticated node
  operations are rate-limited by the bearer-resolved node identity rather than a body, path, or
  proxy-collapsed source (`internal/api/handler_agent.go:28-47`,
  `internal/api/auth_controller.go:137-161`).

## Invariants

- A node can act only as the bearer-resolved approved identity injected by `requireNode`; protected
  handlers do not accept a target identity from the request (`internal/api/auth_controller.go:109-161`).
- WireGuard private keys never cross this API. Enrollment and rekey validate uniqueness across
  approved and manual nodes and store public keys only
  (`internal/controller/enrollment.go:157-216,266-350`).
- `GetServedConfig` returns the promoted bundle and signed trust list atomically, and a configured
  keystone without its served signed manifest fails closed
  (`internal/api/handler_agent.go:85-136`).

## Gotchas (optional)

- Enrollment validates public-key syntax before burning the token, but an authorized lifecycle or
  uniqueness conflict occurs after the atomic burn and requires a new enrollment token
  (`internal/controller/enrollment.go:128-216`).
- Routine report and telemetry calls update operational state without appending high-frequency rows
  to the durable audit chain; security-relevant enrollment and rekey mutations remain audited
  (`internal/api/handler_agent.go:240-268,336-383`,
  `internal/controller/enrollment.go:244-263,322-350`).
- Both controller listeners are plain HTTP; production must terminate TLS before these replayable
  bearer credentials traverse the network (`internal/api/server.go:61-71,209-224`).
