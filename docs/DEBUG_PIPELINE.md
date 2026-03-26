# Debug Pipeline: Topologically Connected but Unpingable Nodes

This document provides a systematic, layer-by-layer debug pipeline for the scenario where YAOG-compiled nodes have valid topology edges, configs are deployed, and services appear running — yet overlay IPs cannot be pinged.

Run every step **on both ends** (source and destination) unless noted otherwise.

---

## Quick Reference Flowchart

```
                         Nodes unpingable
                              │
              ┌───────────────┼───────────────┐
              ▼               ▼               ▼
         Layer 1          Layer 2          Layer 3
       WireGuard           Babel         System/Net
              │               │               │
    ┌─────────┤         ┌─────┤         ┌─────┤
    ▼         ▼         ▼     ▼         ▼     ▼
 Interface  Handshake  Daemon Routes  sysctl  Firewall
  exists?   recent?    alive? exist?  fwd?    blocking?
    │         │         │     │         │       │
    No→[S1]   No→[S3]   No→[S5] No→[S7] No→[S9] Yes→[S10]
    │         │         │     │
    ▼         ▼         ▼     ▼
  Keys     Endpoint   Config  dummy0
  match?   correct?   valid?  has IP?
    │         │         │       │
    No→[S2]   No→[S4]   No→[S6] No→[S8]
```

---

## Prerequisites

Before starting, collect the following on each node:

```bash
# Node identity
hostname
cat /etc/os-release | head -2

# What YAOG deployed
cat /etc/wireguard/wg-*.conf 2>/dev/null | head -5
ls /etc/wireguard/
ls /etc/babel/ 2>/dev/null
cat /etc/sysctl.d/99-overlay.conf
```

---

## Stage 1: WireGuard Interface Layer

### S1. Verify interfaces exist and are up

```bash
# List all WireGuard interfaces
wg show interfaces

# Verify each expected interface is present
ip -brief link show type wireguard
```

**Expected:** Every per-peer interface from the compiled topology (e.g., `wg-alpha`, `wg-beta`) should appear.

**If missing:**
```bash
# Check if config file exists
ls -la /etc/wireguard/wg-*.conf

# Try to bring it up manually and read the error
wg-quick up wg-<peer-name>

# Common errors:
# - "RTNETLINK: File exists" → interface half-created; ip link del wg-<name> first
# - "Configuration file not found" → install script didn't deploy configs
# - "Unable to access interface: Protocol not supported" → WireGuard kernel module not loaded
```

```bash
# Check WireGuard kernel module
lsmod | grep wireguard
# If missing:
modprobe wireguard
```

### S2. Verify WireGuard keys match

On **Node A**, extract the public key it expects for Node B:

```bash
# Show all peers and their public keys
wg show wg-<nodeB-name>
```

On **Node B**, verify the local private key derives the expected public key:

```bash
# Extract private key from config
grep PrivateKey /etc/wireguard/wg-<nodeA-name>.conf

# Derive public key from it (paste the private key)
echo "<private-key>" | wg pubkey
```

**The derived public key must match what Node A has under `[Peer] PublicKey`.**

**If mismatched:**
- Keys were regenerated between compiles (expected if `fixed_private_key` is not set)
- Recompile and redeploy both nodes from the same compilation run

### S3. Verify WireGuard handshake

```bash
wg show wg-<peer-name>
```

Check the `latest handshake` field:

| Value | Meaning | Action |
|---|---|---|
| Within last 2 minutes | Handshake active | WG tunnel is alive → skip to Stage 2 |
| Several minutes ago | Stale handshake | Peer may be offline or unreachable |
| `(none)` | No handshake ever | Endpoint or port problem → S4 |

If stale or none, **force a handshake attempt**:

```bash
# Ping the remote transit IP (point-to-point link address)
# Check the config for the remote transit IP:
grep -A5 '\[Peer\]' /etc/wireguard/wg-<peer-name>.conf

# The transit IP of the remote end is NOT in the conf —
# it's the Address line of the remote node's matching interface.
# Use the AllowedIPs or the overlay IP as a quick test:
ping -c3 -I wg-<peer-name> <remote-transit-ip>
```

### S4. Verify endpoint and port reachability

```bash
# What endpoint is configured?
wg show wg-<peer-name> endpoints

# Test UDP connectivity to that endpoint
# (from a machine that has internet/direct access)
nc -uzv <endpoint-host> <endpoint-port>

# Or use nmap for UDP:
nmap -sU -p <endpoint-port> <endpoint-host>
```

**Common issues:**
- **Wrong port**: The `endpoint_port` in the edge doesn't match the remote node's allocated `ListenPort` for this specific per-peer interface. After compiling, use the `compiled_port` value (visible in the UI) or check:
  ```bash
  # On the REMOTE node, check what port this interface is listening on:
  grep ListenPort /etc/wireguard/wg-<your-name>.conf
  ```
- **NAT/Firewall**: The port is not forwarded on the remote's router. Verify with the remote host's public IP.
- **Dynamic IP changed**: If using a hostname/DDNS, verify it resolves correctly:
  ```bash
  dig +short <endpoint-host>
  ```
- **No endpoint at all**: If both nodes are behind NAT with no public endpoint, WireGuard cannot establish a direct connection. One node must be publicly reachable or use a relay.

### S4b. Verify ListenPort is actually bound

On the **remote node**:

```bash
# Check that WireGuard is actually listening on the expected port
ss -ulnp | grep <listen-port>

# If nothing shows, the interface may not be up:
wg-quick up wg-<interface-name>
```

---

## Stage 2: Transit IP / Point-to-Point Link Layer

### S5. Verify transit IPs are assigned

```bash
# Check each WG interface has its transit IP
ip addr show dev wg-<peer-name>

# Expected: a /32 address from 10.10.0.0/24
# Example output:
#   inet 10.10.0.1/32 scope global wg-alpha
```

**If missing:**
```bash
# Check the config
grep Address /etc/wireguard/wg-<peer-name>.conf
# Should show something like: Address = 10.10.0.1/32

# If present in config but not on interface, restart:
wg-quick down wg-<peer-name> && wg-quick up wg-<peer-name>
```

### S5b. Ping the remote transit IP through the tunnel

```bash
# Find the remote transit IP for this link:
# It's the "other" address in the pair (e.g., if you're 10.10.0.1, remote is 10.10.0.2)
ping -c3 10.10.0.2
```

**If this works but overlay IP doesn't**: The WireGuard tunnel is functional — the problem is in routing (Stage 2 or Stage 3).

**If this fails**: The tunnel itself is broken — go back to Stage 1.

### S5c. Verify IPv6 link-local addresses (Babel requires these)

```bash
ip -6 addr show dev wg-<peer-name>

# Expected: a fe80:: address
# Example: fe80::1/64

# If missing, the PostUp command may have failed:
ip -6 addr add fe80::1/64 dev wg-<peer-name>
```

---

## Stage 2: Babel Routing Layer

### S6. Verify babeld is running

```bash
systemctl status babeld

# Check the actual process
pgrep -a babeld

# Check which config file it's using
cat /etc/systemd/system/babeld.service.d/override.conf
```

**If not running:**
```bash
# Check for config errors
babeld -c /etc/babel/babeld.conf -d 1 --check-config 2>&1

# Common errors:
# - "unknown interface wg-xxx" → interface not up when babeld started
# - Syntax errors in config

# Restart with correct ordering:
# 1. Ensure all WG interfaces are up first
wg show interfaces
# 2. Then restart babeld
systemctl restart babeld
```

### S6b. Verify Babel config is correct

```bash
cat /etc/babel/babeld.conf
```

Check:
- `router-id` is present and unique per node
- All expected WG interfaces are listed under `interface <name> type tunnel`
- Redistribute rules include the node's overlay IP `/32`
- For routers/relays: domain CIDR should also be redistributed

### S7. Check Babel neighbor discovery

```bash
# Connect to Babel control socket
echo "dump" | nc -q1 ::1 33123
```

Or interactively:

```bash
nc ::1 33123
# Then type:
dump
```

**Look for:**

```
neighbour <id> address fe80::2 if wg-alpha reach ffff ureach 0000 rxcost 96 txcost 96 cost 96
```

| Field | Good Value | Bad Value | Meaning |
|---|---|---|---|
| `reach` | `ffff` | `0000` or partial | Reachability bitmask (all 1s = perfect) |
| `ureach` | `0000` | `ffff` | Unreachability bitmask (all 0s = good) |
| `rxcost` | 96 (wired/tunnel) | 65535 or infinity | Receive cost |
| `cost` | Finite number | `infinity` | Computed path cost |

**If no neighbors appear:**
- Babel cannot see the remote end on any interface
- Check that the remote node's babeld is also running
- Verify IPv6 link-local addresses exist on both ends (S5c)
- Verify babeld is configured to use the correct interface names

**If `reach` is `0000`:**
- Babel hello packets are not getting through
- Usually means the WG tunnel is not passing traffic (go back to S3)

### S7b. Check Babel routes to the target overlay IP

```bash
# Look for a route to the destination overlay IP
echo "dump" | nc -q1 ::1 33123 | grep <target-overlay-ip>
```

**Expected:**
```
route <prefix>/32 from <source> metric 96 ... installed yes
```

Key fields:
- `installed yes` → route is in the kernel routing table
- `metric` → finite number (not infinity)

**If route exists but `installed no`:**
- A better route exists (check for duplicates)
- Or the route is filtered by redistribute rules

**If route is completely missing:**
- The remote node is not announcing it (check remote babeld config redistribute rules)
- Or there's no path through the mesh to reach it

### S7c. Verify kernel routing table matches Babel

```bash
# Check what the kernel thinks
ip route show table main | grep <target-overlay-ip>

# Expected for a per-peer setup:
# <target-ip>/32 via <next-hop-transit-ip> dev wg-<name> proto babel ...
# OR
# <target-ip>/32 dev wg-<name> proto babel ...

# Also check for conflicting routes:
ip route show table all | grep <target-overlay-ip>
```

**If Babel knows the route but it's not in the kernel:**
- `skip-kernel-setup` might be set to `true` incorrectly (check babeld.conf — YAOG sets it to `false`)
- Route table conflict: another table may have a conflicting entry

---

## Stage 3: System and Network Layer

### S8. Verify dummy0 interface and overlay IP

```bash
ip addr show dev dummy0

# Expected:
#   inet <overlay-ip>/32 scope global dummy0

# If dummy0 doesn't exist:
ip link add dummy0 type dummy
ip addr add <overlay-ip>/32 dev dummy0
ip link set dummy0 up
```

**Why this matters:** The overlay IP lives on `dummy0`, not on any WG interface. Traffic destined for a node's overlay IP arrives via a WG tunnel and is delivered to `dummy0`. If `dummy0` is missing or has the wrong IP, the node can receive traffic but the kernel won't accept it.

Also verify the systemd service:
```bash
systemctl status overlay-dummy.service
```

### S9. Verify sysctl (IP forwarding and rp_filter)

```bash
# Check IP forwarding (required for router/relay/gateway roles)
sysctl net.ipv4.ip_forward

# Check reverse path filtering (must be 0 for forwarders, 2 for peers)
sysctl net.ipv4.conf.all.rp_filter
sysctl net.ipv4.conf.default.rp_filter

# Check per-interface rp_filter (sometimes overrides the global)
for iface in $(wg show interfaces); do
    echo "$iface: $(sysctl -n net.ipv4.conf.$iface.rp_filter)"
done
```

| Role | `ip_forward` | `rp_filter` |
|---|---|---|
| router, relay, gateway | `1` | `0` |
| peer | `0` | `2` |
| client | `0` | (any) |

**If `ip_forward = 0` on a router/relay/gateway:** Traffic transiting through this node is silently dropped.

```bash
# Fix immediately:
sysctl -w net.ipv4.ip_forward=1

# Or re-apply the overlay sysctl:
sysctl -p /etc/sysctl.d/99-overlay.conf
```

**If `rp_filter = 1` (strict) on a forwarding node:** Asymmetric routes (common in mesh networks) will be dropped.

```bash
sysctl -w net.ipv4.conf.all.rp_filter=0
sysctl -w net.ipv4.conf.default.rp_filter=0
```

### S9b. Verify overlay SNAT rule (source address fix)

The per-peer model uses transit IPs on WG interfaces. Without an SNAT rule, `ping <overlay_ip>` fails because the kernel uses the transit IP as source, and the reply is unroutable.

```bash
# Check nftables SNAT rule
sudo nft list table inet overlay-snat 2>/dev/null

# Check iptables SNAT rule (if nft not used)
sudo iptables -t nat -L POSTROUTING -n -v 2>/dev/null | grep SNAT

# Check the systemd service
systemctl status overlay-snat.service
```

**Expected:** One of the above should show an SNAT rule rewriting `10.10.0.0/24` to the node's overlay IP on `wg-*` interfaces.

**If missing:**
```bash
# Quick fix — replace <OVERLAY_IP> with the node's overlay IP
sudo nft add table inet overlay-snat
sudo nft add chain inet overlay-snat postrouting '{ type nat hook postrouting priority srcnat; policy accept; }'
sudo nft add rule inet overlay-snat postrouting oifname "wg-*" ip saddr 10.10.0.0/24 snat to <OVERLAY_IP>
```

**Symptom without SNAT:** `ping -I <overlay_ip> <target>` works but `ping <target>` does not.

### S10. Check firewall rules

```bash
# iptables
iptables -L -n -v | head -40
iptables -L -n -v -t nat | head -20

# nftables (modern distros)
nft list ruleset 2>/dev/null | head -40

# ufw (Ubuntu)
ufw status verbose 2>/dev/null

# firewalld (RHEL/CentOS)
firewall-cmd --list-all 2>/dev/null
```

**Check for:**
1. **INPUT chain drops on WG listen ports (UDP)**:
   ```bash
   # List all WG listen ports
   for conf in /etc/wireguard/wg-*.conf; do
       name=$(basename "$conf" .conf)
       port=$(grep ListenPort "$conf" | awk '{print $3}')
       echo "$name: UDP/$port"
   done

   # Verify they're not blocked
   iptables -L INPUT -n -v | grep -E "udp.*($port1|$port2|...)"
   ```

2. **FORWARD chain drops overlay traffic** (for router/relay/gateway):
   ```bash
   iptables -L FORWARD -n -v
   # If policy is DROP, you need explicit ACCEPT rules for WG interfaces:
   iptables -A FORWARD -i wg-+ -j ACCEPT
   iptables -A FORWARD -o wg-+ -j ACCEPT
   ```

3. **OUTPUT chain drops** (rare, but check):
   ```bash
   iptables -L OUTPUT -n -v
   ```

### S10b. Check for Docker/container interference

Docker adds its own iptables rules that can interfere:

```bash
iptables -L DOCKER-USER -n -v 2>/dev/null
iptables -L FORWARD -n -v | grep -i docker
```

If Docker's FORWARD chain has a blanket DROP:
```bash
iptables -I DOCKER-USER -i wg-+ -j ACCEPT
iptables -I DOCKER-USER -o wg-+ -j ACCEPT
```

---

## Stage 4: End-to-End Verification

### S11. Targeted ping with specific source and interface

```bash
# Ping from overlay IP through a specific interface
ping -c3 -I <local-overlay-ip> <remote-overlay-ip>

# Ping with increased verbosity
ping -c3 -v <remote-overlay-ip>
```

### S12. Traceroute through the overlay

```bash
traceroute -n -s <local-overlay-ip> <remote-overlay-ip>

# Or with mtr for continuous monitoring:
mtr -n -s <local-overlay-ip> <remote-overlay-ip>
```

This reveals where packets are being dropped in the mesh path.

### S13. Packet capture on WireGuard interface

```bash
# On the SENDING node — verify packets are entering the tunnel
tcpdump -i wg-<peer-name> -n icmp

# On the RECEIVING node — verify packets are arriving
tcpdump -i wg-<peer-name> -n icmp

# On the RECEIVING node — check dummy0 for delivered packets
tcpdump -i dummy0 -n icmp
```

**Diagnosis from tcpdump:**

| Sending WG iface | Receiving WG iface | Receiving dummy0 | Diagnosis |
|---|---|---|---|
| Packets seen | Packets seen | Packets seen | Working — check ICMP reply path |
| Packets seen | No packets | — | Tunnel not passing traffic (S3/S4) |
| Packets seen | Packets seen | No packets | Routing issue: kernel not delivering to dummy0 (S7c/S8) |
| No packets | — | — | Local routing not sending to the WG interface (S7c) |

### S14. Check ICMP reply path (asymmetric routing)

If pings go OUT but replies don't come back:

```bash
# On the REMOTE node, capture to see if the reply is sent:
tcpdump -i any -n icmp

# Check which interface the reply exits from:
ip route get <source-overlay-ip> from <destination-overlay-ip>
```

Asymmetric routing (reply goes via a different path) is normal in mesh networks but can be broken by strict `rp_filter`.

---

## Stage 5: Client-Specific Debugging

Client nodes use a single `wg0` interface and do not run Babel.

### S15. Client wg0 basics

```bash
# Interface up?
wg show wg0

# Handshake recent?
wg show wg0 | grep "latest handshake"

# Correct AllowedIPs? (should be the domain CIDR, e.g., 10.0.0.0/24)
wg show wg0 allowed-ips
```

### S16. Client routing

Clients don't use Babel — routing is purely WireGuard-based:

```bash
# Check that traffic to the overlay is routed through wg0
ip route get <target-overlay-ip>

# Expected: ... dev wg0 ...
# If it goes through the default route instead, the AllowedIPs is wrong
```

### S17. Router-side client peer

On the **router** that the client connects to:

```bash
# Check the per-peer interface for the client
wg show wg-<client-name>

# Verify the PostUp route was created:
ip route show | grep <client-overlay-ip>
# Expected: <client-overlay-ip>/32 dev wg-<client-name>

# Verify Babel is redistributing the client's overlay IP:
echo "dump" | nc -q1 ::1 33123 | grep <client-overlay-ip>
```

---

## Common Pitfalls Summary

| # | Symptom | Root Cause | Fix |
|---|---|---|---|
| 1 | No handshake, endpoint looks correct | UDP port blocked by firewall | Open UDP port on remote host |
| 2 | Handshake OK, transit ping fails | `Table = off` not set, conflicting routes | Check WG config has `Table = off` |
| 3 | Transit ping OK, overlay ping fails | dummy0 missing or wrong IP | Recreate dummy0 with correct overlay IP |
| 4 | Direct peer OK, multi-hop fails | IP forwarding disabled on intermediate node | `sysctl -w net.ipv4.ip_forward=1` |
| 5 | Forwarding enabled, still dropped | `rp_filter = 1` (strict) dropping asymmetric traffic | Set `rp_filter = 0` on all WG interfaces |
| 6 | Babel running, no neighbors | Missing IPv6 link-local on WG interfaces | Check PostUp ran: `ip -6 addr show dev wg-*` |
| 7 | Babel neighbors OK, no routes | Missing `redistribute local ip <overlay>/32 allow` | Check babeld.conf redistribute rules |
| 8 | Route exists in Babel, not in kernel | babeld `skip-kernel-setup true` | Set to `false` in babeld.conf |
| 9 | Keys mismatch | Recompiled without `fixed_private_key` | Redeploy both ends from same compile |
| 10 | Port mismatch | `endpoint_port` doesn't match remote `ListenPort` | Use `compiled_port` or recompile |
| 11 | Client can't reach non-router nodes | Router not redistributing client IP in Babel | Check router babeld.conf has client IP in redistribute |
| 12 | Works then stops after minutes | PersistentKeepalive not set for NAT node | Verify keepalive=25 on NAT-side edges |
| 13 | Docker FORWARD chain dropping | Docker's default iptables rules | `iptables -I DOCKER-USER -i wg-+ -j ACCEPT` |
| 14 | `ping <overlay>` fails, `ping -I <overlay> <target>` works | Missing SNAT rule — kernel uses transit IP as source | Add overlay SNAT rule (see S9b) |
| 15 | Everything looks right, still fails | Stale WG interface from previous install | `--uninstall` then fresh install |

---

## Automated Diagnostic Script

Run this on any node for a quick health snapshot:

```bash
#!/usr/bin/env bash
# YAOG node diagnostic — paste and run as root

set -uo pipefail
echo "===== YAOG Node Diagnostic ====="
echo "Hostname: $(hostname)"
echo "Date: $(date -Iseconds)"
echo ""

echo "--- WireGuard Interfaces ---"
wg show interfaces 2>/dev/null || echo "(none)"
echo ""

echo "--- WireGuard Status ---"
for iface in $(wg show interfaces 2>/dev/null); do
    echo "[$iface]"
    wg show "$iface" | grep -E 'public key|endpoint|latest handshake|transfer'
    ip addr show dev "$iface" 2>/dev/null | grep inet
    echo ""
done

echo "--- dummy0 ---"
ip addr show dev dummy0 2>/dev/null || echo "(dummy0 not found)"
echo ""

echo "--- sysctl ---"
echo "ip_forward = $(sysctl -n net.ipv4.ip_forward)"
echo "rp_filter (all) = $(sysctl -n net.ipv4.conf.all.rp_filter)"
echo ""

echo "--- Babel ---"
if pgrep -x babeld >/dev/null; then
    echo "babeld: running (PID $(pgrep -x babeld))"
    echo ""
    echo "Neighbors:"
    echo "dump" | nc -q1 ::1 33123 2>/dev/null | grep "^neighbour" | \
        awk '{for(i=1;i<=NF;i++) if($i=="if"||$i=="reach"||$i=="cost"||$i=="address") printf "%s %s  ", $i, $(i+1); print ""}'
    echo ""
    echo "Installed routes:"
    echo "dump" | nc -q1 ::1 33123 2>/dev/null | grep "^route" | grep "installed yes" | \
        awk '{print $2, "metric", $6, "via", $8}'
else
    echo "babeld: NOT running"
fi
echo ""

echo "--- Overlay SNAT ---"
if nft list table inet overlay-snat 2>/dev/null | grep -q snat; then
    echo "nftables SNAT: active"
    nft list chain inet overlay-snat postrouting 2>/dev/null | grep snat
elif iptables -t nat -L POSTROUTING -n 2>/dev/null | grep -q SNAT; then
    echo "iptables SNAT: active"
    iptables -t nat -L POSTROUTING -n 2>/dev/null | grep SNAT
else
    echo "WARNING: No overlay SNAT rule found! ping without -I will fail."
fi
echo ""

echo "--- Firewall (INPUT UDP) ---"
iptables -L INPUT -n 2>/dev/null | grep -i udp | head -10
echo ""

echo "--- Firewall (FORWARD) ---"
iptables -L FORWARD -n 2>/dev/null | head -10
echo ""

echo "===== End Diagnostic ====="
```

Copy-paste this into a file or run inline:
```bash
curl -fsSL <your-gist-url> | sudo bash
# or just paste it directly
```
