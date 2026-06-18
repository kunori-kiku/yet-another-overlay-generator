// Overlay-IP allocator — the TypeScript mirror of internal/allocator/ip.go (AllocateIPs +
// allocateFromCIDR). It assigns each node that lacks an OverlayIP the lowest free host address in its
// domain's CIDR, skipping reserved ranges and already-used addresses, exactly as the Go oracle does.
//
// Pure + side-effect-free: it returns a NEW node slice (the input topology is never mutated), mirroring
// Go's `result := make(...); copy(result, topo.Nodes)`. Every uint32 IP arithmetic step is coerced with
// `>>> 0` (via cidr.ts's helpers) to match Go's uint32 modular arithmetic byte-for-byte.
//
// Go is the authoritative oracle: the allocated overlay IPs MUST equal the Go side value-for-value — a
// wrong IP silently disagrees with the controller. The coded failures (overlay_cidr_invalid /
// overlay_pool_exhausted / overlay_scan_budget_exceeded / node_unknown_domain) mirror apierr.go.

import {
  canonicalIP,
  contains,
  parseCIDR,
  parseIPFamily,
  uint32ToIPv4,
} from './cidr';
import { CompileCode, CompileError } from './errors';
import type { Domain, Node, Topology } from './model';

// maxOverlayScanBudget caps the number of host candidates the allocator enumerates for a SINGLE node
// within one domain CIDR. Mirrors allocator/ip.go:24 (const maxOverlayScanBudget = 1 << 20). A prefix
// whose usable host span exceeds this budget is rejected fast (CodeOverlayScanBudgetExceeded) rather
// than run to completion. 1<<20 = 1,048,576.
const maxOverlayScanBudget = 1 << 20;

// allocateFromCIDR returns the lowest free host IP within cidr, skipping the supplied reserved ranges
// and already-used addresses. Mirrors allocator/ip.go:122-229 (IPAllocator.allocateFromCIDR) EXACTLY:
//   - net.ParseCIDR failure → CodeOverlayCIDRInvalid;
//   - reserved ranges parse as CIDR (reservedNets, matched via Contains) or, failing that, as a single
//     IP (reservedSingleIPs, keyed by the CANONICAL net.ParseIP(rr).String() form);
//   - hostBits==0 (/32) or hostBits>=32 → a plain (non-coded) error, matching Go's fmt.Errorf guards
//     (unreachable once schema validation enforces the /8 lower bound, kept as a safety net);
//   - startHost=1, endHost=totalHosts-1 (exclude network + broadcast); hostBits<=1 (/31) → startHost=0,
//     endHost=totalHosts (both addresses usable);
//   - scan-budget cap (endHost-startHost > maxOverlayScanBudget) → CodeOverlayScanBudgetExceeded;
//   - scan [startHost, endHost): candidate = networkIP + h (uint32), skip used / single-reserved /
//     net-reserved, return the first free; whole span scanned → CodeOverlayPoolExhausted.
function allocateFromCIDR(
  cidr: string,
  reservedRanges: string[],
  usedIPs: Set<string>,
): string {
  const ipNet = parseCIDR(cidr);
  if (ipNet === null) {
    throw new CompileError(CompileCode.OverlayCIDRInvalid, { cidr });
  }

  // Parse the reserved ranges into networks and single-IP reservations, mirroring Go's loop: try
  // net.ParseCIDR first; on failure fall back to net.ParseIP (a single IP, keyed by its canonical
  // String()); anything else is skipped.
  const reservedNets: ReturnType<typeof parseCIDR>[] = [];
  const reservedSingleIPs = new Set<string>();
  for (const rr of reservedRanges) {
    const rNet = parseCIDR(rr);
    if (rNet === null) {
      // Not a CIDR; try parsing it as a single IP (any family Go's net.ParseIP accepts). The key is
      // the canonical net.ParseIP(rr).String() form so it compares equal to the canonical candidate.
      if (parseIPFamily(rr) !== null) {
        reservedSingleIPs.add(canonicalIP(rr));
      }
      continue;
    }
    reservedNets.push(rNet);
  }

  // Determine the host-bit count from the CIDR mask.
  const hostBits = ipNet.hostBits;

  // A /32 has no assignable host addresses.
  if (hostBits === 0) {
    throw new CompileError(
      CompileCode.OverlayPoolExhausted,
      { cidr },
      `CIDR ${cidr} has no assignable host addresses (prefix too long)`,
    );
  }

  // Host-bit overflow guard (unreachable once schema validation enforces a minimum CIDR size — for
  // IPv4 hostBits is at most 32, reached only at /0; kept as Go's safety net).
  if (hostBits >= 32) {
    throw new CompileError(
      CompileCode.OverlayPoolExhausted,
      { cidr },
      `CIDR ${cidr} has too many host bits to enumerate (must be IPv4 with prefix >= /8)`,
    );
  }

  // totalHosts = 2^hostBits as an unsigned value. Use Math.pow (not <<) so hostBits up to 31 does not
  // hit the signed-32 shift footgun; the values are within Number's exact-integer range (<= 2^31).
  const totalHosts = Math.pow(2, hostBits);

  // Skip the network address (host 1 is the first usable) and the broadcast address (the last).
  let startHost = 1;
  let endHost = totalHosts - 1; // exclude the broadcast address

  if (hostBits <= 1) {
    // For a /31 (point-to-point), both addresses are usable.
    startHost = 0;
    endHost = totalHosts;
  }

  // Scan-budget cap (S1, plan-8): reject an over-large span up front rather than running a
  // multi-million-iteration scan. Checked BEFORE the loop so the rejection is immediate.
  if (endHost - startHost > maxOverlayScanBudget) {
    throw new CompileError(CompileCode.OverlayScanBudgetExceeded, {
      cidr,
      budget: String(maxOverlayScanBudget),
    });
  }

  const networkIP = ipNet.network >>> 0;

  for (let h = startHost; h < endHost; h++) {
    // candidate = networkIP + h as a uint32 (coerced; networkIP is the masked network address and h is
    // within the host span, so the sum never overflows for a valid IPv4 CIDR, but coerce defensively).
    const candidateUint = (networkIP + h) >>> 0;
    const candidateIP = uint32ToIPv4(candidateUint);

    // Skip addresses already in use.
    if (usedIPs.has(candidateIP)) {
      continue;
    }

    // Skip addresses reserved as single IPs (canonical key == canonical candidate; candidateIP is
    // already the canonical dotted-quad form).
    if (reservedSingleIPs.has(candidateIP)) {
      continue;
    }

    // Skip addresses that fall inside any reserved network.
    let reserved = false;
    for (const rNet of reservedNets) {
      if (rNet !== null && contains(rNet, candidateIP)) {
        reserved = true;
        break;
      }
    }
    if (reserved) {
      continue;
    }

    return candidateIP;
  }

  throw new CompileError(CompileCode.OverlayPoolExhausted, { cidr });
}

// allocateIPs assigns an OverlayIP to every node that lacks one, drawing sequentially from the node's
// domain CIDR (skipping reserved ranges and already-used addresses). Nodes that already hold a valid
// in-CIDR address are left untouched. Mirrors allocator/ip.go:50-116 (IPAllocator.AllocateIPs):
//   - work on a COPY of the nodes (the input topology is never mutated);
//   - clear overlay IPs that fall outside their domain's CIDR (skip empty / unknown-domain /
//     unparseable-CIDR; clear when the node IP is unparseable OR not contained);
//   - seed the used-IP set from the surviving overlay IPs (raw strings, as Go keys the map);
//   - fill every still-empty node IN NODE-SLICE ORDER via allocateFromCIDR (an unknown domain on a
//     still-empty node → CodeNodeUnknownDomain).
//
// The TS port carries no ctx (it is pure + synchronous); the budget cap still rejects an over-large
// CIDR identically, and there is no cancellation path to mirror.
export function allocateIPs(topo: Topology): Node[] {
  // Index domains by ID for quick lookup.
  const domainMap = new Map<string, Domain>();
  for (const d of topo.domains) {
    domainMap.set(d.id, d);
  }

  // Work on a copy of the nodes so the input topology is not mutated. Each node is shallow-cloned so
  // writing overlay_ip below does not alias the caller's objects.
  const result: Node[] = topo.nodes.map((n) => ({ ...n }));

  // Clear overlay IPs that fall outside their domain's CIDR (e.g. the user changed the domain CIDR
  // after a previous compile). Mirrors the Go loop's skip-then-clear ordering precisely.
  for (const node of result) {
    if (!node.overlay_ip) {
      continue;
    }
    const domain = domainMap.get(node.domain_id);
    if (domain === undefined) {
      continue;
    }
    const ipNet = parseCIDR(domain.cidr);
    if (ipNet === null) {
      continue;
    }
    // Go: net.ParseIP(node.OverlayIP); nil OR !Contains → clear. parseIPFamily===null mirrors a nil
    // ParseIP (any family Go accepts), and contains() mirrors ipNet.Contains for the IPv4 pool.
    if (parseIPFamily(node.overlay_ip) === null || !contains(ipNet, node.overlay_ip)) {
      node.overlay_ip = ''; // force re-allocation
    }
  }

  // Record already-used IPs (after clearing stale ones). Go keys this map by the RAW OverlayIP string;
  // candidate lookups use the canonical dotted-quad, so a previously-allocated canonical IP matches.
  const usedIPs = new Set<string>();
  for (const node of result) {
    if (node.overlay_ip) {
      usedIPs.add(node.overlay_ip);
    }
  }

  // Allocate an IP for every node that still lacks one, in node-slice order.
  for (const node of result) {
    if (node.overlay_ip) {
      continue; // already has an IP; keep it
    }

    const domain = domainMap.get(node.domain_id);
    if (domain === undefined) {
      // Defensive net for the direct allocator path: the semantic validator (CodeNodeDomainRefMissing)
      // catches an unknown-domain reference before the compiler reaches allocation, so this coded
      // branch only fires for a direct allocateIPs caller. Mirrors apierr CodeNodeUnknownDomain.
      throw new CompileError(CompileCode.NodeUnknownDomain, {
        node: node.name,
        domain: node.domain_id,
      });
    }

    const ip = allocateFromCIDR(domain.cidr, domain.reserved_ranges ?? [], usedIPs);
    node.overlay_ip = ip;
    usedIPs.add(ip);
  }

  return result;
}
