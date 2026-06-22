package compiler

import (
	"context"
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
	// mimicFallbackDefault is the fleet-wide mimic-fallback policy a link inherits when its edge
	// leaves mimic_fallback empty. "" (the default) ⇒ no fleet preference ⇒ resolveMimicFallback
	// floors to "none" — byte-identical to the pre-change pipeline. PURE policy, never allocation.
	mimicFallbackDefault string
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

// WithMimicFallbackDefault sets the fleet-wide mimic-fallback default and returns the same *Compiler
// for chaining. Full air-gap/CLI compiles do not call it (default stays "" ⇒ resolveMimicFallback
// floors to "none" everywhere ⇒ byte-identical). Only the controller threads the operator setting
// through (via localcompile from render.FetchSettings).
func (c *Compiler) WithMimicFallbackDefault(policy string) *Compiler {
	c.mimicFallbackDefault = policy
	return c
}

// Compile runs the full compilation pipeline on topo and returns a CompileResult, or an error if
// any validation stage fails. ctx bounds the IP-allocation pass (Pass 3): an over-large domain
// CIDR is rejected fast (CodeOverlayScanBudgetExceeded) and a long scan is abortable on request
// cancellation. The request context reaches here through localcompile.CompileResultCtx — the
// air-gap HTTP handlers pass r.Context() and the controller subgraph compile passes its request
// ctx; the air-gap CLI and the pure façade entry points pass context.Background(). (plan-8
// consumes ctx in the allocator scan; plan-3's façade threads the live callers' ctx in.)
//
// Compile is the byte-identical shim that delegates to CompileAt with time.Now() — the clock
// is the only impurity it injects, so every existing caller stays unchanged.
func (c *Compiler) Compile(ctx context.Context, topo *model.Topology, keys map[string]KeyPair) (*CompileResult, error) {
	return c.CompileAt(ctx, topo, keys, time.Now())
}

// CompileAt is Compile with the compile clock made explicit: compiledAt is stamped into
// CompileManifest.CompiledAt instead of reading the wall clock internally. This is the seam the
// localcompile façade (plan-3) injects so the local compile path is a pure function — no internal
// time.Now() — which lets the conformance harness (plan-5) run a fixture with a fixed clock and get
// a reproducible result. compiledAt feeds ONLY manifest.json's compiled_at, which is OUT of the
// conformance byte set (display-only); the rendered configs do not depend on it. See
// docs/spec/compiler/io-contract.md (the IN/OUT conformance list).
func (c *Compiler) CompileAt(ctx context.Context, topo *model.Topology, keys map[string]KeyPair, compiledAt time.Time) (*CompileResult, error) {
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
	allocatedNodes, err := c.ipAllocator.AllocateIPs(ctx, topo)
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
	peerMap, pairAllocations, err := derivePeers(compiledTopo, keys, c.reserved, c.mimicFallbackDefault)
	if err != nil {
		return nil, fmt.Errorf("deriving WireGuard peer configuration failed: %w", err)
	}

	// Client configs.
	clientConfigs := DeriveClientConfigs(compiledTopo, keys, pairAllocations, c.mimicFallbackDefault)

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
			CompiledAt:  compiledAt,
			NodeCount:   len(allocatedNodes),
			Checksum:    computeChecksum(compiledTopo),
		},
	}

	return result, nil
}

// computeChecksum is a DISPLAY-ONLY, NON-CANONICAL fingerprint of the compiled
// topology, surfaced in CompileManifest.Checksum purely as a human-facing "did
// anything change?" hint in the UI. It is NOT the bundle digest: the signed,
// canonical per-node bundle digest is produced by internal/bundlesig
// (Canonicalize over the sha256sum -c checksums string), and a signature is
// NEVER taken over this value.
//
// It hashes fmt.Sprintf("%v", topo), whose output depends on Go's struct/map
// formatting and map iteration order, so it is neither stable across Go
// versions nor reproducible by a non-Go implementation. It is therefore
// explicitly OUT OF SCOPE for plan-5's Go<->TS conformance harness — the
// TypeScript local-mode compiler port cannot and should not attempt to
// reproduce this value. Do not promote it to a security or equivalence anchor.
func computeChecksum(topo *model.Topology) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%v", topo)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}
