package controller

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// genWGPubKey returns a fresh, real WireGuard public key (base64). The controller
// only ever holds public keys (zero-knowledge custody), so the test mirrors that:
// it generates a private key locally, keeps only the public half, and discards the
// private one — the matching private key never enters the store.
func genWGPubKey(t *testing.T) string {
	t.Helper()
	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	return priv.PublicKey().String()
}

// stageTestTopo is a small topology: one router (public), one peer that dials the
// router, and one client that also dials the router. Edges are peer->router and
// client->router (both single outbound, matching the client-edge rule). This is the
// shape the compile-and-stage flow projects down to its enrolled subgraph.
func stageTestTopo() *model.Topology {
	return &model.Topology{
		Project: model.Project{ID: "ctrl-stage-001", Name: "Stage Test"},
		Domains: []model.Domain{{
			ID: "domain-1", Name: "net", CIDR: "10.50.0.0/24",
			AllocationMode: "auto", RoutingMode: "babel",
		}},
		Nodes: []model.Node{
			{
				ID: "node-router", Name: "router", Hostname: "router.example.com",
				Role: "router", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: true, CanForward: true, HasPublicIP: true},
			},
			{
				ID: "node-peer", Name: "peer",
				Role: "peer", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
			},
			{
				ID: "node-client", Name: "client",
				Role: "client", DomainID: "domain-1",
				Capabilities: model.NodeCapabilities{CanAcceptInbound: false, CanForward: false, HasPublicIP: false},
			},
		},
		Edges: []model.Edge{
			{ID: "e-peer", FromNodeID: "node-peer", ToNodeID: "node-router", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
			{ID: "e-client", FromNodeID: "node-client", ToNodeID: "node-router", Type: "public-endpoint", EndpointHost: "198.51.100.1", EndpointPort: 0, Transport: "udp", IsEnabled: true},
		},
	}
}

// putStageTopo stores stageTestTopo via PutTopology and returns the test context.
func putStageTopo(t *testing.T, store Store, tnt TenantID) context.Context {
	t.Helper()
	ctx := context.Background()
	raw, err := json.Marshal(stageTestTopo())
	if err != nil {
		t.Fatalf("marshal topology: %v", err)
	}
	if _, err := store.PutTopology(ctx, tnt, raw); err != nil {
		t.Fatalf("PutTopology: %v", err)
	}
	return ctx
}

// approveNode enrolls a node into the registry as NodeApproved with a public key.
func approveNode(t *testing.T, ctx context.Context, store Store, tnt TenantID, nodeID, pub string) {
	t.Helper()
	if err := store.UpsertNode(ctx, tnt, Node{
		NodeID:      nodeID,
		WGPublicKey: pub,
		Status:      NodeApproved,
	}); err != nil {
		t.Fatalf("UpsertNode(%s): %v", nodeID, err)
	}
}

// containsStr reports whether s contains v.
func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// wgPrivKeyLineRE matches an [Interface] PrivateKey line carrying a real 44-char
// base64 WireGuard private key (43 base64 chars + a trailing '='). It deliberately
// does NOT match the PRIVATEKEY_PLACEHOLDER sentinel (not valid base64), so a hit
// means a real private key leaked into a controller-rendered bundle.
var wgPrivKeyLineRE = regexp.MustCompile(`(?m)^PrivateKey = [A-Za-z0-9+/]{43}=\s*$`)

// routerWGConfig returns the concatenated text of every wireguard/*.conf file in the
// router's staged bundle, fetched via PromoteStaged + GetCurrentBundle.
func routerWGConfig(t *testing.T, ctx context.Context, store Store, tnt TenantID, routerID string) string {
	t.Helper()
	bundle, err := store.GetCurrentBundle(ctx, tnt, routerID)
	if err != nil {
		t.Fatalf("GetCurrentBundle(%s): %v", routerID, err)
	}
	var b strings.Builder
	for path, content := range bundle.Files {
		if strings.HasPrefix(path, "wireguard/") && strings.HasSuffix(path, ".conf") {
			b.Write(content)
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 {
		t.Fatalf("router bundle has no wireguard/*.conf files; have keys: %v", bundleKeys(bundle))
	}
	return b.String()
}

func bundleKeys(b SignedBundle) []string {
	out := make([]string, 0, len(b.Files))
	for k := range b.Files {
		out = append(out, k)
	}
	return out
}

// TestCompileAndStage_RenderWhatsReady covers the three cases of the
// render-what's-ready policy: partial enrollment skips the unenrolled node and omits
// its edge; an empty registry stages nothing; and enrolling the last node fills the
// subgraph in on a later stage.
func TestCompileAndStage_RenderWhatsReady(t *testing.T) {
	// (b) Empty registry: a stored topology but no enrolled node -> nothing staged.
	t.Run("empty registry stages nothing", func(t *testing.T) {
		store := NewMemStore()
		tnt := TenantID("stage-empty")
		ctx := putStageTopo(t, store, tnt)

		res, err := CompileAndStage(ctx, store, tnt, time.Now())
		if err != nil {
			t.Fatalf("CompileAndStage: %v", err)
		}
		if len(res.Staged) != 0 {
			t.Errorf("Staged = %v, want empty", res.Staged)
		}
		// Nothing was staged, so a promote must find nothing to promote.
		if _, err := store.PromoteStaged(ctx, tnt); err != ErrNoStagedBundle {
			t.Errorf("PromoteStaged after empty stage: err = %v, want ErrNoStagedBundle", err)
		}
		// plan-3: a zero-node stage is the design-destroying-deploy shape and must
		// leave an audit trace, not just a transient HTTP response.
		entries, err := store.ListAudit(ctx, tnt)
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		foundEmpty := false
		for _, e := range entries {
			if e.Action == "stage-empty" {
				foundEmpty = true
			}
		}
		if !foundEmpty {
			t.Errorf("no stage-empty audit entry after a zero-node stage (entries: %+v)", entries)
		}
	})

	// (a) Only router+peer enrolled; client NOT enrolled.
	t.Run("partial enrollment skips unenrolled client", func(t *testing.T) {
		store := NewMemStore()
		tnt := TenantID("stage-partial")
		ctx := putStageTopo(t, store, tnt)

		routerPub := genWGPubKey(t)
		peerPub := genWGPubKey(t)
		approveNode(t, ctx, store, tnt, "node-router", routerPub)
		approveNode(t, ctx, store, tnt, "node-peer", peerPub)
		// node-client is intentionally NOT enrolled.

		res, err := CompileAndStage(ctx, store, tnt, time.Now())
		if err != nil {
			t.Fatalf("CompileAndStage: %v", err)
		}
		if !containsStr(res.Staged, "node-router") || !containsStr(res.Staged, "node-peer") {
			t.Errorf("Staged = %v, want both node-router and node-peer", res.Staged)
		}
		if containsStr(res.Staged, "node-client") {
			t.Errorf("Staged = %v, must not contain unenrolled node-client", res.Staged)
		}
		if !containsStr(res.SkippedUnenrolled, "node-client") {
			t.Errorf("SkippedUnenrolled = %v, want node-client", res.SkippedUnenrolled)
		}
		if res.Generation != 1 {
			t.Errorf("Generation = %d, want 1", res.Generation)
		}

		// Only staged, not promoted: the current bundle does not exist yet.
		if _, err := store.GetCurrentBundle(ctx, tnt, "node-router"); err != ErrNotFound {
			t.Errorf("GetCurrentBundle before promote: err = %v, want ErrNotFound", err)
		}

		// Promote the staged generation and read the router's now-current bundle.
		gen, err := store.PromoteStaged(ctx, tnt)
		if err != nil {
			t.Fatalf("PromoteStaged: %v", err)
		}
		if gen != 1 {
			t.Fatalf("PromoteStaged returned gen %d, want 1", gen)
		}

		wg := routerWGConfig(t, ctx, store, tnt, "node-router")

		// Zero-knowledge: the router's own [Interface] PrivateKey is the placeholder,
		// and NO real 44-char base64 private key appears anywhere in its WG config.
		if !strings.Contains(wg, "PrivateKey = "+"PRIVATEKEY_PLACEHOLDER") {
			t.Errorf("router WG config missing placeholder PrivateKey line\n%s", wg)
		}
		if loc := wgPrivKeyLineRE.FindString(wg); loc != "" {
			t.Errorf("router WG config leaked a real private key line: %q", loc)
		}

		// The enrolled peer appears as a [Peer] (its public key is present); the
		// unenrolled client does not (it is not in the subgraph at all).
		if !strings.Contains(wg, peerPub) {
			t.Errorf("router WG config missing enrolled peer's public key %q\n%s", peerPub, wg)
		}
		// The unenrolled client must NOT appear as a peer: with the per-peer interface
		// model the router has exactly one [Peer] (the enrolled peer), proving its
		// dropped edge to the unenrolled client did not render.
		if n := strings.Count(wg, "[Peer]"); n != 1 {
			t.Errorf("router WG config has %d [Peer] blocks, want 1 (unenrolled client must be absent)\n%s", n, wg)
		}

		// Comprehensive zero-knowledge check: NO staged bundle (router OR peer) carries
		// a real WireGuard private key on any of its wireguard/*.conf files.
		for _, id := range []string{"node-router", "node-peer"} {
			b, err := store.GetCurrentBundle(ctx, tnt, id)
			if err != nil {
				t.Fatalf("GetCurrentBundle(%s): %v", id, err)
			}
			for path, content := range b.Files {
				if strings.HasPrefix(path, "wireguard/") && wgPrivKeyLineRE.Match(content) {
					t.Errorf("bundle %s/%s leaked a real WireGuard private key", id, path)
				}
			}
		}
	})

	// (c) Enroll the client too and re-run: the client is now staged and the
	// router's bundle gains the client as a peer.
	t.Run("enrolling client fills in the subgraph", func(t *testing.T) {
		store := NewMemStore()
		tnt := TenantID("stage-fill")
		ctx := putStageTopo(t, store, tnt)

		routerPub := genWGPubKey(t)
		peerPub := genWGPubKey(t)
		clientPub := genWGPubKey(t)
		approveNode(t, ctx, store, tnt, "node-router", routerPub)
		approveNode(t, ctx, store, tnt, "node-peer", peerPub)

		// First stage + promote with only router+peer.
		res1, err := CompileAndStage(ctx, store, tnt, time.Now())
		if err != nil {
			t.Fatalf("CompileAndStage(first): %v", err)
		}
		if !containsStr(res1.SkippedUnenrolled, "node-client") {
			t.Fatalf("first stage SkippedUnenrolled = %v, want node-client", res1.SkippedUnenrolled)
		}
		if _, err := store.PromoteStaged(ctx, tnt); err != nil {
			t.Fatalf("PromoteStaged(first): %v", err)
		}

		// Now enroll the client and stage again.
		approveNode(t, ctx, store, tnt, "node-client", clientPub)
		res2, err := CompileAndStage(ctx, store, tnt, time.Now())
		if err != nil {
			t.Fatalf("CompileAndStage(second): %v", err)
		}
		if !containsStr(res2.Staged, "node-client") {
			t.Errorf("second stage Staged = %v, want node-client now staged", res2.Staged)
		}
		if len(res2.SkippedUnenrolled) != 0 {
			t.Errorf("second stage SkippedUnenrolled = %v, want empty", res2.SkippedUnenrolled)
		}
		if res2.Generation != 2 {
			t.Errorf("second stage Generation = %d, want 2", res2.Generation)
		}

		gen, err := store.PromoteStaged(ctx, tnt)
		if err != nil {
			t.Fatalf("PromoteStaged(second): %v", err)
		}
		if gen != 2 {
			t.Fatalf("PromoteStaged(second) returned gen %d, want 2", gen)
		}

		// The router's bundle now includes the client as a peer.
		wg := routerWGConfig(t, ctx, store, tnt, "node-router")
		if !strings.Contains(wg, clientPub) {
			t.Errorf("router WG config missing newly-enrolled client's public key %q\n%s", clientPub, wg)
		}
		// And a current bundle now exists for the client itself.
		if _, err := store.GetCurrentBundle(ctx, tnt, "node-client"); err != nil {
			t.Errorf("GetCurrentBundle(node-client): %v", err)
		}
	})
}

// nodeOverlayIP reads a node's allocated overlay IP from the stored topology (it is
// written there by CompileAndStage's allocation persistence).
func nodeOverlayIP(t *testing.T, ctx context.Context, store Store, tnt TenantID, nodeID string) string {
	t.Helper()
	rec, err := store.GetTopology(ctx, tnt)
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	var topo model.Topology
	if err := json.Unmarshal(rec.JSON, &topo); err != nil {
		t.Fatalf("unmarshal stored topology: %v", err)
	}
	for _, n := range topo.Nodes {
		if n.ID == nodeID {
			return n.OverlayIP
		}
	}
	t.Fatalf("node %s not in stored topology", nodeID)
	return ""
}

// TestCompileAndStage_ClientNotReady covers the render-what's-ready fix for the client
// role: an enrolled client whose dial target (router) is NOT yet enrolled must be
// treated as not-ready and skipped — NOT fed edgeless to the compiler (which would
// hard-fail the whole stage on the client-edge rule). It activates on a later deploy.
func TestCompileAndStage_ClientNotReady(t *testing.T) {
	store := NewMemStore()
	tnt := TenantID("stage-client-first")
	ctx := putStageTopo(t, store, tnt)

	// Enroll ONLY the client; its dial target node-router is left unenrolled.
	approveNode(t, ctx, store, tnt, "node-client", genWGPubKey(t))

	res, err := CompileAndStage(ctx, store, tnt, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage must not error when only an unready client is enrolled: %v", err)
	}
	if len(res.Staged) != 0 {
		t.Errorf("Staged = %v, want empty (client not ready: its router is unenrolled)", res.Staged)
	}
	if !containsStr(res.SkippedUnenrolled, "node-client") {
		t.Errorf("SkippedUnenrolled = %v, want to include node-client (not ready)", res.SkippedUnenrolled)
	}
	if _, err := store.PromoteStaged(ctx, tnt); err != ErrNoStagedBundle {
		t.Errorf("PromoteStaged: err = %v, want ErrNoStagedBundle (nothing staged)", err)
	}
}

// TestCompileAndStage_AllocationStability pins invariant I10 for the render-what's-ready
// model: enrolling a new node and re-compiling must NOT renumber an already-staged
// node's overlay IP. This works because CompileAndStage persists the compiled
// allocation pins back into the stored topology, which the next compile sticky-pins.
func TestCompileAndStage_AllocationStability(t *testing.T) {
	store := NewMemStore()
	tnt := TenantID("stage-stability")
	ctx := putStageTopo(t, store, tnt)

	approveNode(t, ctx, store, tnt, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tnt, "node-peer", genWGPubKey(t))
	if _, err := CompileAndStage(ctx, store, tnt, time.Now()); err != nil {
		t.Fatalf("CompileAndStage(first): %v", err)
	}
	routerIP := nodeOverlayIP(t, ctx, store, tnt, "node-router")
	peerIP := nodeOverlayIP(t, ctx, store, tnt, "node-peer")
	if routerIP == "" || peerIP == "" {
		t.Fatalf("allocations not persisted to the stored topology: router=%q peer=%q", routerIP, peerIP)
	}

	// Enroll a third node and re-compile: existing nodes must keep their overlay IPs.
	approveNode(t, ctx, store, tnt, "node-client", genWGPubKey(t))
	if _, err := CompileAndStage(ctx, store, tnt, time.Now()); err != nil {
		t.Fatalf("CompileAndStage(second): %v", err)
	}
	if got := nodeOverlayIP(t, ctx, store, tnt, "node-router"); got != routerIP {
		t.Errorf("router overlay IP shifted %q -> %q after another node enrolled (I10 violated)", routerIP, got)
	}
	if got := nodeOverlayIP(t, ctx, store, tnt, "node-peer"); got != peerIP {
		t.Errorf("peer overlay IP shifted %q -> %q after another node enrolled (I10 violated)", peerIP, got)
	}
}
