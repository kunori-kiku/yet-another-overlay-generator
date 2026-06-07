# WireGuard Configuration Rendering

## Per-Peer Configuration

Each per-peer interface config contains:
- `[Interface]`: Private key, transit IP `/32`, `Table = off` (Babel manages routing), ListenPort, MTU
- `PostUp/PostDown`: IPv6 link-local address for Babel; optional client overlay IP route injection
- `[Peer]`: Single peer with public key, AllowedIPs (`0.0.0.0/0, ::/0`), optional Endpoint, optional PersistentKeepalive

## Client wg0 Configuration

Single-interface config:
- `[Interface]`: Private key, overlay IP `/32`, ListenPort, MTU
- `[Peer]`: Router's public key, AllowedIPs = domain CIDR, Endpoint, PersistentKeepalive = 25
