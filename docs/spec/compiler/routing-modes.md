# Routing modes and route installation

A Domain's `routing_mode` selects how reachability is propagated across the overlay. YAOG ships a
single route installer — Babel — and this spec defines the normative contract for the
`routing_mode` enum, the conditions under which Babel artifacts are emitted, and the kernel-route
prerequisites that every announced prefix MUST satisfy to actually route.

This contract settles dossier theme T2 (routing-mode / Babel semantic gaps). Related specs:
[../data-model/domain.md](../data-model/domain.md) (the `routing_mode` field),
[pipeline.md](pipeline.md) (where normalization runs),
[../artifacts/babel.md](../artifacts/babel.md) (the `babeld.conf` renderer),
[../artifacts/install-script.md](../artifacts/install-script.md) (kernel-route anchoring),
[../roles/roles.md](../roles/roles.md) (per-role announce policy).

## Routing-mode enum

`Domain.routing_mode` is one of:

| Value | Meaning | Status |
|---|---|---|
| `babel` | Dynamic routing via babeld over the per-peer tunnels. | Implemented (only mode). |
| `static` | Operator-managed kernel routes, no routing daemon. | Reserved — rejected at validation. |
| `none` | No route installation at all. | Reserved — rejected at validation. |
| `""` (empty) | Unset; normalizes to `babel`. | Normalized at validation. |

### Normalization rule

An empty `routing_mode` MUST be normalized to `babel` during validation. The normalization MUST be
a write-back into the topology object so the value round-trips: a topology compiled with an empty
`routing_mode` MUST come back out (in the compile response and in any persisted topology) carrying
the explicit `babel` value. This eliminates the silent-empty failure mode where an unset routing
mode suppressed the routing daemon while the compile still reported success.

> **Compliance:** the schema validator accepts an empty `routing_mode` without normalizing it
> (`internal/validator/schema.go:103-107` skips the check when `domain.RoutingMode != ""` is
> false), and `shouldRunBabel` treats any value other than the literal `"babel"` — including
> `""` — as "do not run Babel" (`internal/renderer/babel.go:160-169`). The combination produces a
> green compile with zero routes (dossier D2). Closed by Plan 6 (PR #6).

### Rejection of unimplemented modes

`static` and `none` MUST be rejected by the semantic validator with a localized
not-yet-implemented error for as long as no route installer exists for them. The error MUST follow
the existing locale pattern (an entry in each supported locale, not an English-only string) and
MUST name the unimplemented mode and the only supported value (`babel`). Empty enum values MUST
NOT bypass the enum check — the validator MUST first normalize empty to `babel`, then validate the
resulting value against the supported set.

> **Compliance:** the schema validator currently lists `static`, `babel`, and `none` as equally
> valid (`internal/validator/schema.go:103`), so `static`/`none` pass validation but render no
> routing artifacts (dossier D72). Closed by Plan 6 (PR #6).

A future subject MAY implement `static` (kernel-route rendering) and/or `none`; until then they are
reserved. This mirrors the treatment of the reserved `route_policies` field
(see [../data-model/route-policy.md](../data-model/route-policy.md)).

## `Table = off` is a babel-mode artifact

Every per-peer WireGuard interface is rendered with `Table = off`, which tells `wg-quick` not to
install any cryptokey-derived kernel routes for that interface's `AllowedIPs`. This is correct
**only** in `babel` mode: with `Table = off`, babeld is the sole authority that installs overlay
routes, and the tunnels are pure L3 links it routes over. `Table = off` MUST therefore be
understood as a babel-mode artifact, not an unconditional default.

It follows that the renderer MUST NOT emit `Table = off` interfaces for a domain whose effective
`routing_mode` does not run a routing daemon. Because `static`/`none` are rejected at validation
(above), the only mode that reaches the renderer is `babel`, so `Table = off` is always paired with
a running babeld. If a non-babel mode is ever implemented, it MUST supply its own route source
before `Table = off` may be emitted (or omit `Table = off` and let `wg-quick` install
cryptokey routes).

> **Compliance:** `Table = off` is emitted unconditionally on every per-peer interface
> (`internal/renderer/wireguard.go:59`) regardless of `routing_mode`, so a non-babel mode produces
> tunnels that carry no routes from any source (dossier D2). The client `wg0` template does not set
> `Table = off` and relies on cryptokey routing instead — see
> [../artifacts/wireguard.md](../artifacts/wireguard.md). Closed by Plan 6 (PR #6).

## Kernel-route prerequisite for announced prefixes

babeld's `redistribute local` only matches **local/connected kernel routes** present on the node —
i.e. routes the kernel already owns for directly-attached addresses and networks. A `redistribute
local ip <prefix> allow` rule that has no matching kernel route on the node redistributes nothing;
the prefix is silently never announced into the overlay.

Therefore: **every prefix a node announces via `redistribute local` MUST correspond to a kernel
local/connected route on that node.** The renderer and the install script together MUST guarantee
this pairing for every prefix the role's announce policy emits.

The current announce policy (per [../roles/roles.md](../roles/roles.md)) emits up to four prefix
classes per node:

| Announced prefix | Emitting roles | Matching kernel route |
|---|---|---|
| Self `/32` (overlay IP) | all babel roles | `dummy0` carries `OverlayIP/32` (present). |
| Domain CIDR aggregate | router, relay, gateway | none today — no node owns the aggregate. |
| `extra_prefixes` (LAN nets) | router/relay if set, gateway always | none today — not installed. |
| Default route `0.0.0.0/0` | gateway | none today — node's real default points at its real gateway, not a redistributable connected route. |
| Client overlay `/32` | router with client peers | injected per-interface via `ip route replace` in the tunnel's PostUp. |

Only the self `/32` and the per-interface client `/32` have a matching kernel route today, so only
those announcements actually propagate. The domain-CIDR aggregate, `extra_prefixes`, and the
gateway default route are **dead announcements** — rendered but never routed.

> **Compliance:** the renderer emits `redistribute local ip <domain CIDR> allow` and
> `redistribute local ip 0.0.0.0/0 allow` (`internal/renderer/babel.go:99-108`) and the
> `extra_prefixes` block (`internal/renderer/babel.go:103-105`), but the install script only
> assigns the overlay `/32` to `dummy0` (`internal/renderer/script.go:302`) and installs no kernel
> route for the aggregate, the LAN prefixes, or the default. Per the babeld manual,
> `redistribute local` matches connected/local routes only, so these rules match nothing: gateway
> internet egress (dossier D40) and domain-CIDR / extra-prefix announcement (dossier D41) are
> currently dead. Closed by Plan 6 (PR #6).

### Target mechanism: anchor routes in the install script

To make announced prefixes routable, the install script MUST anchor a kernel local/connected route
for each non-self prefix the node announces, so that `redistribute local` has something to match:

- **Gateway default route** — anchor `0.0.0.0/0` as a local/connected route the node owns for
  redistribution purposes (e.g. a route on the overlay device), so babeld can originate it.
- **`extra_prefixes`** — anchor each LAN/extra prefix as a connected route (typically already
  connected on the node's real LAN interface; the install script MUST verify/install it).
- **Domain CIDR aggregate** — anchor the aggregate as a local route on the overlay device so the
  router/relay/gateway can originate the summary.

Each anchored route MUST be paired one-to-one with the `redistribute local` rule the renderer
emits for the same prefix; the renderer MUST NOT emit an announce rule for a prefix the install
script does not anchor, and vice versa.

#### Byte-identical self-`/32` protection gate

The self `/32` anchor (the `OverlayIP/32` on `dummy0`) is the proven, already-working path and MUST
NOT regress. Any new anchoring logic MUST leave the self-`/32` install path byte-identical to its
current form. The Plan 6 verification gate is a byte-identical comparison of the rendered self-`/32`
install steps before and after the change; new prefix anchors are additive and MUST NOT alter the
self-`/32` bytes.

> **Stop-loss (outline milestone 6):** if D40/D41 anchoring proves unprovable in CI, the scope
> narrows to the gateway default route and `extra_prefixes`; the domain-CIDR aggregate anchor moves
> to a follow-up redesign (plan-6.5). The self-`/32` byte-identical gate holds in all cases.

## Babel interface declarations

babeld peers over the per-peer WireGuard tunnels. The renderer declares one `interface ... type
tunnel` line per peer interface. Two normative constraints apply:

### Client-peer interfaces MUST NOT be declared as babel peering interfaces

A `client`-role node runs no babeld (see [../roles/roles.md](../roles/roles.md)). The tunnel from a
router to a client therefore has no Babel speaker on the far side. The router MUST NOT declare a
client-peer tunnel as a babeld peering interface — doing so makes babeld send hellos and updates
into a tunnel that will never answer. Interfaces whose peer is a client (the `IsClientPeer` marker
on the derived peer) MUST be skipped in the interface block. The client's reachability is instead
carried by the per-interface client-`/32` redistribution already described above.

> **Compliance:** the interface loop iterates over all derived peers without filtering
> (`internal/renderer/babel.go:84-92`), so a router's `babeld.conf` declares the client tunnel as a
> peering interface (dossier D73). Closed by Plan 6 (PR #6).

### Edge priority/weight MUST map to per-interface rxcost

`Edge.priority` and `Edge.weight` exist on the model but are never read by the Babel renderer.
These fields MUST influence babeld link cost: an edge's priority/weight MUST map to the
per-interface `rxcost` on the corresponding tunnel interface, so an operator can bias path
selection. When an edge sets no priority/weight, the interface's `rxcost` MUST fall back to the
role preset's default cost (see [../roles/roles.md](../roles/roles.md) and the presets below). An
`rxcost` of 0 means "use babeld's built-in default" and MUST be rendered by omitting the `rxcost`
token.

> **Compliance:** the interface loop uses only `preset.DefaultCost` and never reads
> `Edge.Priority`/`Edge.Weight` (`internal/renderer/babel.go:84-90`), so those fields have no
> effect (dossier D63). Closed by Plan 6 (PR #6).

## Role-preset timers and control port

`hello-interval`, `update-interval`, and the babeld `local-port` MUST come from the per-role preset
(`GetBabelRolePreset`, `internal/renderer/babel_presets.go`). The current hardcoded values are the
defaults the presets MUST carry:

| Preset field | Current default | Source of truth |
|---|---|---|
| `local-port` | `33123` | preset (control socket) |
| `hello-interval` | `4` (seconds) | preset per role |
| `update-interval` | `16` (seconds) | preset per role |
| `rxcost` default | per role (`relay` = 96; others omit) | preset `DefaultCost`, overridable by edge priority/weight |

A preset field of `0` for an interval means "omit the token and use babeld's built-in default". The
renderer MUST read these from the preset rather than embedding literals in the template, so that
per-role tuning is reachable.

> **Compliance:** the template hardcodes `local-port 33123`, `hello-interval 4`, and
> `update-interval 16` (`internal/renderer/babel.go:44,56`), while the preset's `HelloInterval` and
> `UpdateInterval` fields are present but never read (`internal/renderer/babel_presets.go:8-12`,
> all roles return `0`) (dossier D78). Closed by Plan 6 (PR #6).
