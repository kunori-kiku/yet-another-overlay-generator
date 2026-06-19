// Package edgecase is a TEST-ONLY adversarial-input layer (plan-16 / 3.4, Engine B). It lives
// beside internal/regression and is imported by no cmd binary, so it ships in nothing. It builds
// pathological / degenerate / adversarial topologies, drives them through the real compile path
// (compiler.Compile + render.All), and is the source of the class-tagged corpus that plan-5's
// conformance harness and plan-18's real-tunnel bring-up reuse.
//
// generator.go holds the programmatic builders + the compile/render helpers; the *_test.go files
// hold the FuzzCompile target, the no-panic / idempotent / order-independent invariants, the
// S1/S2/S3 DoS reproductions, the C2 re-enable loud-fail, and the corpus writer.
package edgecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// Class tags a fixture so downstream consumers select mechanically: plan-18/3.6 takes `bringup`,
// plan-5/1.5 takes the byte-comparable `stability` subset, the DoS tier takes `dos`.
type Class string

const (
	ClassDoS        Class = "dos"        // inputs that drive unbounded/quadratic compile cost
	ClassDegenerate Class = "degenerate" // empty / single / self-loop / disconnected / all-disabled
	ClassCharset    Class = "charset"    // unicode / over-long / special-character identifiers
	ClassStability  Class = "stability"  // edge-reorder pairs for byte-identity (C1) checks
	ClassBringup    Class = "bringup"    // small valid topologies for real-tunnel bring-up (plan-18)
)

// Fixture is one named, class-tagged adversarial topology.
type Fixture struct {
	Name  string         `json:"name"`
	Class Class          `json:"class"`
	Topo  model.Topology `json:"topo"`
}

// Corpus returns the full deterministic adversarial corpus (committed as corpus/*.json via
// corpus_test.go -update). Scale knobs are kept modest so the committed JSON + the CI fuzz seeds
// stay small; the DoS tests scale a few classes UP locally via the dos*N builders.
func Corpus() []Fixture {
	return []Fixture{
		// --- degenerate ---
		{"empty", ClassDegenerate, model.Topology{Project: proj("empty"), Domains: []model.Domain{dom("d1", "10.50.0.0/24")}}},
		{"single-router", ClassDegenerate, single("router")},
		{"single-peer-no-edge", ClassDegenerate, single("peer")},
		{"self-loop-edge", ClassDegenerate, selfLoop()},
		{"disconnected-pair", ClassDegenerate, disconnectedPair()},
		{"all-edges-disabled", ClassDegenerate, allDisabled()},
		{"colliding-cross-link-pins", ClassDegenerate, collidingCrossLinkPins()},

		// --- charset ---
		{"unicode-names", ClassCharset, charsetNames("路由器-é中文", "对端-\U0001f600")},
		{"long-names", ClassCharset, charsetNames(strings.Repeat("r", 200), strings.Repeat("p", 200))},

		// --- stability (edge-reorder byte-identity, C1) ---
		{"star-3peer", ClassStability, star(3)},
		{"parallel-links-2", ClassStability, parallelLinks(2, false)},

		// --- bringup (small valid, real-tunnel — plan-18) ---
		{"bringup-router-peer", ClassBringup, routerPeer("198.51.100.1")},

		// --- dos (modest committed sizes; the dos_repro tests scale up locally) ---
		{"dos-reserved-ranges", ClassDoS, dosAllocatorReserved(10, 8)},
		{"dos-many-domains", ClassDoS, dosManyDomains(20)},
		{"dos-parallel-backups", ClassDoS, parallelLinks(8, true)},
	}
}

// ---- builders ----

func proj(id string) model.Project { return model.Project{ID: "e2e-" + id, Name: id} }

func dom(id, cidr string) model.Domain {
	return model.Domain{ID: id, Name: id, CIDR: cidr, AllocationMode: "auto", RoutingMode: "babel"}
}

func router(id string) model.Node {
	return model.Node{
		ID: id, Name: id, Hostname: id + ".example.com", Role: "router", DomainID: "d1",
		Capabilities:    model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
		PublicEndpoints: []model.PublicEndpoint{{ID: id + "-ep", Host: id + ".example.com", Port: 51820}},
	}
}

func peer(id string) model.Node {
	return model.Node{ID: id, Name: id, Role: "peer", DomainID: "d1"}
}

func edge(id, from, to, host string, enabled bool) model.Edge {
	return model.Edge{
		ID: id, FromNodeID: from, ToNodeID: to, Type: "public-endpoint",
		EndpointHost: host, EndpointPort: 0, Transport: "udp", IsEnabled: enabled,
	}
}

func single(role string) model.Topology {
	n := router("n1")
	if role == "peer" {
		n = peer("n1")
	}
	return model.Topology{Project: proj("single-" + role), Domains: []model.Domain{dom("d1", "10.50.0.0/24")}, Nodes: []model.Node{n}}
}

func selfLoop() model.Topology {
	return model.Topology{
		Project: proj("self-loop"), Domains: []model.Domain{dom("d1", "10.50.0.0/24")},
		Nodes: []model.Node{router("n1")},
		Edges: []model.Edge{edge("e1", "n1", "n1", "n1.example.com", true)},
	}
}

func disconnectedPair() model.Topology {
	return model.Topology{
		Project: proj("disconnected"), Domains: []model.Domain{dom("d1", "10.50.0.0/24")},
		Nodes: []model.Node{router("r1"), peer("p1")},
	}
}

func allDisabled() model.Topology {
	t := routerPeer("198.51.100.1")
	for i := range t.Edges {
		t.Edges[i].IsEnabled = false
	}
	t.Project = proj("all-disabled")
	return t
}

func charsetNames(rName, pName string) model.Topology {
	r := router("r1")
	r.Name, r.Hostname = rName, "r1.example.com"
	p := peer("p1")
	p.Name = pName
	return model.Topology{
		Project: proj("charset"), Domains: []model.Domain{dom("d1", "10.50.0.0/24")},
		Nodes: []model.Node{r, p}, Edges: []model.Edge{edge("e1", "p1", "r1", "r1.example.com", true)},
	}
}

// collidingCrossLinkPins builds the C2 corruption shape: two DIFFERENT links (r1<->pa and r1<->pb)
// whose edges both pin the SAME transit-IP pair (10.10.0.1/10.10.0.2) — exactly the "pin occupied
// by two different links" state a stale re-enabled edge leaves behind. Only the transit IPs are
// pinned (ports/link-locals left for gap-fill) so the collision is isolated to one resource. The
// compiler's semantic validator rejects this LOUD (CodePinTransitIPDuplicateCrossLink); the shipped
// normalize.HealCollidingPins repairs it. Both halves are locked by c2_reenable_test.go.
func collidingCrossLinkPins() model.Topology {
	d := dom("d1", "10.50.0.0/24")
	d.TransitCIDR = "10.10.0.0/24"
	mk := func(id, from string) model.Edge {
		e := edge(id, from, "r1", "r1.example.com", true)
		e.PinnedFromTransitIP, e.PinnedToTransitIP = "10.10.0.2", "10.10.0.1"
		return e
	}
	return model.Topology{
		Project: proj("colliding-pins"), Domains: []model.Domain{d},
		Nodes: []model.Node{router("r1"), peer("pa"), peer("pb")},
		Edges: []model.Edge{mk("e-a", "pa"), mk("e-b", "pb")},
	}
}

// routerPeer is the canonical small valid topology (one router + one peer + one edge).
func routerPeer(endpointHost string) model.Topology {
	return model.Topology{
		Project: proj("router-peer"), Domains: []model.Domain{dom("d1", "10.50.0.0/24")},
		Nodes: []model.Node{router("r1"), peer("p1")},
		Edges: []model.Edge{edge("e1", "p1", "r1", endpointHost, true)},
	}
}

// star builds a router with n peers each on its own edge (a clean multi-link graph).
func star(n int) model.Topology {
	t := model.Topology{Project: proj(fmt.Sprintf("star-%d", n)), Domains: []model.Domain{dom("d1", "10.50.0.0/24")}, Nodes: []model.Node{router("r1")}}
	for i := 0; i < n; i++ {
		pid := fmt.Sprintf("p%d", i)
		t.Nodes = append(t.Nodes, peer(pid))
		t.Edges = append(t.Edges, edge(fmt.Sprintf("e%d", i), pid, "r1", "r1.example.com", true))
	}
	return t
}

// parallelLinks builds n edges between the SAME router/peer pair. With backup=true the extras are
// backup links (each gets its own linkKey #edgeID) — the quadratic backup gap-fill (S3) shape.
func parallelLinks(n int, backup bool) model.Topology {
	t := model.Topology{Project: proj(fmt.Sprintf("parallel-%d", n)), Domains: []model.Domain{dom("d1", "10.50.0.0/24")}, Nodes: []model.Node{router("r1"), peer("p1")}}
	for i := 0; i < n; i++ {
		e := edge(fmt.Sprintf("e%d", i), "p1", "r1", "r1.example.com", true)
		if backup && i > 0 {
			e.Role = model.EdgeRoleBackup
		}
		t.Edges = append(t.Edges, e)
	}
	return t
}

// dosBackupGapFill builds n parallel backup links between ONE pair, over a wide transit CIDR. This
// is the literal "parallel-links" S3 shape, but it surfaces a SEPARATE bounded edge: every backup
// to the same peer derives its WG interface name from the same base (wg-<peer>) plus a short hash,
// and that hash namespace is small — so beyond ~15 backups to one peer the names collide and the
// semantic validator rejects the topology LOUD (a coded "rename the colliding node" error) before
// gap-fill ever runs. dosBackupInterfaceCollisionFloor records where that floor sits. The takeaway
// (recorded in the findings ledger): the quadratic gap-fill is UNreachable via parallel backups to
// one pair — the interface-name gate caps the fan-out first. Gap-fill at scale is reached instead
// via many DISTINCT links (dosStarGapFill).
func dosBackupGapFill(n int) model.Topology {
	d := dom("d1", "10.50.0.0/16")
	d.TransitCIDR = "10.10.0.0/16"
	t := model.Topology{Project: proj("dos-backup-gapfill"), Domains: []model.Domain{d}, Nodes: []model.Node{router("r1"), peer("p1")}}
	for i := 0; i < n; i++ {
		e := edge(fmt.Sprintf("e%d", i), "p1", "r1", "r1.example.com", true)
		if i > 0 {
			e.Role = model.EdgeRoleBackup
		}
		t.Edges = append(t.Edges, e)
	}
	return t
}

// dosBackupInterfaceCollisionFloor is the number of parallel backup links to one peer at which the
// WG-interface-name hash namespace first collides (semantic validation then rejects loud). Empirically
// deterministic for the e0..eN edge-ID sequence; pinned by TestDegenerateBackupInterfaceCollision.
const dosBackupInterfaceCollisionFloor = 16

// dosStarGapFill builds the gap-fill-at-scale shape that IS reachable: one router with n DISTINCT
// peers, each on its own primary link, over a WIDE transit CIDR (/16) so the per-link transit-pair
// gap-fill runs for all n links without the default /24 transit pool exhausting and without any
// interface-name collision (distinct peer names → distinct wg-<peer> interface names). This drives
// peers.go gapFillTransitPair n times — the re-ParseCIDR-per-iteration surface — at scale.
func dosStarGapFill(n int) model.Topology {
	d := dom("d1", "10.50.0.0/16")
	d.TransitCIDR = "10.10.0.0/16"
	t := model.Topology{Project: proj("dos-star-gapfill"), Domains: []model.Domain{d}, Nodes: []model.Node{router("r1")}}
	for i := 0; i < n; i++ {
		pid := fmt.Sprintf("p%d", i)
		t.Nodes = append(t.Nodes, peer(pid))
		t.Edges = append(t.Edges, edge(fmt.Sprintf("e%d", i), pid, "r1", "r1.example.com", true))
	}
	return t
}

// dosAllocatorReserved builds the S1 shape: a wide (/8) domain with many reserved ranges + nodes,
// so the allocator scans every reserved net per candidate IP. nReserved reserved /24s, nNodes nodes.
func dosAllocatorReserved(nNodes, nReserved int) model.Topology {
	d := dom("d1", "10.0.0.0/8")
	for i := 0; i < nReserved; i++ {
		d.ReservedRanges = append(d.ReservedRanges, fmt.Sprintf("10.%d.0.0/24", i))
	}
	t := model.Topology{Project: proj("dos-reserved"), Domains: []model.Domain{d}, Nodes: []model.Node{router("r1")}}
	for i := 0; i < nNodes; i++ {
		pid := fmt.Sprintf("p%d", i)
		t.Nodes = append(t.Nodes, peer(pid))
		t.Edges = append(t.Edges, edge(fmt.Sprintf("e%d", i), pid, "r1", "r1.example.com", true))
	}
	return t
}

// dosManyDomains builds the S2 shape: many domains, each carrying reserved ranges and one node, so
// the allocator runs a reserved-range-skipping scan once per domain. Domain/ReservedRange counts
// are bounded only by the 4 MiB body cap (NOT the node/edge schema bounds), so this is the
// "unbounded-domains-and-reserved-ranges" surface. Each domain gets its own node (disconnected is
// valid) so every domain's allocation path is actually exercised, not just parsed.
//
// Domains are distinct /24s addressed across two octets (10.<i/256>.<i%256>.0/24), which stays
// valid for thousands of domains — unlike a single-octet 10.<i>.0.0/16 scheme that overflows past
// i=255. Each domain reserves a handful of single IPs (a /24 cannot contain a /24 sub-range), so
// the per-domain scan still walks the reserved-skip path.
func dosManyDomains(nDomains int) model.Topology {
	t := model.Topology{Project: proj("dos-domains")}
	for i := 0; i < nDomains; i++ {
		did := fmt.Sprintf("d%d", i)
		hi, lo := i/256, i%256
		d := dom(did, fmt.Sprintf("10.%d.%d.0/24", hi, lo))
		for j := 0; j < 8; j++ {
			d.ReservedRanges = append(d.ReservedRanges, fmt.Sprintf("10.%d.%d.%d", hi, lo, j+1))
		}
		t.Domains = append(t.Domains, d)
		n := peer(fmt.Sprintf("n%d", i))
		n.DomainID = did
		t.Nodes = append(t.Nodes, n)
	}
	return t
}

// ---- compile/render helpers ----

// KeysFor builds a deterministic fake KeyPair per node id (the allocation/render path embeds them
// verbatim; key generation lives elsewhere — mirrors allocation_stability_test's stableKeys).
func KeysFor(t model.Topology) map[string]compiler.KeyPair {
	keys := make(map[string]compiler.KeyPair, len(t.Nodes))
	for _, n := range t.Nodes {
		keys[n.ID] = compiler.KeyPair{PrivateKey: "priv-" + n.ID + "-fake", PublicKey: "pub-" + n.ID + "-fake"}
	}
	return keys
}

// DeepCopy returns a fully independent copy of t via a JSON round-trip. A plain value copy
// shares the Nodes/Edges/Domains backing arrays, and compiler.Compile stamps pins + compiled
// ports back onto edges in place — so without a deep copy a first compile would mutate the
// input and a second compile (idempotency / order-independence check) would see different
// bytes. The JSON round-trip also mirrors how a topology actually travels (localStorage / the
// wire), so it is the realistic copy, not a bespoke one.
func DeepCopy(t model.Topology) model.Topology {
	b, err := json.Marshal(t)
	if err != nil {
		panic(fmt.Sprintf("edgecase: marshal topology: %v", err)) // unreachable for model.Topology
	}
	var cp model.Topology
	if err := json.Unmarshal(b, &cp); err != nil {
		panic(fmt.Sprintf("edgecase: unmarshal topology: %v", err))
	}
	return cp
}

// Compile runs the real compiler over an independent deep copy of the topology (input-pure)
// with the given context, so callers may compile the same fixture repeatedly and compare bytes.
func Compile(ctx context.Context, t model.Topology) (*compiler.CompileResult, error) {
	cp := DeepCopy(t)
	return compiler.NewCompiler().Compile(ctx, &cp, KeysFor(t))
}

// CompileAndRender runs Compile then render.All so result.BabelConfigs / WireGuardConfigs are
// populated (the C1 byte-identity check compares the rendered babel output).
func CompileAndRender(ctx context.Context, t model.Topology) (*compiler.CompileResult, error) {
	res, err := Compile(ctx, t)
	if err != nil {
		return nil, err
	}
	if err := render.All(res, KeysFor(t), render.FetchSettings{}); err != nil {
		return nil, err
	}
	return res, nil
}
