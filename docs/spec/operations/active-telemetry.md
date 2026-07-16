# Signed active telemetry

Active telemetry is a bounded, signed policy that asks a managed node to test selected destinations
from that node's own network position. It supports ICMP echo (the familiar `ping` behavior) and a TCP
connect check (the useful part of `tcping`) without invoking a shell or installing another utility.

This is deliberately separate from passive resource history. Resource samples describe the node
itself; active probes generate outbound traffic to an operator-chosen destination. The extra network
authority is why the policy is topology data, checksum-covered and bound into the required off-host
keystone-signed membership manifest. Optional tier-1 bundle signing adds `bundle.sig`; activation still
occurs only after off-host keystone authorization.

## Ownership and UI

Fleet owns both configuration and observation:

- The Fleet registry shows a compact health summary for each node.
- A Fleet node-detail page co-locates the hand-edited policy and the latest live results.
- Saving edits the controller's whole design draft. It does not activate the policy.
- Deploy compiles, signs, promotes, and lets the node activate the policy.

The broader page remains named **Fleet** because it also owns enrollment, deployment state, node
identity, and health. Probe results are live telemetry and are removed from the browser persistence
allowlist.

## Topology policy

`Node.telemetry_probes` is optional and limited to 16 entries. There is one destination field:
`host`. It is required and accepts either a bare IPv4/IPv6 literal or an ASCII DNS hostname. There is
no separate or mandatory DNS field. Single-label names, a trailing DNS root dot, and explicit Punycode
are accepted; URL schemes, paths, queries, embedded ports, bracketed URL-style IPv6, whitespace, and
Unicode hostnames are rejected.

```json
{
  "telemetry_probes": [
    { "id": "gateway-ping", "type": "icmp", "host": "192.0.2.1" },
    {
      "id": "dns-tcp",
      "type": "tcp",
      "host": "resolver.example.net",
      "port": 53,
      "interval_seconds": 60,
      "timeout_milliseconds": 2000
    }
  ]
}
```

| Field | Contract |
|---|---|
| `id` | Unique per node; 1–63 ASCII letters, digits, `.`, `_`, or `-`. |
| `type` | `icmp` or `tcp`. Unknown future types are rejected. |
| `host` | Required IP literal or ASCII DNS hostname, maximum 253 bytes. |
| `port` | Required for TCP, 1–65535; forbidden for ICMP. |
| `interval_seconds` | Optional; default 60, allowed 30–3600. |
| `timeout_milliseconds` | Optional; default 2000, allowed 100–5000 and shorter than the interval. |

Manual nodes have no resident agent and cannot originate active telemetry. Changing a topology node
to manual clears its probe policy at the frontend custody boundary, and backend validation rejects an
invalid manual-node policy.

The root policy format is versioned so a future URL probe can introduce a separately typed contract.
It must not reinterpret `host`, accept arbitrary URLs in a TCP probe, or grow into a remote command
surface.

## Signed bundle and activation boundary

For a non-empty policy in AgentHeld custody, the shared render/export path emits compact version-1
`telemetry.json`:

```json
{"version":1,"probes":[...]}
```

The member always participates in `checksums.sha256`. When tier-1 bundle signing is configured,
`bundle.sig` signs that canonical checksum manifest; independently, the required off-host keystone
membership signature binds the resulting bundle digest. The member is omitted entirely when no probes
exist, preserving historical bundle bytes. AirGap bundles do not receive this controller-managed
network policy.

Both sides enforce authorization:

1. Deploy preview and stage refuse a ready probe-bearing node unless the tenant has a pinned off-host
   operator keystone (`telemetry_probes_require_keystone`, HTTP 412).
2. Normal bundle checksum/signature verification proves `telemetry.json` is covered and unmodified.
3. The agent independently requires verified off-host keystone membership before treating the member
   as activatable policy.
4. A probe-bearing `install.sh` requires the version-1 telemetry-policy capability supplied by rc.9
   and later launchers before normal host mutation. A pre-rc.9 agent therefore refuses the apply
   instead of silently ignoring `telemetry.json`; uninstall remains available for recovery.
5. The policy becomes active only after the corresponding bundle has applied successfully and the
   agent commits its final state.

A failed candidate leaves the last-known-good policy active. A successfully applied signed bundle
that omits `telemetry.json`, or a successful uninstall, clears it atomically. Corrupt or manually
edited state is never an authorization source.

## Runtime scheduler

The active-probe sampler never performs network I/O in the heartbeat sampling call. Due attempts run
asynchronously with these bounds:

- At most four attempts run concurrently across the node.
- The same probe never overlaps itself.
- A newly activated probe receives stable startup jitter of at most five seconds, avoiding a
  promotion-wide synchronized burst; the seed includes node identity as well as probe identity.
- Due times and latency use Go's monotonic clock, so NTP/manual wall-clock adjustments cannot suppress
  attempts or create negative/inflated latency. UTC wall time is used only for `checked_at`.
- A DNS hostname is resolved again on every attempt; at most eight distinct answers are tried.
- TCP uses `net.Dialer.DialContext` and requires no external dependency.
- ICMP is implemented with direct IPv4/IPv6 raw sockets and no `ping` subprocess. A node without raw
  socket permission reports `permission_denied` rather than weakening the policy or installing tools.

Signing a DNS name authorizes the addresses to which it resolves on each attempt. Signing a private,
loopback, link-local, or otherwise sensitive destination intentionally authorizes outbound traffic to
that destination; the validator does not pretend those networks are universally unsafe. Operators
must treat the keystone signing ceremony as the review point for that authority.

## Result contract

The sampler reports a bounded `metrics["probe_results"]` array through the ordinary authenticated
telemetry heartbeat:

```json
[
  {
    "id": "dns-tcp",
    "type": "tcp",
    "host": "resolver.example.net",
    "port": 53,
    "status": "success",
    "latency_ms": 12.4,
    "checked_at": "2026-07-16T06:20:00Z"
  }
]
```

These outcomes are authenticated as node telemetry but are not independently signed. Their authority
comes from the enrolled node reporting execution of the signed policy; the UI must not describe an
outcome itself as a cryptographic signature.

The reported host is always the configured value; transient DNS answers are not exposed. Status is
`pending`, `success`, or `failure`. Failures use only the stable categories `dns_failed`, `timeout`,
`permission_denied`, `connection_refused`, `network_unreachable`, and `network_error`; raw platform
errors do not cross the wire.

Results are controller live state, not durable resource history. They can disappear across controller
restart and are deliberately stripped from browser persistence. A configured probe with no result is
shown as waiting, while a result from the just-previous deployed policy may remain visible until the
next heartbeat converges.

## Cross-references

- Reliable heartbeat transport and retained resource samples: [telemetry-history.md](telemetry-history.md)
- Bundle verification and last-known-good apply: [../controller/agent.md](../controller/agent.md)
- Deploy/keystone boundary: [../controller/deploy.md](../controller/deploy.md)
- Node topology model: [../data-model/node.md](../data-model/node.md)
