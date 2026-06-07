package compiler

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocator"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// CompileResult 
type CompileResult struct {
	// （ IP）
	Topology *model.Topology

	//  Peer 
	PeerMap map[string][]PeerInfo

	//  WireGuard 
	WireGuardConfigs map[string]string

	//  Babel 
	BabelConfigs map[string]string

	//  sysctl 
	SysctlConfigs map[string]string

	//
	InstallScripts map[string]string

	// 自动部署脚本
	DeployScripts map[string]string

	// Client 节点的 wg0 配置信息
	ClientConfigs map[string]*ClientPeerInfo

	// 非致命告警（schema + semantic 两个阶段产生的 warning），
	// 供调用方（API/CLI）在编译成功后向用户展示，避免绿色编译掩盖
	// NAT/无 endpoint 边等"哑链路"问题（审计阻断项 UX-1）。
	Warnings []validator.ValidationError

	//
	Manifest CompileManifest
}

// CompileManifest 
type CompileManifest struct {
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Version     string    `json:"version"`
	CompiledAt  time.Time `json:"compiled_at"`
	NodeCount   int       `json:"node_count"`
	Checksum    string    `json:"checksum"`
}

// Compiler 
type Compiler struct {
	ipAllocator *allocator.IPAllocator
}

// NewCompiler 
func NewCompiler() *Compiler {
	return &Compiler{
		ipAllocator: allocator.NewIPAllocator(),
	}
}

// Compile 
func (c *Compiler) Compile(topo *model.Topology, keys map[string]KeyPair) (*CompileResult, error) {
	// Pass 1: Schema 
	schemaResult := validator.ValidateSchema(topo)
	if !schemaResult.IsValid() {
		return nil, fmt.Errorf("schema : %v", schemaResult.Errors)
	}

	// Pass 2:
	semanticResult := validator.ValidateSemantic(topo)
	if !semanticResult.IsValid() {
		return nil, fmt.Errorf(": %v", semanticResult.Errors)
	}

	// 汇总两个验证阶段产生的非致命告警，随编译结果一并返回，
	// 确保每个调用方（API 与 CLI）都能拿到这些告警。
	warnings := make([]validator.ValidationError, 0, len(schemaResult.Warnings)+len(semanticResult.Warnings))
	warnings = append(warnings, schemaResult.Warnings...)
	warnings = append(warnings, semanticResult.Warnings...)

	// Pass 3: IP 
	allocatedNodes, err := c.ipAllocator.AllocateIPs(topo)
	if err != nil {
		return nil, fmt.Errorf("IP : %w", err)
	}

	// 复制 edges 以避免修改输入
	compiledEdges := make([]model.Edge, len(topo.Edges))
	copy(compiledEdges, topo.Edges)

	compiledTopo := &model.Topology{
		Project:       topo.Project,
		Domains:       topo.Domains,
		Nodes:         allocatedNodes,
		Edges:         compiledEdges,
		RoutePolicies: topo.RoutePolicies,
	}

	// Pass 3 :  capabilities
	for i := range compiledTopo.Nodes {
		compiledTopo.Nodes[i].Capabilities = InferCapabilitiesFromRole(&compiledTopo.Nodes[i])
	}

	// Pass 3 :  Peer
	peerMap, pairAllocations, err := DerivePeers(compiledTopo, keys)
	if err != nil {
		return nil, fmt.Errorf("推导 WireGuard peer 配置失败: %w", err)
	}

	// Client 配置
	clientConfigs := DeriveClientConfigs(compiledTopo, keys, pairAllocations)

	// 将该 edge from 侧实际拨号的端口写入 CompiledPort（只读输出，不修改用户输入的 EndpointPort）。
	// CompiledPort 必须等于渲染出的 Endpoint 中携带的端口：
	//   - EndpointPort > 0（运营商显式 NAT/端口转发覆盖）时，逐字反映该覆盖值；
	//   - 否则使用对端接口的已分配监听端口（编译器自动分配）。
	for i := range compiledTopo.Edges {
		edge := &compiledTopo.Edges[i]
		if !edge.IsEnabled || edge.EndpointHost == "" {
			continue
		}
		if edge.EndpointPort > 0 {
			edge.CompiledPort = edge.EndpointPort
			continue
		}
		// 查找该 edge 对应的 pairAllocation，提取对端（toNode）的已分配监听端口
		peerKey := edge.FromNodeID + "->" + edge.ToNodeID
		if alloc, ok := pairAllocations[peerKey]; ok {
			if alloc.fromNodeID == edge.FromNodeID {
				edge.CompiledPort = alloc.toPort
			} else {
				edge.CompiledPort = alloc.fromPort
			}
		}
	}

	result := &CompileResult{
		Topology:         compiledTopo,
		PeerMap:          peerMap,
		WireGuardConfigs: make(map[string]string),
		BabelConfigs:     make(map[string]string),
		SysctlConfigs:    make(map[string]string),
		InstallScripts:   make(map[string]string),
		DeployScripts:    make(map[string]string),
		ClientConfigs:    clientConfigs,
		Warnings:         warnings,
		Manifest: CompileManifest{
			ProjectID:   topo.Project.ID,
			ProjectName: topo.Project.Name,
			Version:     topo.Project.Version,
			CompiledAt:  time.Now(),
			NodeCount:   len(allocatedNodes),
			Checksum:    computeChecksum(compiledTopo),
		},
	}

	return result, nil
}

func computeChecksum(topo *model.Topology) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%v", topo)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}
