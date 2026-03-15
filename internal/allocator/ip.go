package allocator

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// IPAllocator IP 地址分配器
type IPAllocator struct{}

// NewIPAllocator 创建新的 IP 分配器
func NewIPAllocator() *IPAllocator {
	return &IPAllocator{}
}

// AllocateIPs 为拓扑中所有缺少 OverlayIP 的节点分配 IP
// 返回更新后的节点列表（原列表不修改）
func (a *IPAllocator) AllocateIPs(topo *model.Topology) ([]model.Node, error) {
	// 按 Domain 分组处理
	domainMap := make(map[string]*model.Domain)
	for i := range topo.Domains {
		domainMap[topo.Domains[i].ID] = &topo.Domains[i]
	}

	// 收集已占用的 IP（手动分配的）
	usedIPs := make(map[string]bool)
	for _, node := range topo.Nodes {
		if node.OverlayIP != "" {
			usedIPs[node.OverlayIP] = true
		}
	}

	// 复制节点列表
	result := make([]model.Node, len(topo.Nodes))
	copy(result, topo.Nodes)

	// 为每个需要自动分配的节点分配 IP
	for i := range result {
		if result[i].OverlayIP != "" {
			continue // 已有手动 IP，跳过
		}

		domain, ok := domainMap[result[i].DomainID]
		if !ok {
			return nil, fmt.Errorf("节点 %s 引用的 Domain %s 不存在",
				result[i].Name, result[i].DomainID)
		}

		ip, err := a.allocateFromCIDR(domain.CIDR, domain.ReservedRanges, usedIPs)
		if err != nil {
			return nil, fmt.Errorf("为节点 %s 分配 IP 失败: %w", result[i].Name, err)
		}

		result[i].OverlayIP = ip
		usedIPs[ip] = true
	}

	return result, nil
}

// allocateFromCIDR 从 CIDR 中分配一个未使用的 IP
func (a *IPAllocator) allocateFromCIDR(cidr string, reservedRanges []string, usedIPs map[string]bool) (string, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("无效的 CIDR: %s", cidr)
	}

	// 解析保留区间
	reservedNets := make([]*net.IPNet, 0)
	reservedSingleIPs := make(map[string]bool)
	for _, rr := range reservedRanges {
		_, rNet, err := net.ParseCIDR(rr)
		if err != nil {
			// 尝试解析为单 IP
			ip := net.ParseIP(rr)
			if ip != nil {
				reservedSingleIPs[ip.String()] = true
			}
			continue
		}
		reservedNets = append(reservedNets, rNet)
	}

	// 遍历 CIDR 中的所有可用地址
	ones, bits := ipNet.Mask.Size()
	hostBits := bits - ones

	// 对于 /32 或 /128，没有可分配的地址
	if hostBits == 0 {
		return "", fmt.Errorf("CIDR %s 没有可分配的主机地址", cidr)
	}

	totalHosts := uint32(1) << uint(hostBits)

	// 从第 1 个主机地址开始（跳过网络地址）
	// 到倒数第 2 个地址结束（跳过广播地址，仅对 IPv4 /30 及以上）
	startHost := uint32(1)
	endHost := totalHosts - 1 // 排除广播地址

	if hostBits <= 1 {
		// /31 点对点链路，两个地址都可用
		startHost = 0
		endHost = totalHosts
	}

	networkIP := ipToUint32(ipNet.IP.To4())

	for h := startHost; h < endHost; h++ {
		candidateUint := networkIP + h
		candidateIP := uint32ToIP(candidateUint).String()

		// 检查是否已使用
		if usedIPs[candidateIP] {
			continue
		}

		// 检查是否在保留单 IP 中
		if reservedSingleIPs[candidateIP] {
			continue
		}

		// 检查是否在保留区间中
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

	return "", fmt.Errorf("CIDR %s 中没有可用的 IP 地址", cidr)
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
