# Pass 3c: Peer Derivation (`DerivePeers`) — Two-Phase Algorithm

## Phase 1 — Resource Pre-allocation

For each enabled, unique node pair:
1. Allocate a transit IP pair from `10.10.0.0/24` (sequential: `10.10.0.1/2`, `10.10.0.3/4`, ...)
2. Allocate an IPv6 link-local pair (`fe80::1/2`, `fe80::3/4`, ...)
3. Allocate listen ports for both ends: `base_port + per_node_offset++`
4. Store in `pairAllocation` map (keyed both directions)

## Phase 2 — PeerInfo Construction

For each enabled edge:
1. Look up the pre-allocated resources
2. Resolve endpoint: user-specified port takes priority, otherwise use the remote peer's allocated port
3. Compute PersistentKeepalive: 25s if the initiator cannot accept inbound OR there is no reverse edge
4. Generate WireGuard interface name: `wg-<remote_name>` (max 15 chars, Linux limit)
5. Set AllowedIPs to `0.0.0.0/0, ::/0` (per-peer model — routing handled by Babel)
6. Auto-generate the reverse peer (unless target is a client)

**Client handling:** Client nodes get a single `wg0` interface via `DeriveClientConfigs`, not
per-peer interfaces.

## Transit IP Allocation

Sequential from `10.10.0.0/24`:
```
Pair 0: 10.10.0.1, 10.10.0.2
Pair 1: 10.10.0.3, 10.10.0.4
Pair N: 10.10.0.(2N+1), 10.10.0.(2N+2)
```

IPv6 link-local follows the same pattern: `fe80::1/2`, `fe80::3/4`, ...

## WireGuard Interface Naming

```
wg-<lowercase_remote_name>  (max 15 chars, Linux kernel limit)
```
Non-alphanumeric characters (except `-`) are replaced with `-`.

## PersistentKeepalive Logic

Set to `25` (seconds) when:
- The initiating node (`from`) cannot accept inbound connections, OR
- There is no reverse edge (i.e., the remote node has no edge pointing back)

This ensures NAT-traversal keepalive for nodes behind NAT.
