# Sysctl Configuration Rendering

`99-overlay.conf`:
- Forwarding nodes: `net.ipv4.ip_forward = 1`, `rp_filter = 0`
- Non-forwarding nodes: `rp_filter = 2` (loose mode for Babel compatibility)
