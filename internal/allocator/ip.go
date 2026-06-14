package allocator

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
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

	// 结果副本
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

	// 记录已用 IP（after clearing stale ones）
	usedIPs := make(map[string]bool)
	for _, node := range result {
		if node.OverlayIP != "" {
			usedIPs[node.OverlayIP] = true
		}
	}

	//  IP
	for i := range result {
		if result[i].OverlayIP != "" {
			continue //  IP，
		}

		domain, ok := domainMap[result[i].DomainID]
		if !ok {
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

// allocateFromCIDR  CIDR  IP
func (a *IPAllocator) allocateFromCIDR(cidr string, reservedRanges []string, usedIPs map[string]bool) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", apierr.New(apierr.CodeOverlayCIDRInvalid).With("cidr", cidr).Wrap(err)
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

	//  /32  /128，无可分配主机位
	if hostBits == 0 {
		return "", fmt.Errorf("CIDR %s has no assignable host addresses (prefix too long)", cidr)
	}

	// 主机位溢出防御：uint32(1) << 32 会溢出，导致退化的循环边界。
	// 在 schema 校验加入 CIDR 大小下限后此分支不可达，保留为安全网。
	if hostBits >= 32 {
		return "", fmt.Errorf("CIDR %s has too many host bits to enumerate (must be IPv4 with prefix >= /8)", cidr)
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

	networkIP, err := ipToUint32(ipNet.IP)
	if err != nil {
		return "", fmt.Errorf("CIDR %s is not a valid IPv4 network address: %w", cidr, err)
	}

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

	return "", apierr.New(apierr.CodeOverlayPoolExhausted).With("cidr", cidr)
}

// ipToUint32 将 IPv4 地址转换为 uint32。
// 深度防御：对 nil 或无法表示为 4 字节 IPv4 的地址返回错误，
// 而不是越界切片导致 panic（IPv6 CIDR 不应到达 IPv4-only 的分配器）。
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
