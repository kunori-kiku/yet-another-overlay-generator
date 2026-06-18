package localcompile

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// updateGolden regenerates the committed golden corpus instead of asserting against it.
// Run `go test ./internal/localcompile/ -run TestContractGolden -update` after an
// INTENTIONAL contract re-version, review the diff, and commit. A plain `go test` run
// (the gate + CI) must NEVER touch the goldens — the run-twice-assert-equal sub-test and
// the byte assertion below prove the corpus is the authoritative frozen byte output.
var updateGolden = flag.Bool("update", false, "regenerate the golden corpus from the current pipeline output")

// fixedCompiledAt is the explicit compile clock injected into every fixture. It feeds only
// manifest.json's compiled_at, which is OUT of the conformance byte set (masked in the
// golden), but pinning it keeps the request fully deterministic so the run-twice-equal
// purity sub-test compares like with like.
var fixedCompiledAt = time.Date(2026, time.June, 18, 0, 0, 0, 0, time.UTC)

const (
	topologiesDir = "testdata/contract/topologies"
	goldenDir     = "testdata/contract/golden"
	signingKeyPEM = "testdata/contract/signing/test-signing-key.pem"
)

// fixture is the on-disk shape of a contract fixture: a topology plus the request knobs
// that, with a fixed Keygen + fixed clock, make Compile fully deterministic. Custody is
// the string form ("airgap" | "agentheld") so the JSON is self-describing; Signing opts
// the fixture into the throwaway test signer (the bundle.sig / signing-pubkey.pem path).
type fixture struct {
	Name     string          `json:"name"`
	Doc      string          `json:"doc"`
	Custody  string          `json:"custody"`
	Signing  bool            `json:"signing"`
	Topology json.RawMessage `json:"topology"`
}

// loadFixtures reads every *.json fixture under topologiesDir, sorted by name so the
// corpus order is stable across platforms.
func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	entries, err := os.ReadDir(topologiesDir)
	if err != nil {
		t.Fatalf("read fixtures dir %s: %v", topologiesDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	fixtures := make([]fixture, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(topologiesDir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		var fx fixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("parse fixture %s: %v", name, err)
		}
		if fx.Name == "" {
			fx.Name = strings.TrimSuffix(name, ".json")
		}
		fixtures = append(fixtures, fx)
	}
	if len(fixtures) < 8 {
		t.Fatalf("contract corpus must hold >=8 fixtures, found %d", len(fixtures))
	}
	return fixtures
}

// requestFor builds the deterministic CompileRequest for a fixture: the parsed topology,
// the fixture's custody, the stdlib ecdhKeygen (so production's default wgtypesKeygen is
// not implicitly required and the seam is exercised), the fixed clock, and — when the
// fixture opts in — the throwaway test signer loaded from the committed PEM.
func requestFor(t *testing.T, fx fixture) CompileRequest {
	t.Helper()
	req := CompileRequest{
		Keygen:     ecdhKeygen{},
		CompiledAt: fixedCompiledAt,
	}
	if err := json.Unmarshal(fx.Topology, &req.Topology); err != nil {
		t.Fatalf("fixture %s: parse topology: %v", fx.Name, err)
	}

	switch fx.Custody {
	case "airgap", "":
		req.Custody = render.AirGap
	case "agentheld":
		req.Custody = render.AgentHeld
	default:
		t.Fatalf("fixture %s: unknown custody %q", fx.Name, fx.Custody)
	}

	if fx.Signing {
		req.SigningKey = loadTestSigner(t)
	}
	return req
}

// loadTestSigner builds a bundlesig.ConfigSigner from the committed THROWAWAY test
// Ed25519 key. The façade is pure — it never reads YAOG_BUNDLE_SIGNING_KEY — so the
// signer is constructed here and injected through CompileRequest.SigningKey. The key is
// a test-only key generated under testdata; it is NOT any production signing key.
func loadTestSigner(t *testing.T) bundlesig.ConfigSigner {
	t.Helper()
	pemBytes, err := os.ReadFile(signingKeyPEM)
	if err != nil {
		t.Fatalf("read test signing key: %v", err)
	}
	priv, err := bundlesig.LoadPrivateKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("parse test signing key: %v", err)
	}
	return &bundlesig.Signing{
		Priv:      priv,
		PubKeyPEM: bundlesig.MarshalPublicKeyPEM(priv.Public().(ed25519.PublicKey)),
	}
}

// goldenArtifacts is the masked, deterministic projection of CompileArtifacts that the
// golden corpus pins. It carries every conformance-IN field (rendered Files, Deploy
// scripts, per-node Checksums, and — when signing is on — base64 Signatures + the
// SigningPubPEM) and DROPS the conformance-OUT fields: Manifest.CompiledAt (a timestamp)
// and Manifest.Checksum (the display-only sha256(fmt.Sprintf("%v", topo)) with no TS
// counterpart) are masked, and the random/keygen-derived private-key material lives only
// inside the (fixed-key) rendered Files, never as a separately-pinned output.
//
// The compiled Topology is summarized to its load-bearing allocator write-backs (overlay
// IPs + the seven pin fields per edge) rather than re-serializing the whole input — those
// write-backs ARE part of the contract (allocated pins/IPs/ports) while the echoed input
// fields are not.
type goldenArtifacts struct {
	Files       map[string]map[string]string `json:"files"`
	Deploy      map[string]string            `json:"deploy"`
	Checksums   map[string]string            `json:"checksums"`
	Signatures  map[string]string            `json:"signatures,omitempty"`
	SigningPub  string                       `json:"signing_pub_pem,omitempty"`
	NodeAlloc   map[string]string            `json:"node_overlay_ip"`
	EdgeAlloc   map[string]edgeAlloc         `json:"edge_alloc"`
	ManifestRaw manifestProjection           `json:"manifest_masked"`
}

// edgeAlloc captures an edge's allocator write-backs — the seven model.Edge pin json tags
// (compiled_port + the six pinned_* fields). These are conformance-IN (allocated
// pins/ports/transit IPs/link-locals) and the surface the pinned-pins round-trip and
// edge-reorder fixtures pin.
type edgeAlloc struct {
	CompiledPort        int    `json:"compiled_port"`
	PinnedFromPort      int    `json:"pinned_from_port"`
	PinnedToPort        int    `json:"pinned_to_port"`
	PinnedFromTransitIP string `json:"pinned_from_transit_ip"`
	PinnedToTransitIP   string `json:"pinned_to_transit_ip"`
	PinnedFromLinkLocal string `json:"pinned_from_link_local"`
	PinnedToLinkLocal   string `json:"pinned_to_link_local"`
}

// manifestProjection is the manifest with the two OUT-of-conformance fields masked: the
// project identity / node count stay (they are deterministic), while CompiledAt and
// Checksum are dropped entirely so a clock tick or a fmt-digest change never reds the
// corpus.
type manifestProjection struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	Version     string `json:"version"`
	NodeCount   int    `json:"node_count"`
}

// project turns a CompileArtifacts into its masked golden projection.
func project(art CompileArtifacts) goldenArtifacts {
	g := goldenArtifacts{
		Files:      art.Files,
		Deploy:     art.Deploy,
		Checksums:  art.Checksums,
		Signatures: art.Signatures,
		NodeAlloc:  map[string]string{},
		EdgeAlloc:  map[string]edgeAlloc{},
		ManifestRaw: manifestProjection{
			ProjectID:   art.Manifest.ProjectID,
			ProjectName: art.Manifest.ProjectName,
			Version:     art.Manifest.Version,
			NodeCount:   art.Manifest.NodeCount,
		},
	}
	if len(art.SigningPubPEM) > 0 {
		g.SigningPub = string(art.SigningPubPEM)
	}
	// An empty Signatures map and an absent one are the same contract state (signing off);
	// normalize to nil so omitempty drops it from the golden.
	if len(g.Signatures) == 0 {
		g.Signatures = nil
	}
	if art.Topology != nil {
		for _, n := range art.Topology.Nodes {
			g.NodeAlloc[n.ID] = n.OverlayIP
		}
		for _, e := range art.Topology.Edges {
			g.EdgeAlloc[e.ID] = edgeAlloc{
				CompiledPort:        e.CompiledPort,
				PinnedFromPort:      e.PinnedFromPort,
				PinnedToPort:        e.PinnedToPort,
				PinnedFromTransitIP: e.PinnedFromTransitIP,
				PinnedToTransitIP:   e.PinnedToTransitIP,
				PinnedFromLinkLocal: e.PinnedFromLinkLocal,
				PinnedToLinkLocal:   e.PinnedToLinkLocal,
			}
		}
	}
	return g
}

// marshalGolden renders the masked projection as indented JSON with a trailing newline,
// so the committed goldens are human-diffable and git-stable. json.Marshal sorts map keys,
// so the byte output is deterministic regardless of Go map iteration order.
func marshalGolden(t *testing.T, g goldenArtifacts) []byte {
	t.Helper()
	out, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	return append(out, '\n')
}

// TestContractGolden is the authoritative byte-freeze: each fixture is run through the
// pure façade with the stdlib ecdhKeygen + a fixed clock, and its masked artifacts must
// byte-equal the committed golden. This is the substrate plan-4 (the TS port) and plan-5
// (the conformance harness) target — a byte regression here is exactly the cross-language
// drift the migration track exists to prevent.
func TestContractGolden(t *testing.T) {
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatalf("ensure golden dir: %v", err)
	}
	for _, fx := range loadFixtures(t) {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			art, err := Compile(requestFor(t, fx))
			if err != nil {
				t.Fatalf("Compile fixture %s: %v", fx.Name, err)
			}
			got := marshalGolden(t, project(art))

			goldenPath := filepath.Join(goldenDir, fx.Name+".golden")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s (run with -update to generate): %v", goldenPath, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("fixture %s byte-differs from golden %s\n--- got ---\n%s\n--- want ---\n%s",
					fx.Name, goldenPath, got, want)
			}
		})
	}
}

// TestContractGolden_PureFunction proves Compile is a pure function (no env / clock /
// filesystem / global leaks): the SAME request run twice yields a byte-identical result.
// It runs every fixture so the proof covers the signing-on, AgentHeld, and mimic paths,
// not just the simplest one.
func TestContractGolden_PureFunction(t *testing.T) {
	for _, fx := range loadFixtures(t) {
		fx := fx
		t.Run(fx.Name, func(t *testing.T) {
			first, err := Compile(requestFor(t, fx))
			if err != nil {
				t.Fatalf("Compile (first) %s: %v", fx.Name, err)
			}
			second, err := Compile(requestFor(t, fx))
			if err != nil {
				t.Fatalf("Compile (second) %s: %v", fx.Name, err)
			}
			a := marshalGolden(t, project(first))
			b := marshalGolden(t, project(second))
			if !bytes.Equal(a, b) {
				t.Errorf("fixture %s: two identical requests produced different output (façade not pure)\n--- run 1 ---\n%s\n--- run 2 ---\n%s", fx.Name, a, b)
			}
		})
	}
}

// reorderStableFiles are the bundle members the as-shipped C1 fix makes byte-stable under
// a benign edge reorder: babeld.conf (the beta.8 sort-by-InterfaceName fix), sysctl, and
// the per-peer wireguard/<iface>.conf files (one file per interface, immune by
// construction). install.sh and the deploy scripts are DELIBERATELY excluded — see the
// note in TestContractGolden_EdgeReorderStable.
var reorderStableFiles = func(p string) bool {
	return p == "babel/babeld.conf" ||
		p == "sysctl/99-overlay.conf" ||
		(strings.HasPrefix(p, "wireguard/") && strings.HasSuffix(p, ".conf"))
}

// TestContractGolden_EdgeReorderStable is the COMPILER-LEVEL C1 pin (owner flag 1): the
// two set-equal edge-reorder fixtures (identical edge SET, reversed edge ARRAY order)
// compile to BYTE-IDENTICAL babeld.conf / sysctl / per-peer wireguard configs. It
// complements the renderer-level unit test (internal/renderer/babel_test.go
// TestRenderBabelConfig_StableUnderPeerReorder) by exercising the FULL pipeline — IP
// allocation, peer derivation, RenderAllBabelConfigs — not just RenderBabelConfig in
// isolation. (Investigation I2 / principle P1: rendered config files must be byte-stable
// under cosmetic edge reorder; this is the as-shipped beta.8 C1 guarantee, NOT a new fix.)
//
// SCOPE — what is and is NOT pinned (a load-bearing finding this fixture surfaced):
// the C1 fix that shipped in beta.8 sorts only the BABEL renderer's peer slice. The
// full-pipeline pair confirms babeld.conf, sysctl, and every wireguard/<iface>.conf are
// byte-identical under reorder — but it ALSO reveals that install.sh and the deploy
// scripts (deploy-all.sh/.ps1) still enumerate per-peer interfaces / nodes in peer-slice
// (edge-array) order, so they are NOT byte-stable under a wholesale array reversal, and
// therefore the per-node checksums.sha256 (which covers install.sh) is not either.
// That residual install.sh/deploy ordering dependence is a C1-CLASS issue beyond the
// shipped babel fix; freezing it (or fixing the script renderer's interface enumeration
// to sort by InterfaceName like babel does) is a roadmap item, NOT this verify-only
// freeze plan's scope (plan-3 introduces no intentional byte change; any C1 code change
// shipped in beta.8 / is owned downstream). The contract doc records this as a documented
// non-guarantee. This test asserts ONLY the surface C1 actually covers today.
func TestContractGolden_EdgeReorderStable(t *testing.T) {
	byName := map[string]fixture{}
	for _, fx := range loadFixtures(t) {
		byName[fx.Name] = fx
	}
	const (
		fwd = "edge-reorder-forward"
		rev = "edge-reorder-reversed"
	)
	fxFwd, okF := byName[fwd]
	fxRev, okR := byName[rev]
	if !okF || !okR {
		t.Fatalf("edge-reorder pair missing: have %q=%v %q=%v", fwd, okF, rev, okR)
	}

	artFwd, err := Compile(requestFor(t, fxFwd))
	if err != nil {
		t.Fatalf("Compile %s: %v", fwd, err)
	}
	artRev, err := Compile(requestFor(t, fxRev))
	if err != nil {
		t.Fatalf("Compile %s: %v", rev, err)
	}

	got := reorderStableProjection(artFwd)
	want := reorderStableProjection(artRev)
	if !bytes.Equal(got, want) {
		t.Errorf("edge-reorder pair rendered DIFFERENT C1-stable bytes (compiler-level C1 regression)\n--- forward ---\n%s\n--- reversed ---\n%s", got, want)
	}
}

// reorderStableProjection serializes a node-keyed map of just the C1-stable bundle files
// (reorderStableFiles), deterministically (json.Marshal sorts map keys), for the
// edge-reorder byte assertion.
func reorderStableProjection(art CompileArtifacts) []byte {
	stable := map[string]map[string]string{}
	for node, files := range art.Files {
		kept := map[string]string{}
		for path, content := range files {
			if reorderStableFiles(path) {
				kept[path] = content
			}
		}
		stable[node] = kept
	}
	out, err := json.Marshal(stable)
	if err != nil {
		panic(err)
	}
	return out
}
