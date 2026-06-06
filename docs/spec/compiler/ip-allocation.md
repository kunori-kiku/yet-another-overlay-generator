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

## Address-family and CIDR-size constraints

The allocator is IPv4-only. These rules are normative.

- A domain `cidr` (and any `transit_cidr` / `extra_prefixes`) consumed by allocation MUST be an
  IPv4 CIDR. IPv6 and any non-IPv4 family MUST be rejected at schema validation, never reaching the
  allocator.
- The host-bit count derived from a CIDR MUST be bounded: the allocator MUST reject prefixes too
  small to allocate a usable host from, and MUST NOT attempt to enumerate prefixes large enough to
  spin the CPU (a `/0` implies ~4.29 billion candidates).

These are validator obligations; the allocator MAY additionally guard, but MUST NOT be the first
line of defense. See [validation.md](validation.md) for the IPv4-only and CIDR-size validation rules.

> **Compliance:** schema validation accepts any address family (`net.ParseCIDR`, `schema.go:89`), so
> an IPv6 domain CIDR reaches the allocator, where `ipToUint32` slices `ip[12:16]` on a nil `To4()`
> result and panics — aborting the request with no recover middleware (`ip.go:129,164-169`;
> D4/D35/D20). A `/0` CIDR computes `totalHosts := uint32(1) << 32` (`ip.go:116`), which overflows
> and produces a degenerate loop bound, causing a multi-billion-iteration spin per request (D56).
> The IPv4-family guard and CIDR-size bound are the target. Closed by Plan 3.

## Determinism and renumbering caveats

The overlay-IP pass is already stable across recompiles by the skip-set mechanism above: a node that
already holds a valid in-CIDR overlay IP keeps it. This is the proven pattern the broader
sticky-pin allocation work generalizes.

Two caveats hold until that work lands:

- **Other allocated values are NOT yet identity-stable.** Listen ports, transit IP pairs, and Babel
  link-locals are assigned by positional counters over `topo.Edges` order, not bound to a stable
  link identity, so they are stable on a pure append but SHIFT on any reorder, delete-and-re-add, or
  enable/disable of an unrelated edge (audit theme T13). Only the overlay IP survives those
  operations today. The order-independence and identity-binding guarantees, and the sticky-pin
  mechanism that delivers them, are specified in
  [allocation-stability.md](allocation-stability.md) (invariants I1–I10).
- **Editing a domain `cidr` renumbers.** The allocator clears any overlay IP that falls outside its
  domain's (possibly changed) CIDR and reallocates it; changing a domain's CIDR therefore renumbers
  every node whose old IP no longer fits. Renumbering MUST be an explicit consequence of editing the
  CIDR, never a side effect of adding an unrelated node — that explicit-renumber guarantee is part
  of the allocation-stability contract (invariant I7).
