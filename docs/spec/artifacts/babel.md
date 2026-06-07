# Babel Configuration Rendering

Per-node `babeld.conf`:
- `router-id`: Stable MAC-48 derived from SHA-256 of node ID
- `local-port 33123` (Babel control socket)
- Route redistribution rules based on role semantics (self /32, domain CIDR, extra prefixes, default route)
- Interface declarations: one per WireGuard tunnel, type `tunnel`, with configurable rxcost

## Link cost resolution (parallel links / failover)

Each link's `rxcost` is resolved in the compiler (stamped onto `PeerInfo.LinkCost`) by the first
matching rule:

1. **Explicit operator setting** — edge `priority`/`weight` non-zero → the D63 mapping, verbatim.
2. **Backup preset** — `role: "backup"` → **384** (4× the babeld wired default of 96), so Babel
   never prefers a backup while the primary link is alive, while multi-hop alternatives still
   compare sanely.
3. **Default** — `0` → the rxcost token is omitted and babeld's built-in default applies.

The renderer emits one `interface <name> ... rxcost <cost>` stanza per PeerInfo; two parallel
links toward the same neighbor are two stanzas with distinct interface names
([naming.md](naming.md) §Edge-aware names) and distinct costs. **Failover semantics require a
cost gap**: if every link of a multi-link pair resolves to the same effective cost, Babel has no
preference and the configuration expresses no failover — the validator emits a warning
([../compiler/validation.md](../compiler/validation.md)).

## Router ID Generation

Stable Babel router-id from SHA-256 of node ID:
```
SHA-256(nodeID) → take first 6 bytes → set locally-administered bit, clear multicast bit → format as MAC-48
```
