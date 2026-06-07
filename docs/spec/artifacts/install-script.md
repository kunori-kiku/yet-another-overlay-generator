# Install Script

Generated per-node bash script with phases:
- **Uninstall mode** (`--uninstall` / `-u`): Complete teardown — stops all WG interfaces, disables
  Babel, removes configs, removes SNAT rules, removes dummy0, removes systemd services
- **Phase 0**: Cleanup previous installation (managed + legacy interfaces)
- **Phase 1**: Environment preparation — checksum verification, dependency installation, dummy0
  interface creation with overlay IP, SNAT source address fix. When any link uses `transport: "tcp"`,
  also installs the `mimic` package, `modprobe mimic` (persisted), and checks kernel-eBPF support
- **Phase 2**: Configuration deployment — copies WG configs, Babel config, sysctl config
- **Phase 3**: Activation — applies sysctl; for mimic nodes, detects the egress NIC, writes
  `/etc/mimic/<egress>.conf` (one filter per mimic listen port) and starts `mimic@<egress>` **before**
  bringing up WireGuard; then starts WG interfaces, configures babeld systemd override, shows status

mimic teardown (one filter per mimic link on the egress NIC, MTU −12 per mimic interface) is detailed
in [mimic.md](./mimic.md); uninstall stops/disables `mimic@<egress>`, removes its config and
modules-load entry, and detaches.

## Source Address Fix (SNAT)

The per-peer WireGuard model uses transit IPs (`10.10.0.0/24`) on tunnel interfaces. Without a
fix, outgoing packets to overlay destinations use the transit IP as the source instead of the
overlay IP, causing `ping <overlay_ip>` to silently fail. The install script adds an SNAT rule
(nftables preferred, iptables fallback) that rewrites transit source IPs to the node's overlay IP
on all `wg-*` interfaces. A persistent `overlay-snat.service` systemd unit ensures the rule
survives reboots.

## Uninstall Support

Both the per-peer install script and the client install script support a `--uninstall` (or `-u`)
flag:

```bash
sudo bash install.sh --uninstall
```

The uninstall operation:
1. Stops and disables all managed WireGuard interfaces
2. Stops and disables ALL remaining WireGuard interfaces (catches any leftovers)
3. Removes all WireGuard config files from `/etc/wireguard/`
4. (Per-peer nodes) Stops and disables Babel, removes Babel configs and systemd overrides
5. Removes sysctl overlay configuration and re-applies system defaults
6. (Per-peer nodes) Removes the `dummy0` overlay interface and its `overlay-dummy.service`
7. Reloads systemd daemon
