# Pass 3: IP Allocation (`allocator.AllocateIPs`)

- Clears overlay IPs that fall outside their domain's CIDR (handles domain CIDR changes)
- Sequentially allocates from domain CIDR, skipping network/broadcast, reserved ranges, and
  already-used IPs

## Allocation Algorithm

Sequential allocation from domain CIDR:
1. Parse CIDR to get network base and host bits
2. Start from host address 1 (skip network address)
3. End before last address (skip broadcast)
4. Skip reserved ranges and already-used IPs
5. Return first available

Already-set overlay IPs are preserved across recompiles (the allocator skips any node whose
`overlay_ip` is non-empty and still inside its domain CIDR).
