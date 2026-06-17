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
// 规范来源是 model.CurrentAllocSchemaVersion（validator 据此 fail-closed 拒绝来自更新版本
// 的拓扑，且 compiler→validator 的依赖方向使 validator 无法反向引用 compiler 常量）。
// 详见 docs/spec/compiler/allocation-stability.md（不变量 I10）。
const AllocationSchemaVersion = model.CurrentAllocSchemaVersion

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

	// ArtifactsJSON holds the per-node, controller-signed artifacts.json content (nodeID ->
	// JSON), carrying the mimic GitHub-.deb pins (and, from plan-9, the agent self-update
	// block). render.All populates it from FetchSettings; it is EMPTY when no catalog is
	// configured, so export omits the file and the air-gap bundle stays byte-identical (D4).
	// It is a signed bundleFiles member — the install.sh reads its pins after integrity verify.
	ArtifactsJSON map[string]string

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
	// reserved 携带「子图之外的 edge」所占的分配资源（端口 / transit IP / link-local），
	// 供子图编译时让 gap-fill 避让，避免跨子图 pin 碰撞。nil（默认）= 全量编译，行为不变。
	reserved *ReservedAllocations
}

// NewCompiler
func NewCompiler() *Compiler {
	return &Compiler{
		ipAllocator: allocator.NewIPAllocator(),
	}
}

// WithReserved 设定一组「子图之外的 edge」预留资源，返回同一个 *Compiler 以便链式调用
// （compiler.NewCompiler().WithReserved(r).Compile(...)）。仅 controller 子图编译需要；
// 全量编译（air-gap CLI / API）不调用它，reserved 保持 nil。
func (c *Compiler) WithReserved(r *ReservedAllocations) *Compiler {
	c.reserved = r
	return c
}

// Compile
func (c *Compiler) Compile(topo *model.Topology, keys map[string]KeyPair) (*CompileResult, error) {
	// Pass 1: Schema
	schemaResult := validator.ValidateSchema(topo)
	if !schemaResult.IsValid() {
		return nil, fmt.Errorf("topology failed schema validation: %v", schemaResult.Errors)
	}

	// Pass 2:
	semanticResult := validator.ValidateSemantic(topo)
	if !semanticResult.IsValid() {
		return nil, fmt.Errorf("topology failed semantic validation: %v", semanticResult.Errors)
	}

	// 汇总两个验证阶段产生的非致命告警，随编译结果一并返回，
	// 确保每个调用方（API 与 CLI）都能拿到这些告警。
	warnings := make([]validator.ValidationError, 0, len(schemaResult.Warnings)+len(semanticResult.Warnings))
	warnings = append(warnings, schemaResult.Warnings...)
	warnings = append(warnings, semanticResult.Warnings...)

	// Pass 3: IP
	allocatedNodes, err := c.ipAllocator.AllocateIPs(topo)
	if err != nil {
		return nil, fmt.Errorf("IP allocation failed: %w", err)
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
	// 子图编译时 c.reserved 非 nil，让 gap-fill 避开子图外 edge 占用的资源（跨子图碰撞根因修复）；
	// 全量编译 c.reserved==nil，derivePeers 退化为原 DerivePeers 行为。
	peerMap, pairAllocations, err := derivePeers(compiledTopo, keys, c.reserved)
	if err != nil {
		return nil, fmt.Errorf("deriving WireGuard peer configuration failed: %w", err)
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
		ArtifactsJSON:    make(map[string]string),
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
