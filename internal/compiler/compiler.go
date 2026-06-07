package compiler

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocator"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/linkid"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// AllocationSchemaVersion 是粘性 pin 分配方案的 schema 版本号（不变量 I10）。
// 编译器把它写回到编译后拓扑的 AllocSchemaVersion 字段，使未来对 pin 格式的改动
// 能够检测并迁移旧拓扑，而不是把旧格式当成新格式静默误读。
// 详见 docs/spec/compiler/allocation-stability.md（不变量 I10）。
const AllocationSchemaVersion = 1

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
		// 标记本次编译使用的分配方案版本（不变量 I10），使未来对 pin 格式的改动可检测并迁移。
		AllocSchemaVersion: AllocationSchemaVersion,
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

	// 把每条 enabled edge 分配到的资源写回到其 pin 字段（六个 pinned_*），并按本 edge 的
	// from/to 方向定向；同时写回只读的 CompiledPort 供 UI 显示。pin 经前端持久化往返后，
	// 下次编译被 reserve-then-gap-fill 逐字沿用，从而让 superset 拓扑对既有 edge 重现
	// 逐字节相同的分配值（不变量 I1/I8）。详见 docs/spec/compiler/allocation-stability.md。
	//
	// CompiledPort 必须等于渲染出的 Endpoint 中携带的端口：
	//   - EndpointPort > 0（运营商显式 NAT/端口转发覆盖）时，逐字反映该覆盖值；
	//   - 否则使用对端接口的已分配监听端口（编译器自动分配）。
	for i := range compiledTopo.Edges {
		edge := &compiledTopo.Edges[i]
		if !edge.IsEnabled {
			continue
		}

		// 查找该 edge 对应的 pairAllocation，键为 linkid.LinkKey(edge)（规范 I3：
		// per-peer 分配身份即 linkKey）。primary class 的同对节点全部 edge 共享统一链路的
		// alloc（定向按本 edge）；每条 backup edge 取它自己链路的 alloc。
		alloc, ok := pairAllocations[linkid.LinkKey(edge)]
		if !ok {
			continue
		}

		// 按本 edge 的 from/to 方向定向 pin：alloc.fromNodeID 是分配 struct 的「规范 from」，
		// 若与本 edge 的 FromNodeID 一致则正向取值，否则镜像。
		isForward := alloc.fromNodeID == edge.FromNodeID
		if isForward {
			edge.PinnedFromPort = alloc.fromPort
			edge.PinnedToPort = alloc.toPort
			edge.PinnedFromTransitIP = alloc.localTransit
			edge.PinnedToTransitIP = alloc.remoteTransit
			edge.PinnedFromLinkLocal = alloc.localLL
			edge.PinnedToLinkLocal = alloc.remoteLL
		} else {
			edge.PinnedFromPort = alloc.toPort
			edge.PinnedToPort = alloc.fromPort
			edge.PinnedFromTransitIP = alloc.remoteTransit
			edge.PinnedToTransitIP = alloc.localTransit
			edge.PinnedFromLinkLocal = alloc.remoteLL
			edge.PinnedToLinkLocal = alloc.localLL
		}

		// CompiledPort：仅对带 endpoint_host 的 edge 写回（与渲染出的 Endpoint 端口一致）。
		if edge.EndpointHost == "" {
			continue
		}
		if edge.EndpointPort > 0 {
			edge.CompiledPort = edge.EndpointPort
			continue
		}
		// 自动分配：对端（toNode）接口的已分配监听端口。
		if isForward {
			edge.CompiledPort = alloc.toPort
		} else {
			edge.CompiledPort = alloc.fromPort
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
