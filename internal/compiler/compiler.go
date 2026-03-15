package compiler

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocator"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// CompileResult 编译结果
type CompileResult struct {
	// 编译后的拓扑（含分配的 IP）
	Topology *model.Topology

	// 每节点的 Peer 关系
	PeerMap map[string][]PeerInfo

	// 每节点的 WireGuard 配置
	WireGuardConfigs map[string]string

	// 每节点的 Babel 配置
	BabelConfigs map[string]string

	// 每节点的 sysctl 配置
	SysctlConfigs map[string]string

	// 每节点的安装脚本
	InstallScripts map[string]string

	// 编译元信息
	Manifest CompileManifest
}

// CompileManifest 编译清单
type CompileManifest struct {
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Version     string    `json:"version"`
	CompiledAt  time.Time `json:"compiled_at"`
	NodeCount   int       `json:"node_count"`
	Checksum    string    `json:"checksum"`
}

// Compiler 编译器
type Compiler struct {
	ipAllocator *allocator.IPAllocator
}

// NewCompiler 创建新的编译器
func NewCompiler() *Compiler {
	return &Compiler{
		ipAllocator: allocator.NewIPAllocator(),
	}
}

// Compile 执行完整编译流程
func (c *Compiler) Compile(topo *model.Topology, keys map[string]KeyPair) (*CompileResult, error) {
	// Pass 1: Schema 校验
	schemaResult := validator.ValidateSchema(topo)
	if !schemaResult.IsValid() {
		return nil, fmt.Errorf("schema 校验失败: %v", schemaResult.Errors)
	}

	// Pass 2: 语义校验
	semanticResult := validator.ValidateSemantic(topo)
	if !semanticResult.IsValid() {
		return nil, fmt.Errorf("语义校验失败: %v", semanticResult.Errors)
	}

	// Pass 3: IP 分配
	allocatedNodes, err := c.ipAllocator.AllocateIPs(topo)
	if err != nil {
		return nil, fmt.Errorf("IP 分配失败: %w", err)
	}

	// 构建编译后的拓扑副本
	compiledTopo := &model.Topology{
		Project:       topo.Project,
		Domains:       topo.Domains,
		Nodes:         allocatedNodes,
		Edges:         topo.Edges,
		RoutePolicies: topo.RoutePolicies,
	}

	// Pass 3 续: 根据角色推导 capabilities
	for i := range compiledTopo.Nodes {
		compiledTopo.Nodes[i].Capabilities = InferCapabilitiesFromRole(&compiledTopo.Nodes[i])
	}

	// Pass 3 续: 推导 Peer 关系
	peerMap := DerivePeers(compiledTopo, keys)

	result := &CompileResult{
		Topology:         compiledTopo,
		PeerMap:          peerMap,
		WireGuardConfigs: make(map[string]string),
		BabelConfigs:     make(map[string]string),
		SysctlConfigs:    make(map[string]string),
		InstallScripts:   make(map[string]string),
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
