# Glossary

| Term | Definition |
|---|---|
| **Overlay IP** | The virtual IP assigned to a node within the overlay network |
| **Transit IP** | Point-to-point IP used on a per-peer WireGuard interface (from `10.10.0.0/24`) |
| **Link-local** | IPv6 `fe80::/10` address used by Babel for neighbor discovery |
| **dummy0** | A Linux dummy interface used to host the stable overlay IP (independent of tunnels) |
| **Table = off** | WireGuard option that disables automatic routing table entries (Babel manages routes instead) |
| **Per-peer interface** | Architecture where each WireGuard peer gets a dedicated network interface |
| **Self-extracting installer** | A bash script with an embedded base64 tar.gz payload |
