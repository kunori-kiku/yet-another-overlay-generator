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

// AllocationSchemaVersion is the schema version of the sticky-pin allocation scheme (invariant I10).
// The compiler writes it back to the compiled topology's AllocSchemaVersion field so that future
// changes to the pin format can be detected and old topologies migrated, instead of silently
// misreading an old format as the new one.
// The canonical source is model.CurrentAllocSchemaVersion (the validator uses it to fail-closed and
// reject topologies from newer versions; the compiler->validator dependency direction means the
// validator cannot reference compiler constants in reverse).
// See docs/spec/compiler/allocation-stability.md (invariant I10).
const AllocationSchemaVersion = model.CurrentAllocSchemaVersion

// CompileResult holds the output of a full compilation: the resolved topology, per-node peer
// maps, all rendered configs and scripts, and the manifest.
type CompileResult struct {
	// Topology is the compiled topology (with allocated IPs).
	Topology *model.Topology

	// PeerMap maps each node ID to its derived peer entries.
	PeerMap map[string][]PeerInfo

	// WireGuardConfigs holds the rendered WireGuard config per node.
	WireGuardConfigs map[string]string

	// BabelConfigs holds the rendered Babel config per node.
	BabelConfigs map[string]string

	// SysctlConfigs holds the rendered sysctl settings per node.
	SysctlConfigs map[string]string

	// InstallScripts holds the rendered install script per node.
	InstallScripts map[string]string

	// ArtifactsJSON holds the per-node, controller-signed artifacts.json content (nodeID ->
	// JSON), carrying the mimic GitHub-.deb pins (and, from plan-9, the agent self-update
	// block). render.All populates it from FetchSettings; it is EMPTY when no catalog is
	// configured, so export omits the file and the air-gap bundle stays byte-identical (D4).
	// It is a signed bundleFiles member — the install.sh reads its pins after integrity verify.
	ArtifactsJSON map[string]string

	// DeployScripts holds the auto-generated deploy script per node.
	DeployScripts map[string]string

	// ClientConfigs holds the wg0 config info for client-role nodes.
	ClientConfigs map[string]*ClientPeerInfo

	// Warnings carries the non-fatal warnings produced by the schema and semantic stages,
	// so callers (API/CLI) can surface them to the user after a successful compile. This
	// prevents a green compile from masking "dumb link" issues such as NAT or edges without
	// an endpoint (audit blocker UX-1).
	Warnings []validator.ValidationError

	// Manifest is the compile manifest summarizing this build.
	Manifest CompileManifest
}

// CompileManifest summarizes a compile: project identity, version, timestamp, node count, and checksum.
type CompileManifest struct {
	ProjectID   string    `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Version     string    `json:"version"`
	CompiledAt  time.Time `json:"compiled_at"`
	NodeCount   int       `json:"node_count"`
	Checksum    string    `json:"checksum"`
}

// Compiler holds the per-compile state: the IP allocator and, for subgraph compiles, the
// reserved out-of-subgraph allocations.
type Compiler struct {
	ipAllocator *allocator.IPAllocator
	// reserved carries the allocation resources (ports / transit IPs / link-locals) occupied
	// by edges outside the subgraph, so subgraph compiles let gap-fill avoid them and prevent
	// cross-subgraph pin collisions. nil (the default) = full compile, behavior unchanged.
	reserved *ReservedAllocations
}

// NewCompiler constructs a Compiler with a fresh IP allocator.
func NewCompiler() *Compiler {
	return &Compiler{
		ipAllocator: allocator.NewIPAllocator(),
	}
}

// WithReserved sets the reserved resources for edges outside the subgraph and returns the same
// *Compiler for chaining (compiler.NewCompiler().WithReserved(r).Compile(...)). Only controller
// subgraph compiles need it; full compiles (air-gap CLI / API) do not call it and reserved stays nil.
func (c *Compiler) WithReserved(r *ReservedAllocations) *Compiler {
	c.reserved = r
	return c
}

// Compile runs the full compilation pipeline on topo and returns a CompileResult, or an error if
// any validation stage fails.
func (c *Compiler) Compile(topo *model.Topology, keys map[string]KeyPair) (*CompileResult, error) {
	// Pass 1: Schema validation.
	schemaResult := validator.ValidateSchema(topo)
	if !schemaResult.IsValid() {
		return nil, fmt.Errorf("topology failed schema validation: %v", schemaResult.Errors)
	}

	// Pass 2: Semantic validation.
	semanticResult := validator.ValidateSemantic(topo)
	if !semanticResult.IsValid() {
		return nil, fmt.Errorf("topology failed semantic validation: %v", semanticResult.Errors)
	}

	// Collect the non-fatal warnings from both validation stages and return them with the compile
	// result, ensuring every caller (API and CLI) receives these warnings.
	warnings := make([]validator.ValidationError, 0, len(schemaResult.Warnings)+len(semanticResult.Warnings))
	warnings = append(warnings, schemaResult.Warnings...)
	warnings = append(warnings, semanticResult.Warnings...)

	// Pass 3: IP allocation.
	allocatedNodes, err := c.ipAllocator.AllocateIPs(topo)
	if err != nil {
		return nil, fmt.Errorf("IP allocation failed: %w", err)
	}

	// Copy edges to avoid mutating the input.
	compiledEdges := make([]model.Edge, len(topo.Edges))
	copy(compiledEdges, topo.Edges)

	compiledTopo := &model.Topology{
		Project:       topo.Project,
		Domains:       topo.Domains,
		Nodes:         allocatedNodes,
		Edges:         compiledEdges,
		RoutePolicies: topo.RoutePolicies,
		// Stamp the allocation-scheme version used by this compile (invariant I10), so future
		// changes to the pin format can be detected and migrated.
		AllocSchemaVersion: AllocationSchemaVersion,
	}

	// Pass 3: infer capabilities.
	for i := range compiledTopo.Nodes {
		compiledTopo.Nodes[i].Capabilities = InferCapabilitiesFromRole(&compiledTopo.Nodes[i])
	}

	// Pass 3: derive peers.
	// On a subgraph compile c.reserved is non-nil, letting gap-fill avoid the resources occupied by
	// out-of-subgraph edges (the cross-subgraph collision root-cause fix); on a full compile
	// c.reserved==nil, and derivePeers degrades to the original DerivePeers behavior.
	peerMap, pairAllocations, err := derivePeers(compiledTopo, keys, c.reserved)
	if err != nil {
		return nil, fmt.Errorf("deriving WireGuard peer configuration failed: %w", err)
	}

	// Client configs.
	clientConfigs := DeriveClientConfigs(compiledTopo, keys, pairAllocations)

	// Write the resources allocated to each enabled edge back into its pin fields (the six
	// pinned_*), oriented by this edge's from/to direction; also write back the read-only
	// CompiledPort for UI display. After a pin round-trips through frontend persistence, the next
	// compile reuses it verbatim via reserve-then-gap-fill, so a superset topology reproduces
	// byte-identical allocation values for existing edges (invariants I1/I8). See
	// docs/spec/compiler/allocation-stability.md.
	//
	// CompiledPort must equal the port carried in the rendered Endpoint:
	//   - when EndpointPort > 0 (an explicit operator NAT/port-forward override), reflect that
	//     override value verbatim;
	//   - otherwise use the peer interface's allocated listen port (compiler-assigned).
	for i := range compiledTopo.Edges {
		edge := &compiledTopo.Edges[i]
		if !edge.IsEnabled {
			continue
		}

		// Look up the pairAllocation for this edge, keyed by linkid.LinkKey(edge) (invariant I3:
		// the per-peer allocation identity is the linkKey). All edges of the primary class between
		// the same node pair share one link's alloc (oriented per this edge); each backup edge
		// takes the alloc of its own link.
		alloc, ok := pairAllocations[linkid.LinkKey(edge)]
		if !ok {
			continue
		}

		// Orient the pin by this edge's from/to direction: alloc.fromNodeID is the allocation
		// struct's "canonical from"; if it matches this edge's FromNodeID, take values forward,
		// otherwise mirror them.
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

		// CompiledPort: written back only for edges with endpoint_host (matching the rendered
		// Endpoint port).
		if edge.EndpointHost == "" {
			continue
		}
		if edge.EndpointPort > 0 {
			edge.CompiledPort = edge.EndpointPort
			continue
		}
		// Auto-assigned: the peer (toNode) interface's allocated listen port.
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
