package allocator

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

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
func (a *IPAllocator) AllocateIPs(topo *model.Topology) ([]model.Node, error) {
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

		ip, err := a.allocateFromCIDR(domain.CIDR, domain.ReservedRanges, usedIPs)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate an IP for node %s: %w", result[i].Name, err)
		}

		result[i].OverlayIP = ip
		usedIPs[ip] = true
	}

	return result, nil
}

// allocateFromCIDR returns the first free host IP within cidr, skipping the
// supplied reserved ranges and already-used addresses.
func (a *IPAllocator) allocateFromCIDR(cidr string, reservedRanges []string, usedIPs map[string]bool) (string, error) {
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

	networkIP, err := ipToUint32(ipNet.IP)
	if err != nil {
		return "", fmt.Errorf("CIDR %s is not a valid IPv4 network address: %w", cidr, err)
	}

	for h := startHost; h < endHost; h++ {
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
