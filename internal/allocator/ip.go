package allocator

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// IPAllocator IP 
type IPAllocator struct{}

// NewIPAllocator  IP 
func NewIPAllocator() *IPAllocator {
	return &IPAllocator{}
}

// AllocateIPs  OverlayIP  IP
// （）
func (a *IPAllocator) AllocateIPs(topo *model.Topology) ([]model.Node, error) {
	//  Domain 
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	//  IP（）
	usedIPs := make(map[string]bool)
	for _, node := range topo.Nodes {
		if node.OverlayIP != "" {
			usedIPs[node.OverlayIP] = true
		}
	}

	// 
	result := make([]model.Node, len(topo.Nodes))
	copy(result, topo.Nodes)

	//  IP
	for i := range result {
		if result[i].OverlayIP != "" {
			continue //  IP，
		}

		domain, ok := domainMap[result[i].DomainID]
		if !ok {
			return nil, fmt.Errorf(" %s  Domain %s ",
				result[i].Name, result[i].DomainID)
		}

		ip, err := a.allocateFromCIDR(domain.CIDR, domain.ReservedRanges, usedIPs)
		if err != nil {
			return nil, fmt.Errorf(" %s  IP : %w", result[i].Name, err)
		}

		result[i].OverlayIP = ip
		usedIPs[ip] = true
	}

	return result, nil
}

// allocateFromCIDR  CIDR  IP
func (a *IPAllocator) allocateFromCIDR(cidr string, reservedRanges []string, usedIPs map[string]bool) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf(" CIDR: %s", cidr)
	}

	// 
	reservedNets := make([]*net.IPNet, 0)
	reservedSingleIPs := make(map[string]bool)
	for _, rr := range reservedRanges {
		_, rNet, err := net.ParseCIDR(rr)
		if err != nil {
			//  IP
			ip := net.ParseIP(rr)
			if ip != nil {
				reservedSingleIPs[ip.String()] = true
			}
			continue
		}
		reservedNets = append(reservedNets, rNet)
	}

	//  CIDR 
	ones, bits := ipNet.Mask.Size()
	hostBits := bits - ones

	//  /32  /128，
	if hostBits == 0 {
		return "", fmt.Errorf("CIDR %s ", cidr)
	}

	totalHosts := uint32(1) << uint(hostBits)

	//  1 （）
	//  2 （， IPv4 /30 ）
	startHost := uint32(1)
	endHost := totalHosts - 1 // 

	if hostBits <= 1 {
		// /31 ，
		startHost = 0
		endHost = totalHosts
	}

	networkIP := ipToUint32(ipNet.IP.To4())

	for h := startHost; h < endHost; h++ {
		candidateUint := networkIP + h
		candidateIP := uint32ToIP(candidateUint).String()

		// 
		if usedIPs[candidateIP] {
			continue
		}

		//  IP 
		if reservedSingleIPs[candidateIP] {
			continue
		}

		// 
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

	return "", fmt.Errorf("CIDR %s  IP ", cidr)
}

func ipToUint32(ip net.IP) uint32 {
	if len(ip) == 4 {
		return binary.BigEndian.Uint32(ip)
	}
	return binary.BigEndian.Uint32(ip[12:16])
}

func uint32ToIP(n uint32) net.IP {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, n)
	return ip
}
