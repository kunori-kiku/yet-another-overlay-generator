# Signed active telemetry

Signed telemetry policy gives a managed node two bounded kinds of observability authority:

- **Active probes** test an operator-selected destination from that node's own network position.
- **Automatic device telemetry** observes eligible disks, mounted filesystems, and GPUs on the node
  itself when the operator explicitly opts that node in.

Neither path is an arbitrary command surface. Probe types, destinations, schedules, device discovery,
collectors, result shapes, and resource bounds are closed contracts implemented by the agent. The
policy is topology data, checksum-covered and bound into the required off-host keystone-signed
membership manifest. Optional tier-1 bundle signing adds `bundle.sig`; activation still occurs only
after off-host keystone authorization.

This policy is separate from the always-on passive resource sample. CPU, memory, and load describe the
node without extra topology authority. Active probes generate outbound traffic, while automatic
device telemetry expands local collection to hardware and filesystems; both additions must therefore
cross the signed deployment boundary.

## Ownership and UI

Fleet owns both configuration and observation:

- The Fleet registry shows a compact health summary for each node.
- A Fleet node-detail page co-locates the hand-edited signed policy, latest live results and
  inventory, and component-local charts.
- Saving edits the controller's whole design draft. It does not activate the policy. A newly added
  row may be saved before its destination is filled in, so an operator can preserve work in
  progress; the editor marks that row incomplete and Deploy remains blocked for a ready node until
  the destination is supplied or the row is removed.
- Deploy compiles, signs, promotes, and lets the node activate the policy.

The broader page remains named **Fleet** because it also owns enrollment, deployment state, node
identity, and health. Latest values, discovered inventory, and fetched history are excluded from the
browser persistence allowlist. Retained numeric history lives only behind the authenticated
controller API.

## Topology policy

`Node.telemetry_probes` is optional and limited to 16 entries. `Node.telemetry_devices` is also
optional; its only accepted mode is `all-eligible-v1`. Both fields are valid only for managed nodes
that have a resident agent.

```json
{
  "telemetry_probes": [
    {
      "id": "gateway-ping",
      "name": "Office gateway",
      "type": "icmp",
      "host": "192.0.2.1"
    },
    {
      "id": "dns-tcp",
      "name": "Primary resolver",
      "type": "tcp",
      "host": "resolver.example.net",
      "port": 53,
      "interval_seconds": 60,
      "timeout_milliseconds": 2000
    },
    {
      "id": "service-health",
      "name": "Public API",
      "type": "url",
      "url": "https://api.example.net/health?full=0",
      "expected_status": 204,
      "interval_seconds": 30
    }
  ],
  "telemetry_devices": {
    "mode": "all-eligible-v1"
  }
}
```

### Probe fields

| Field | Contract |
|---|---|
| `id` | Unique per node; 1–63 ASCII letters, digits, `.`, `_`, or `-`. |
| `name` | Optional Fleet display label; valid UTF-8, printable and single-line, no surrounding whitespace, at most 128 characters. It need not be unique. |
| `type` | Exactly `icmp`, `tcp`, or `url`. Unknown types are rejected. |
| `host` | Required for ICMP/TCP and forbidden for URL; one IP literal or ASCII DNS hostname, maximum 253 bytes. |
| `port` | Required for TCP, 1–65535; forbidden for ICMP and URL. A URL port, when wanted, is part of `url`. |
| `url` | Required only for URL probes; the exact absolute HTTP(S) URL, at most 2048 bytes. |
| `expected_status` | URL only; exact successful HTTP status, 100–599. Omission means 200. |
| `interval_seconds` | Optional; default 60, allowed 30–3600. |
| `timeout_milliseconds` | Optional; default 2000, allowed 100–5000 and shorter than the interval. |

ICMP/TCP use one destination field, `host`. It accepts either a bare IPv4/IPv6 literal or an ASCII
DNS hostname. There is no separate or mandatory DNS field. Single-label names, a trailing DNS root
dot, and explicit Punycode are accepted; URL schemes, paths, queries, embedded ports, bracketed
URL-style IPv6, whitespace, and Unicode hostnames are rejected.

A URL probe is a separate type rather than an overloaded TCP host. Its URL must:

- be an absolute `http` or `https` URL with a valid IP literal or ASCII DNS hostname;
- contain no user information, fragment, control characters, literal spaces, or authority escapes;
- fit the 2048-byte bound; and
- be retained and executed as the exact unnormalized string. The controller does not silently rewrite
  a path, hostname, port, query, or status contract before signing it.

Across the at-most-16 probes, JSON-encoded URL destinations are capped at 32 KiB so mandatory latest
results still fit the authenticated 64 KiB metrics envelope.

Private, loopback, link-local, or otherwise sensitive probe destinations are intentionally allowed.
Signing such a destination authorizes the resulting outbound traffic. The keystone signing ceremony,
not a generic public-address filter, is the authority review point.

`name` is presentation metadata stored in controller topology. Stable `id` plus the exact executable
type and destination form history identity; `name` is never executable and does not split a series.
An empty required destination is tolerated only as unfinished saved draft work. Once that managed node
is in the ready deployment subgraph, compile preview, deploy preview, and stage return structured
topology validation (HTTP 422) until the operator completes or removes the row.

Manual nodes have no resident agent and cannot originate probes or automatic device telemetry.
Changing a topology node to manual clears its policy at the frontend custody boundary, and backend
deployment validation rejects policy attached to a manual node.

## Versioned signed bundle

The render/export path emits at most one active policy member for AgentHeld custody:

| Policy needed by the node | Emitted member | Schema |
|---|---|---|
| ICMP/TCP probes only | `telemetry.json` | Strict version 1: `{"version":1,"probes":[...]}` |
| Any URL probe or automatic device telemetry | `telemetry-policy.json` | Strict version 2 containing all probes plus optional `devices` |
| No executable policy | No member | Applying the omission clears an older active policy. |

Version 2 is a successor, not a sidecar. A bundle containing both `telemetry.json` and
`telemetry-policy.json` is invalid and the agent must fail closed. The compiler chooses version 2 for
the whole node as soon as any successor feature is present, so mixed ICMP/TCP/URL policies still have
one authoritative document. Unknown versions, fields, modes, and invalid values are rejected; an
accepted document is canonicalized before it becomes durable last-known-good state.
Version 2 writes the effective URL success default explicitly as `expected_status: 200`.

Executable probe objects deliberately omit topology-only `name`. A name-only Save changes the
controller design and Fleet presentation but not either policy member, bundle checksums, served
bundle digest, or history selector. Deploy preview therefore reports executable bundles unchanged and
stage does not create a new promotable generation solely for a rename.

The selected member always participates in `checksums.sha256`. When tier-1 bundle signing is
configured, `bundle.sig` signs that canonical checksum manifest; independently, the required off-host
keystone membership signature binds the resulting bundle digest. AirGap bundles receive neither
controller-managed policy member.

### Activation gates

Both controller and agent enforce authorization and compatibility:

1. Deploy preview and stage refuse a ready policy-bearing node unless the tenant has a pinned
   off-host operator keystone (`telemetry_probes_require_keystone`, HTTP 412).
2. Normal checksum/signature verification proves the one policy member is covered and unmodified.
3. The agent parses and canonicalizes the policy and verifies off-host keystone membership before
   normal root mutation.
4. Version-1 policy requires the launcher marker for exact capability `telemetry-policy-v1`,
   introduced with rc.9. A pre-rc.9 agent refuses apply instead of silently ignoring the policy;
   uninstall remains available for recovery.
5. Version-2 policy requires the exact `telemetry-policy-v2` capability and, according to its
   contents, `url-probes-v1` and/or `device-telemetry-v1`. Normal deployment readiness uses the latest
   authenticated agent heartbeat, not a claimed semantic version or route inference.
6. The policy becomes active only after the corresponding bundle applies successfully and the agent
   commits final state.

A failed candidate leaves the durable last-known-good policy active. A successfully applied signed
bundle that omits both policy members, or a successful uninstall, clears the durable policy. The
runtime transition then cancels device collection, clears volatile snapshots, and resets rate
baselines. Corrupt or manually edited agent state is never an authorization source.

### Successor rollout is deliberately two deployments

The controller advertises `telemetry-policy-v2-topology` in the authenticated operator session. The
panel refreshes that session before every successor topology write; an older controller therefore
cannot accidentally persist fields it does not understand.

When ready nodes have not yet reported the exact successor capabilities, **Upgrade first** provides
an explicit compatibility projection. With signed agent self-update configured it carries that
upgrade; otherwise the preview warns that the operator must update uncovered agents out of band:

1. The saved design remains unchanged. For the first deployment, the controller compiles a temporary
   projection that removes URL probes and automatic device telemetry from affected ready nodes while
   preserving any ICMP/TCP probes. The preview names every affected ready node.
2. Covered agents self-update from that signed deployment; agents outside the configured rollout must
   be updated out of band. The warning does not claim that omission alone upgrades them.
3. The operator waits until every affected ready node's latest capability advertisement received
   through authenticated telemetry carries the required exact capabilities.
4. A second normal deployment compiles and activates the saved URL/device policy.

An unfinished successor draft on a node outside the ready deployment subgraph may remain saved and
does not block the first deployment. A normal deployment never silently strips successor fields, and
the upgrade-first projection never mutates the controller draft.

## Active-probe runtime

Probe sampling never performs network I/O inside heartbeat collection. Due attempts run
asynchronously with these bounds:

- At most four attempts run concurrently across the node.
- The same probe never overlaps itself.
- A newly activated probe receives stable startup jitter of at most five seconds, avoiding a
  promotion-wide synchronized burst; the seed includes node and probe identity.
- Due times and latency use Go's monotonic clock, so wall-clock corrections cannot suppress attempts
  or create negative/inflated latency. UTC wall time is used only for `checked_at`.
- An ICMP/TCP DNS hostname is resolved again on every attempt; at most eight distinct answers are
  tried.
- TCP uses `net.Dialer.DialContext` and requires no external dependency.
- ICMP uses direct IPv4/IPv6 raw sockets and no `ping` subprocess. Missing raw-socket permission is
  reported as `permission_denied`; the agent does not install a tool or weaken the policy.

URL probes use a deliberately fixed HTTP transaction:

- exactly one GET with no operator-supplied body, headers, credentials, or redirect policy;
- no redirect following, environment proxy, response decompression, keepalive reuse, or opportunistic
  HTTP/2 transport;
- ordinary system TLS trust and hostname verification, with no signed option to disable either;
- bounded DNS/dial/TLS/header/total time and at most 32 KiB of response headers; and
- the response body is closed without being consumed.

The actual response code is compared for exact equality with the effective `expected_status`. A
completed response with a different code is an `unexpected_status` failure, but still has meaningful
request latency. A DNS, connection, TLS, or timeout failure has neither an HTTP code nor a fabricated
latency.

## Automatic device runtime

`telemetry_devices.mode = "all-eligible-v1"` opts the node into deterministic local discovery. The
operator does not enumerate device IDs by hand. Collection runs every 30 seconds, independently of
heartbeat upload, under a three-second whole-collection timeout. At most one provider worker is
active; a slow provider cannot build an unbounded queue.

On Linux the collector discovers, subject to deterministic caps:

- eligible sysfs block devices, excluding loop, RAM, and zram pseudo-devices;
- mounted filesystems for which usage can be measured; and
- PCI/DRM display-class GPUs from every vendor.

At most 64 disk/filesystem inventory rows and 16 GPU rows are reported, and the complete inventory
plus numeric sample pair is capped at 24 KiB. External command execution is capped at two seconds and
64 KiB of output. Non-Linux builds support the signed policy contract but return an empty device
inventory and sample set.

NVIDIA metrics use `nvidia-smi` only from `/usr/bin/nvidia-smi`,
`/usr/local/bin/nvidia-smi`, or `/usr/local/nvidia/bin/nvidia-smi`, with fixed arguments and no
shell. AMD `amdgpu` metrics use bounded sysfs files. Other GPU vendors, an absent NVIDIA tool, or an
unavailable driver still produce bounded inventory with an explicit status such as `unsupported`,
`tool_missing`, `driver_unavailable`, or `metrics_unavailable`; they do not cause the agent to install
a dependency or run a discovered command.

The live device payload separates:

- **inventory**, including kind, opaque device identity, display metadata, and categorical status;
  from
- **numeric samples**, whose allowed definitions are filesystem used %, disk read/write bytes per
  second, disk I/O busy %, GPU utilization %, and GPU VRAM used %.

Every numeric sample must match an inventory entry. Percentages are finite and bounded to 0–100;
rates are finite and non-negative. A measured zero is valid data. Missing counters, unsupported
metrics, the first disk counter baseline, or a timed-out collection emit no numeric point and
therefore remain chart gaps. A timed-out/in-flight collection may retain the previous bounded
inventory marked `collection_error`, but must not replay old numeric values as new measurements.
Removing the opt-in policy cancels collection, clears snapshots, and resets disk-rate baselines.

## Live result and history contracts

### Probe results

The agent keeps the backward-compatible bounded `metrics["probe_results"]` latest-value array for
Fleet status and older controllers. A URL row additionally carries the exact `url`, effective
`expected_status`, and—only after an HTTP response completes—`actual_status`:

```json
[
  {
    "id": "service-health",
    "type": "url",
    "url": "https://api.example.net/health?full=0",
    "expected_status": 204,
    "actual_status": 503,
    "status": "failure",
    "failure_reason": "unexpected_status",
    "latency_ms": 18.7,
    "checked_at": "2026-07-17T06:20:00Z"
  }
]
```

Outcomes are authenticated node telemetry but are not independently signed. The configured
destination is reported, while transient DNS answers are not. Status is `pending`, `success`, or
`failure`. Stable failure categories include `dns_failed`, `timeout`, `permission_denied`,
`connection_refused`, `network_unreachable`, `network_error`, and URL
`unexpected_status`; raw platform errors do not cross the wire.

After a protocol-v2 controller advertises `probe-samples-v1` on a successful receipt, an updated agent
also emits `metrics["probe_samples"]`, a rolling window of at most 64 **completed** attempts. Each row
uses the same result shape plus effective `interval_ms`. It never includes initial `pending`.
Multiple attempts that complete between ordinary heartbeats therefore remain available to the
reliable replay queue. The agent keeps the rc.9 shape until negotiation succeeds, disables the
extension after a successful no-capability receipt, and sends one coalesced clean heartbeat after
rollback. An updated controller can still derive progressive, lower-fidelity history from repeated
rc.9 `probe_results` snapshots.

Completing 32 attempts since the previous snapshot schedules at most one early collection, leaving
half the rolling window as headroom. This does not create a second transport or one POST per attempt:
collection remains single-goroutine and upload uses the ordinary bounded replay queue.

The controller strictly parses both keys, bounds `checked_at` against the normalized outer sample
clock, and retains only completed attempts. Repeated latest snapshots, overlapping windows, and exact
transport retries are deduplicated. Series identity is:

- `id + type + host + port` for ICMP/TCP; or
- `id + type + exact URL + effective expected status` for URL.

`name` is not reported and cannot split a chart. Reusing an ID for a different destination or expected
status creates a different opaque series instead of splicing unrelated measurements.

Fleet charts latency and availability/failure through the shared probe chart path. Successful
responses and completed URL status mismatches contribute real latency; transport failures contribute
an attempted failure with no latency. Missing telemetry contributes nothing. `actual_status` is
displayed in the latest-result view but is deliberately not retained or charted: the predefined
expected-code match is the stable success contract, while a categorical code-over-time graph would
add complexity without improving the latency/availability view.

### Device observations

Device inventory is categorical and **live-only**: it explains what was discovered and why a provider
has no numeric sample, but retaining changing display/status blobs would not produce a meaningful
time series. Numeric `device_samples` are charted. The controller retains only their bounded
collection time, opaque exact device series, metric key, and value; it does not persist inventory
labels, status details, or probe-like executable policy.

The authenticated history query selects one exact `device_kind + device_id` at a time. Fleet displays
the selected live inventory item and uses the shared time-series chart for every numeric definition
applicable to that kind. All future numeric device definitions must enter the shared metric catalog
and register a controller projector before production emission; they cannot appear as latest-only
opaque values.

Probe and device histories share the ordinary telemetry-history cap, private JSONL custody,
off-heartbeat flusher, stable bucket grid, and a **global** 1000-bucket response budget with resource
history. Server aggregation omits empty metrics and buckets. A valid numeric zero remains a charted
zero; missing collection remains a gap and is never zero-filled.

## Implementation verification (2026-07-17)

This contract was re-verified on 2026-07-17 against the canonical policy/model packages
(`internal/model`, `internal/probepolicy`, `internal/probemetric`, and `internal/devicemetric`), the
shared render/export and agent apply paths, controller readiness/history projection, and Fleet's
controller-session, latest-result, and history-selector code. Those implementations and their
focused contract tests are authoritative if a later change makes this document stale.

## Cross-references

- Reliable heartbeat transport and retained resource/probe/device samples:
  [telemetry-history.md](telemetry-history.md)
- Bundle verification and last-known-good apply: [../controller/agent.md](../controller/agent.md)
- Deploy/keystone boundary: [../controller/deploy.md](../controller/deploy.md)
- Node topology model: [../data-model/node.md](../data-model/node.md)
