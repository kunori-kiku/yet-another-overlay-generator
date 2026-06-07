# Babel Configuration Rendering

Per-node `babeld.conf`:
- `router-id`: Stable MAC-48 derived from SHA-256 of node ID
- `local-port 33123` (Babel control socket)
- Route redistribution rules based on role semantics (self /32, domain CIDR, extra prefixes, default route)
- Interface declarations: one per WireGuard tunnel, type `tunnel`, with configurable rxcost

## Router ID Generation

Stable Babel router-id from SHA-256 of node ID:
```
SHA-256(nodeID) → take first 6 bytes → set locally-administered bit, clear multicast bit → format as MAC-48
```
