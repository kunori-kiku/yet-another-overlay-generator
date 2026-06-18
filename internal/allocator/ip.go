package allocator

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// maxOverlayScanBudget caps the number of host candidates the allocator will enumerate for a
// SINGLE node within one domain CIDR. The allocator finds a free address by walking the CIDR's
// host range linearly; a maliciously (or accidentally) large prefix — a /8 is ~16.7M candidates,
// already valid per the validator's /8 lower bound — would tie up a request goroutine in a
// multi-million-iteration scan per node. This ceiling is the allocator-side analogue of the
// schema-layer nodes/edges count bound (validator/schema.go): generous enough that no realistic
// overlay ever trips it (a /16 is 65k candidates, comfortably under), strict enough that "a
// /8 × a node" is rejected fast with a coded error rather than running to completion. It bounds
// the PER-NODE scan; because the allocator runs this scan once per node, the total work is
// bounded by nodes × maxOverlayScanBudget. OWNER-FACING (plan-8 owner flag R6): a generous
// documented value whose only job is to stop "millions".
const maxOverlayScanBudget = 1 << 20 // 1,048,576 candidates per node — well above a /12 (~1M)

// ctxCheckInterval is how often the linear scan polls the request context for cancellation. A
// per-iteration check would add measurable overhead to the hot path; checking every N candidates
// keeps the abort latency bounded (at most N iterations after cancel) while leaving the common
// small-CIDR case effectively free.
const ctxCheckInterval = 4096

// IPAllocator assigns overlay IP addresses to topology nodes.
type IPAllocator struct{}

// NewIPAllocator constructs an IPAllocator.
func NewIPAllocator() *IPAllocator {
	return &IPAllocator{}
}

// AllocateIPs assigns an OverlayIP to every node that lacks one, drawing
// sequentially from the node's domain CIDR (skipping reserved ranges and
// already-used addresses). Nodes that already hold a valid in-CIDR address
// are left untouched.
//
// ctx bounds the work: a very large domain CIDR is rejected up front via the
// per-node scan budget (CodeOverlayScanBudgetExceeded), and a long-running scan
// is abortable on request cancellation (the loop polls ctx.Err() periodically).
// The request context flows in from the HTTP boundary (handler.go) through
// compiler.Compile; the air-gap CLI / direct callers pass context.Background().
func (a *IPAllocator) AllocateIPs(ctx context.Context, topo *model.Topology) ([]model.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Index domains by ID for quick lookup.
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	// Work on a copy of the nodes so the input topology is not mutated.
	result := make([]model.Node, len(topo.Nodes))
	copy(result, topo.Nodes)

	// Clear overlay IPs that fall outside their domain's CIDR
	// (e.g., user changed the domain CIDR after a previous compile)
	for i := range result {
		if result[i].OverlayIP == "" {
			continue
		}
		domain, ok := domainMap[result[i].DomainID]
		if !ok {
			continue
		}
		_, ipNet, err := net.ParseCIDR(domain.CIDR)
		if err != nil {
			continue
		}
		nodeIP := net.ParseIP(result[i].OverlayIP)
		if nodeIP == nil || !ipNet.Contains(nodeIP) {
			result[i].OverlayIP = "" // force re-allocation
		}
	}

	// Record already-used IPs (after clearing stale ones).
	usedIPs := make(map[string]bool)
	for _, node := range result {
		if node.OverlayIP != "" {
			usedIPs[node.OverlayIP] = true
		}
	}

	// Allocate an IP for every node that still lacks one.
	for i := range result {
		if result[i].OverlayIP != "" {
			continue // already has an IP; keep it
		}

		domain, ok := domainMap[result[i].DomainID]
		if !ok {
			// Defensive: via the HTTP compile relays the semantic validator (CodeNodeDomainRefMissing)
			// catches an unknown-domain reference in Compile Pass 2 before AllocateIPs (Pass 3) runs,
			// so this coded branch is the safety net for the direct allocator.AllocateIPs path.
			return nil, apierr.New(apierr.CodeNodeUnknownDomain).With("node", result[i].Name).With("domain", result[i].DomainID)
		}

		ip, err := a.allocateFromCIDR(ctx, domain.CIDR, domain.ReservedRanges, usedIPs)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate an IP for node %s: %w", result[i].Name, err)
		}

		result[i].OverlayIP = ip
		usedIPs[ip] = true
	}

	return result, nil
}

// allocateFromCIDR returns the first free host IP within cidr, skipping the
// supplied reserved ranges and already-used addresses. The scan is bounded by
// the per-node scan budget (rejected up front for an over-large CIDR) and is
// abortable via ctx.
func (a *IPAllocator) allocateFromCIDR(ctx context.Context, cidr string, reservedRanges []string, usedIPs map[string]bool) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", apierr.New(apierr.CodeOverlayCIDRInvalid).With("cidr", cidr).Wrap(err)
	}

	// Parse the reserved ranges into networks and single-IP reservations.
	reservedNets := make([]*net.IPNet, 0)
	reservedSingleIPs := make(map[string]bool)
	for _, rr := range reservedRanges {
		_, rNet, err := net.ParseCIDR(rr)
		if err != nil {
			// Not a CIDR; try parsing it as a single IP.
			ip := net.ParseIP(rr)
			if ip != nil {
				reservedSingleIPs[ip.String()] = true
			}
			continue
		}
		reservedNets = append(reservedNets, rNet)
	}

	// Determine the host-bit count from the CIDR mask.
	ones, bits := ipNet.Mask.Size()
	hostBits := bits - ones

	// A /32 (or /128) has no assignable host addresses.
	if hostBits == 0 {
		return "", fmt.Errorf("CIDR %s has no assignable host addresses (prefix too long)", cidr)
	}

	// Host-bit overflow guard: uint32(1) << 32 overflows and yields a degenerate
	// loop bound. This branch is unreachable once schema validation enforces a
	// minimum CIDR size, but is kept as a safety net.
	if hostBits >= 32 {
		return "", fmt.Errorf("CIDR %s has too many host bits to enumerate (must be IPv4 with prefix >= /8)", cidr)
	}

	totalHosts := uint32(1) << uint(hostBits)

	// Skip the network address (host 1 is the first usable address) and the
	// broadcast address (the last address; e.g. /30 has two usable hosts).
	startHost := uint32(1)
	endHost := totalHosts - 1 // exclude the broadcast address

	if hostBits <= 1 {
		// For a /31 (point-to-point), both addresses are usable.
		startHost = 0
		endHost = totalHosts
	}

	// Scan-budget cap (S1, plan-8): the loop below walks [startHost, endHost) linearly to find a
	// free address. A very large prefix makes that span enormous (a /8 is ~16.7M candidates), so a
	// big CIDR combined with many nodes would tie up the request goroutine. Reject up front when
	// the per-node scan span exceeds the budget — fast and coded, never a multi-million-iteration
	// run. This is checked BEFORE the loop so the rejection is immediate. (The hostBits>=32 guard
	// above already excludes the overflow case; this caps the merely-very-large valid range.)
	if endHost-startHost > maxOverlayScanBudget {
		return "", apierr.New(apierr.CodeOverlayScanBudgetExceeded).
			With("cidr", cidr).With("budget", fmt.Sprintf("%d", maxOverlayScanBudget))
	}

	networkIP, err := ipToUint32(ipNet.IP)
	if err != nil {
		return "", fmt.Errorf("CIDR %s is not a valid IPv4 network address: %w", cidr, err)
	}

	for h := startHost; h < endHost; h++ {
		// Honor request cancellation periodically so a large in-budget scan is abortable when the
		// caller's context is cancelled (S1, plan-8). Checked every ctxCheckInterval candidates to
		// keep the abort latency bounded without paying ctx.Err() on every iteration of the hot path.
		if (h-startHost)%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}

		candidateUint := networkIP + h
		candidateIP := uint32ToIP(candidateUint).String()

		// Skip addresses already in use.
		if usedIPs[candidateIP] {
			continue
		}

		// Skip addresses reserved as single IPs.
		if reservedSingleIPs[candidateIP] {
			continue
		}

		// Skip addresses that fall inside any reserved network.
		candidate := net.ParseIP(candidateIP)
		reserved := false
		for _, rNet := range reservedNets {
			if rNet.Contains(candidate) {
				reserved = true
				break
			}
		}
		if reserved {
			continue
		}

		return candidateIP, nil
	}

	return "", apierr.New(apierr.CodeOverlayPoolExhausted).With("cidr", cidr)
}

// ipToUint32 converts an IPv4 address to a uint32.
// Defense in depth: it returns an error for a nil address or one that cannot be
// represented as a 4-byte IPv4 address, rather than panicking on an out-of-range
// slice (an IPv6 CIDR should never reach this IPv4-only allocator).
func ipToUint32(ip net.IP) (uint32, error) {
	v4 := ip.To4()
	if v4 == nil || len(v4) != 4 {
		return 0, fmt.Errorf("address %q is not a valid IPv4 address", ip.String())
	}
	return binary.BigEndian.Uint32(v4), nil
}

func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}
