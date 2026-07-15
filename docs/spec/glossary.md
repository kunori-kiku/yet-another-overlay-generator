# Glossary

| Term | Definition |
|---|---|
| **Overlay IP** | The virtual IP assigned to a node within the overlay network |
| **Transit IP** | Point-to-point IP used on a per-peer WireGuard interface (from `10.10.0.0/24`) |
| **Link-local** | IPv6 `fe80::/10` address used by Babel for neighbor discovery |
| **dummy0** | A Linux dummy interface used to host the stable overlay IP (independent of tunnels) |
| **Table = off** | WireGuard option that disables automatic routing table entries (Babel manages routes instead) |
| **Per-peer interface** | Architecture where each WireGuard peer gets a dedicated network interface |
| **Canonical bundle** | The complete per-node directory keyed only by `node.ID`; its members are the single input to `checksums.sha256` and optional bundle signing |
| **Canonical member** | A file returned by `artifacts.BundleFiles`, including `install.sh` and `README.txt`; every member is hashed by `checksums.sha256` |
| **AirGap custody** | Local/offline compilation in which private WireGuard keys may be rendered into the exported bundle; direct `deploy-all` execution is allowed for this custody mode |
| **AgentHeld custody** | Controller compilation in which private WireGuard keys remain on the node; project-level `deploy-all` files are fail-closed guidance and installation goes through enrolled agents or `yaog-agent kit apply` |
| **Keystone** | The operator-controlled approval credential whose signature authorizes a manual-node kit operation; it may be Ed25519 or WebAuthn-backed |
