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

	// Pass 3: IP 
	allocatedNodes, err := c.ipAllocator.AllocateIPs(topo)
	if err != nil {
		return nil, fmt.Errorf("IP : %w", err)
	}

	// 
	compiledTopo := &model.Topology{
		Project:       topo.Project,
		Domains:       topo.Domains,
		Nodes:         allocatedNodes,
		Edges:         topo.Edges,
		RoutePolicies: topo.RoutePolicies,
	}

	// Pass 3 :  capabilities
	for i := range compiledTopo.Nodes {
		compiledTopo.Nodes[i].Capabilities = InferCapabilitiesFromRole(&compiledTopo.Nodes[i])
	}

	// Pass 3 :  Peer 
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
