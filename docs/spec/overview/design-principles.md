# Key Design Principle: Per-Peer WireGuard Interfaces

Unlike traditional WireGuard setups that multiplex all peers onto a single `wg0` interface, YAOG
implements a **per-peer interface model**. Each peer-to-peer connection gets a dedicated WireGuard
interface (e.g., `wg-alpha`, `wg-beta`), each with:

- An independently allocated listen port (base port + offset)
- A dedicated point-to-point transit IP pair (`10.10.0.0/24` pool)
- An IPv6 link-local address pair (for Babel neighbor discovery)

This enables Babel to treat each tunnel as an independent routing interface with per-link metrics,
cost tuning, and fault isolation.

**Exception:** `client` role nodes use a single `wg0` interface (standard WireGuard client
behavior) and do not run Babel.
