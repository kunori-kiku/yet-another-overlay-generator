package controller

// compile.go is the controller's compile-and-stage step (plan-4.3a, reworked for the
// keystone CORRECTION of plan-5.1, 2026-06-08): it turns the stored, public-keys-only
// topology plus the enrolled registry into signed per-node bundles staged at the next
// generation, and — when the keystone is ON — builds the OFF-HOST-signable membership
// MANIFEST that binds each node's bundle digest.
//
// Two design commitments shape this file:
//
//   - REUSE the frozen pipeline, do not reimplement it. The compiler, renderer,
//     and exporter stay frozen and dependency-minimal (see
//     docs/spec/controller/persistence.md §The quarantine boundary). This step
//     drives them through the single shared localcompile façade (the one compile
//     authority) — the same path used by the standalone compiler and browser/WASM,
//     keeping the controller's rendered bundles byte-identical — and reads the export
//     back through a temp directory.
//
//   - RENDER WHAT'S READY. The ready subgraph is compiled: managed topology nodes
//     require a NodeApproved registry record with a non-empty WGPublicKey, while manual
//     nodes and client targets use their validated topology public key. Any edge whose
//     far end is not ready is dropped.
//
// KEYSTONE (CORRECTION). The off-host signature must cover what RUNS, not merely the
// membership list. So the staged bundles are exported WITHOUT any trust-list files
// (the trust-list binds the checksums digest and therefore cannot live inside it);
// instead CompileAndStage computes, for every ready node (including delta-skipped
// nodes that keep their served bundle), bundleSHA256 =
// hex(sha256(checksums.sha256)) — and checksums.sha256 covers install.sh + every
// config — and assembles a TrustList whose Members each carry {NodeID, WGPublicKey,
// BundleSHA256}. That manifest is STORED as the staged, to-be-signed manifest (its
// canonical bytes in StoredTrustList.TrustListJSON, an EMPTY SignatureJSON until the
// operator signs off-host). Staging does NOT require a signature; PROMOTING does (see
// PromoteStaged below). The signed manifest is appended to the SERVED file map at
// /config time — never embedded in the bundle's checksum set.
//
// Zero-knowledge custody is preserved end-to-end: GenerateKeys runs in AgentHeld
// mode, the registry holds public keys only, and any stray private key on the
// topology node is cleared before rendering.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
)

// StageResult reports the outcome of CompileAndStage. Staged and SkippedUnenrolled
// are NODE IDs (the registry/agent identity), not node names. Generation is the
// staged generation (CurrentGeneration+1); it becomes current only when the
// operator calls PromoteStaged.
type StageResult struct {
	// Staged holds the node IDs that were compiled and staged this generation — the UPDATED nodes whose
	// bundle content changed (or every enrolled node when the delta-skip is disabled/inapplicable).
	Staged []string
	// UnchangedNodeIDs holds the enrolled nodes SKIPPED this deploy because their freshly compiled bundle
	// digest equals their currently-served bundle (plan-5 delta-skip): they keep their current generation,
	// so their agents see no new generation and never re-apply. Empty when the skip is disabled (keystone
	// rotation / first-pin) or every node changed. Staged + UnchangedNodeIDs together are the full
	// enrolled set for a normal deploy.
	UnchangedNodeIDs []string
	// SkippedUnenrolled holds the node IDs present in the topology but excluded
	// from the render because they are not yet enrolled (not NodeApproved, or no
	// WGPublicKey). Each fills in on a later deploy once it enrolls.
	SkippedUnenrolled []string
	// Generation is the staged generation. Zero when nothing was staged.
	Generation int64
}

// BuildFetchSettings maps the controller's persisted settings into the render-layer FetchSettings
// channel that drives install.sh fetches + the signed artifacts.json. It carries the mimic catalog
// (plan-3) and the agent self-update scalars (plan-9, reusing AgentReleaseBaseURL as the agent
// release base — no duplicate field). It does NOT set AgentRolloutNodeIDs: that per-node canary set
// needs the node list, so the caller fills it via AgentRolloutNodeIDs(cs, nodes) — an empty target
// version means no agent block is emitted regardless, so the set only matters when a rollout is on.
func BuildFetchSettings(cs ControllerSettings) render.FetchSettings {
	return render.FetchSettings{
		GithubProxy:          cs.GithubProxy,
		MimicVersion:         cs.MimicVersion,
		MimicReleaseBase:     cs.MimicReleaseBase,
		MimicDebs:            cs.MimicDebs,
		MimicFallbackDefault: cs.MimicFallbackDefault,
		AgentVersion:         cs.TargetAgentVersion,
		AgentMinVersion:      cs.MinAgentVersion,
		AgentReleaseBase:     cs.AgentReleaseBaseURL,
		AgentBins:            cs.AgentBins,
	}
}

// AgentRolloutNodeIDs computes the set of node IDs that receive the artifacts.json agent block
// (and thus self-update) for the canary-then-fleet rollout (plan-9 D2). When the operator has
// promoted the rollout fleet-wide, EVERY supplied node is in the set; otherwise only the
// configured canary subset (intersected with the actual nodes) is. With no target version
// configured the set is irrelevant (no agent block is emitted), but it is still computed honestly.
func AgentRolloutNodeIDs(cs ControllerSettings, nodes []Node) map[string]bool {
	set := make(map[string]bool)
	if cs.AgentRolloutFleetWide {
		for _, n := range nodes {
			set[n.NodeID] = true
		}
		return set
	}
	canary := make(map[string]bool, len(cs.AgentCanaryNodeIDs))
	for _, id := range cs.AgentCanaryNodeIDs {
		canary[id] = true
	}
	for _, n := range nodes {
		if canary[n.NodeID] {
			set[n.NodeID] = true
		}
	}
	return set
}

// servedBundleDigest returns hex(sha256(checksums.sha256)) of a node's SERVED (promoted) bundle, or
// ok=false when there is no served bundle (never promoted) or its checksums are missing — the caller
// then stages (fail open). This is the identity the delta-skip compares the freshly compiled bundle
// against; it is byte-stable for an unchanged node because it excludes the volatile compiled_at.
func servedBundleDigest(ctx context.Context, store Store, t TenantID, nodeID string) (string, bool) {
	b, err := store.GetCurrentBundle(ctx, t, nodeID)
	if err != nil {
		return "", false
	}
	checks, ok := b.Files["checksums.sha256"]
	if !ok {
		return "", false
	}
	return bundleSHA256(checks), true
}

// stageSkipEnabled reports whether the per-node delta-skip may run for this stage/preview. It is DISABLED
// (a full re-stage) for the two keystone cases that change ZERO bundle bytes yet require the promote to
// flip the served trust-list: FIRST-PIN (keystone on, no served trust-list yet) and ROTATION
// (KeystoneRedeployRequired — the served trust-list is signed under a rotated-away credential). A
// non-keystone tenant, or a healthy keystone, is enabled. Shared by CompileAndStage + DeployPreview so
// the preview can never disagree with what a real Deploy would do.
func stageSkipEnabled(ctx context.Context, store Store, t TenantID, keystoneOn bool, opCred OperatorCredential) (bool, error) {
	if !keystoneOn {
		return true, nil
	}
	if _, err := store.GetServedTrustList(ctx, t); errors.Is(err, ErrNotFound) {
		return false, nil // first-pin: no served trust-list yet
	} else if err != nil {
		return false, fmt.Errorf("controller: loading served trust-list to decide delta-skip: %w", err)
	}
	redeploy, err := KeystoneRedeployRequired(ctx, store, t, opCred)
	if err != nil {
		return false, err
	}
	return !redeploy, nil // rotation → disabled
}

// readBundleDir walks an exported node directory and returns its files keyed by
// bundle-relative slash path (e.g. "install.sh", "wireguard/wg-alpha.conf"). It
// skips directories and normalizes separators with filepath.ToSlash so the bundle
// keys are platform-independent — the same keys the agent expects regardless of the
// controller's OS.
func readBundleDir(nodeDir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.Walk(nodeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(nodeDir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
