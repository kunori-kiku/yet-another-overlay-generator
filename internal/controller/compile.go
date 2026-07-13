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
//     authority) — exactly as the air-gap callers do, keeping the controller's rendered
//     bundles byte-identical — and reads the export back through a temp directory.
//
//   - RENDER WHAT'S READY. Only the enrolled subgraph is compiled: a topology node
//     is included iff its registry record is NodeApproved with a non-empty
//     WGPublicKey, and any edge whose far end is not enrolled is dropped.
//
// KEYSTONE (CORRECTION). The off-host signature must cover what RUNS, not merely the
// membership list. So the staged bundles are exported WITHOUT any trust-list files
// (the trust-list binds the checksums digest and therefore cannot live inside it);
// instead CompileAndStage computes, for every staged node, bundleSHA256 =
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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/localcompile"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/render"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
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

// CompileSubgraph runs the read-only compile half shared by CompileAndStage and the
// operator compile-preview: build the enrolled subgraph of `topo` from the registry
// `nodes`, then drive the frozen, zero-knowledge pipeline through the localcompile façade
// (the single compile authority) in AgentHeld custody — private keys are
// PRIVATEKEY_PLACEHOLDER, never real material. It performs NO store writes, NO allocation
// persist, NO export, and NO staging; the caller decides what to do with the rendered result.
//
// Returns a NIL result when no node is enrolled (subgraph.Nodes empty) — the caller
// handles that case (CompileAndStage purges + audits; a preview reports "nothing
// enrolled"). `skipped` lists the node IDs present in the topology but dropped from the
// render because they are not yet enrolled. Custody invariant: because the façade runs
// AgentHeld, neither the returned result nor anything rendered from it contains a real
// private key — making this safe to surface to an authenticated operator (PR6 preview).
//
// ctx bounds the compile: an early cancellation check short-circuits before the reserved-pin
// scan, and it is then threaded into the façade (CompileResultCtx) so the allocator's per-node
// scan is cancellable on a client disconnect — the per-node scan budget remains the hard DoS
// bound for an over-large CIDR. The frozen contract itself stays context-free (the TS port
// mirrors a context-free seam); ctx is the orthogonal Go-runtime param the live callers pass.
func CompileSubgraph(ctx context.Context, topo *model.Topology, nodes []Node, fs render.FetchSettings) (*compiler.CompileResult, model.Topology, []string, error) {
	// Validate the operator-asserted identity of any MANUAL (hand-deployed, agent-less) node before it
	// is admitted: a manual node carries its own pre-known public key (no enrollment proves it), so it
	// must have one and it must be unique across the fleet. This runs on BOTH the stage and the compile
	// -preview path (both call here), so a missing/colliding manual key is a LOUD error, not the silent
	// exclusion enrolledSubgraph would otherwise apply.
	if err := validateManualNodes(topo, nodes); err != nil {
		return nil, model.Topology{}, nil, err
	}
	subgraph, skipped := enrolledSubgraph(topo, nodes)
	if len(subgraph.Nodes) == 0 {
		return nil, subgraph, skipped, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, subgraph, skipped, err
	}
	// Reserve the allocation pins held by edges in the FULL topology that are NOT in this
	// subgraph (dropped because a far end is not yet enrolled). Without this, the subgraph's
	// gap-fill restarts from .1 and can hand a fresh edge a transit IP / port / link-local that
	// a dropped edge still pins in storage — and since each incremental enrollment compiles a
	// DIFFERENT subgraph, two edges that were never compiled together collide (the "pin occupied
	// by two different links" validate error). Reserving the excluded edges' pins makes the
	// subgraph allocate around them, so a new node's links never collide with an out-of-subgraph
	// link. (Existing corruption is cleaned by the normalize-layer heal; this only prevents new.)
	included := make(map[string]bool, len(subgraph.Edges))
	for i := range subgraph.Edges {
		included[subgraph.Edges[i].ID] = true
	}
	reserved := compiler.BuildReservedFromExcludedEdges(topo, included)

	// Drive the frozen pipeline through the single shared façade (the local compile
	// authority), exactly as the air-gap callers do — keeping the controller's rendered
	// bundles byte-identical (the keystone bundle digest + bundle.sig depend on it). The
	// custody and subgraph semantics are preserved verbatim: Custody AgentHeld (private
	// keys stay PRIVATEKEY_PLACEHOLDER, never real material), the default wgtypesKeygen,
	// the WithReserved subgraph allocation path via req.Reserved, and the controller's
	// FetchSettings. CompileResult returns the raw result so CompileAndStage / the operator
	// preview keep their *compiler.CompileResult shape. CompiledAt feeds only
	// manifest.json's compiled_at (out of the checksummed set), matching the prior
	// time.Now() stamp.
	result, err := localcompile.CompileResultCtx(ctx, localcompile.CompileRequest{
		Topology:   subgraph,
		Custody:    render.AgentHeld,
		Fetch:      fs,
		Reserved:   reserved,
		CompiledAt: time.Now(),
	})
	if err != nil {
		return nil, subgraph, skipped, fmt.Errorf("controller: compiling enrolled subgraph: %w", err)
	}
	return result, subgraph, skipped, nil
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

// CompileAndStage renders the enrolled subgraph of the stored topology into signed
// per-node bundles and stages them at the next generation. When the keystone is ON it
// also builds the off-host-signable membership manifest (binding each node's bundle
// digest) and stores it as the staged, UNSIGNED manifest — staging never requires a
// signature.
//
// The flow:
//
//  1. Load the stored topology (ErrNotFound → empty result, no error).
//  2. Build the enrolled subgraph; drop edges to unenrolled peers. Zero enrolled →
//     empty result, no error.
//  3. GenerateKeys(AgentHeld) → Compile → render.All on the subgraph.
//  4. Export to a temp dir (removed on return) — WITHOUT any trust-list files.
//  5. Read each enrolled node's exported dir back into a file map and StageBundle it.
//  6. KEYSTONE ON: compute each staged node's bundle digest, assemble the manifest with
//     the monotonic epoch, and store it as the staged (unsigned) manifest.
//  7. Append one "stage" audit entry.
//
// Bundles are signed iff YAOG_BUNDLE_SIGNING_KEY is set — that tier-1 signing happens
// inside artifacts.Export (the Phase-0 env path), not here.
// StageOption configures a CompileAndStage run. The only knob today is FORCE — re-staging a node even
// when its bundle digest is unchanged (the operator escape hatch for on-host drift/rescue; the delta-skip
// otherwise leaves an unchanged node alone).
type StageOption func(*stageConfig)

type stageConfig struct {
	forceAll   bool
	forceNodes map[string]bool
}

// WithForceAll re-stages EVERY enrolled node even if unchanged (fleet-wide force redeploy).
func WithForceAll() StageOption { return func(c *stageConfig) { c.forceAll = true } }

// WithForceNodes re-stages the named nodes even if unchanged (per-node force redeploy).
func WithForceNodes(ids ...string) StageOption {
	return func(c *stageConfig) {
		if c.forceNodes == nil {
			c.forceNodes = make(map[string]bool, len(ids))
		}
		for _, id := range ids {
			c.forceNodes[id] = true
		}
	}
}

func (c stageConfig) forced(nodeID string) bool { return c.forceAll || c.forceNodes[nodeID] }

func CompileAndStage(ctx context.Context, store Store, t TenantID, now time.Time, opts ...StageOption) (StageResult, error) {
	var cfg stageConfig
	for _, o := range opts {
		o(&cfg)
	}
	// Serialize the whole stage against any concurrent stage/promote for this
	// tenant (review finding): the sequence below is many individual Store calls,
	// and a promote landing mid-loop would flip a PARTIAL fresh stage set and
	// permanently strand the remainder (their provisional generation would equal
	// the now-current one, so the scoped promote filter excludes them forever);
	// two interleaved stages would purge each other's freshly staged bundles.
	defer lockTenantOps(t)()

	// (1) Load the stored, public-keys-only topology. No stored topology is a
	// benign no-op: there is nothing to stage yet (and nothing can be staged —
	// staging requires a stored topology — so there is nothing to purge either).
	rec, err := store.GetTopology(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return StageResult{}, nil
		}
		return StageResult{}, fmt.Errorf("controller: loading topology to stage: %w", err)
	}
	var topo model.Topology
	if err := json.Unmarshal(rec.JSON, &topo); err != nil {
		return StageResult{}, fmt.Errorf("controller: parsing stored topology: %w", err)
	}
	// Self-heal any pre-existing cross-link pin collision in the STORED topology before compiling, so
	// a fleet still carrying corruption persisted by an older buggy compile converges on DEPLOY without
	// requiring an explicit re-save: the colliding edges are stripped here, re-allocated cleanly by the
	// subgraph compile below (the out-of-subgraph reservation keeps every other edge stable), and
	// persisted back via persistAllocations. No-op (and no re-store) when already collision-free, so
	// healthy fleets see zero drift. Complements the update-topology write-path heal (clean on save).
	normalize.HealCollidingPins(&topo)

	// (2)+(3) Build the enrolled subgraph and drive the frozen compile pipeline
	// (AgentHeld keys → compile → render) via the shared CompileSubgraph helper.
	// `result` is nil when no node is enrolled — handled by the empty path below.
	nodes, err := store.ListNodes(ctx, t)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: listing nodes to stage: %w", err)
	}
	// Load the tenant settings (defaults applied) and map them into the render FetchSettings so a
	// configured mimic catalog flows into install.sh + the signed artifacts.json. No mimic catalog
	// ⇒ zero-relevant fs ⇒ no artifacts.json (D4), bundles byte-identical to today. An absent
	// settings record is normal (most deploys never set one) → fall back to defaults, never fail.
	cs, err := store.GetSettings(ctx, t)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return StageResult{}, fmt.Errorf("controller: loading settings to stage: %w", err)
	}
	fs := BuildFetchSettings(cs.WithDefaults())
	fs.AgentRolloutNodeIDs = AgentRolloutNodeIDs(cs, nodes)
	result, subgraph, skipped, err := CompileSubgraph(ctx, &topo, nodes, fs)
	if err != nil {
		return StageResult{}, err
	}

	// Nothing enrolled → nothing to render or stage. Report the skips so the caller
	// can surface "no node has enrolled yet" — and leave an audit trace (plan-3):
	// a stage that staged ZERO nodes is exactly the shape of a design-destroying
	// deploy (every node silently skipped), so its occurrence must be visible in
	// the audit log, not just in a transient HTTP response. Best-effort: the audit
	// must not turn the benign no-op into an error.
	//
	// The purge MUST still run on this path (review finding): an empty stage is a
	// stage — the previous stage's bundles keep their promotable provisional
	// generation, so without the purge an operator who retracted the whole design
	// and "cleared" it with an empty stage would have the retracted bundles flip
	// LIVE on the next promote (running install.sh as root with a dead design).
	if len(subgraph.Nodes) == 0 {
		purged, err := store.PruneStagedBundles(ctx, t, nil)
		if err != nil {
			return StageResult{}, fmt.Errorf("controller: purging staged bundles on empty stage: %w", err)
		}
		for _, nodeID := range purged {
			appendStageAudit(ctx, store, t, now, "purge-staged", nodeID)
		}
		appendStageAudit(ctx, store, t, now, "stage-empty", "")
		return StageResult{SkippedUnenrolled: skipped}, nil
	}

	// Is the keystone ON for this tenant? A pinned operator credential turns it on. We
	// read it up front so a store failure (other than ErrNotFound) fails fast, but note
	// the keystone gate to STAGE is intentionally weak: we build + store the manifest,
	// but DO NOT require a signature here (the signature gate is in PromoteStaged).
	keystoneOn := false
	var opCred OperatorCredential
	if cred, err := store.GetOperatorCredential(ctx, t); err == nil {
		keystoneOn = true
		opCred = cred
	} else if !errors.Is(err, ErrNotFound) {
		return StageResult{}, fmt.Errorf("controller: loading operator credential to stage: %w", err)
	}

	// (plan-5) The per-node DELTA-SKIP: a node whose freshly compiled bundle digest equals its served
	// bundle is NOT re-staged (it keeps its generation, its agent never re-applies) — UNLESS a keystone
	// rotation/first-pin forces a full re-stage. The same decision drives the plan-6 pre-deploy preview,
	// so it lives in the shared stageSkipEnabled.
	skipEnabled, err := stageSkipEnabled(ctx, store, t, keystoneOn, opCred)
	if err != nil {
		return StageResult{}, err
	}

	// Persist the compiled allocation pins back into the FULL stored topology so a later
	// re-compile sticky-pins them (invariant I10). rec.JSON is passed so a write-back
	// that changes NOTHING (sticky pins re-derived byte-identically) is skipped instead
	// of burning one of the bounded history slots.
	if err := persistAllocations(ctx, store, t, &topo, result.Topology, rec.JSON); err != nil {
		return StageResult{}, err
	}

	// Signing-anchor invariant: before producing any bundle, reconcile the configured bundle
	// signing key against the per-tenant pinned anchor, so a redeploy that DROPPED or SWAPPED the
	// key is caught here instead of silently shipping unsigned/differently-signed bundles. (The
	// actual signing still happens inside artifacts.Export from the same env key.)
	if err := enforceSigningAnchor(ctx, store, t, now); err != nil {
		return StageResult{}, err
	}

	// (4) Export to a temp dir we own and remove on return. The export carries NO
	// trust-list files: the off-host manifest binds each node's checksums.sha256 digest,
	// so the trust-list cannot live inside that very checksum set. The served file map
	// appends trustlist.json/.sig at /config time instead.
	tmp, err := os.MkdirTemp("", "yaog-stage-")
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: creating stage temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	if _, err := artifacts.Export(result, tmp); err != nil {
		return StageResult{}, fmt.Errorf("controller: exporting bundles to stage: %w", err)
	}

	// (5) Read each enrolled node's exported dir back into a file map and stage it at
	// the next generation. While doing so, capture each node's checksums.sha256 so the
	// keystone manifest (step 6) can bind its digest.
	cur, err := store.CurrentGeneration(ctx, t)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: reading current generation: %w", err)
	}
	nextGen := cur + 1

	var staged []string
	var unchanged []string
	digests := make(map[string]string, len(subgraph.Nodes)) // nodeID -> bundleSHA256 (ALL nodes, for the manifest)
	pubKeys := make(map[string]string, len(subgraph.Nodes)) // nodeID -> wg public key (stamped on the subgraph node: enrollment registry for a managed node, topology for a manual node)
	for _, node := range subgraph.Nodes {
		nodeDir := filepath.Join(tmp, node.Name)
		files, err := readBundleDir(nodeDir)
		if err != nil {
			return StageResult{}, fmt.Errorf("controller: reading bundle for node %s: %w", node.ID, err)
		}
		checks, hasChecks := files["checksums.sha256"]
		if keystoneOn && !hasChecks {
			return StageResult{}, fmt.Errorf("controller: staged bundle for node %s has no checksums.sha256 to bind", node.ID)
		}
		// The content identity for BOTH the delta-skip and the keystone binding: hex(sha256(checksums.sha256)),
		// which excludes the volatile compiled_at, so an UNCHANGED node recompiles to the same digest.
		newDigest := ""
		if hasChecks {
			newDigest = bundleSHA256(checks)
		}
		if keystoneOn {
			// Bind EVERY enrolled node's digest (updated AND skipped) into the manifest — a skipped node's
			// newDigest equals its served digest, so the regenerated trust-list still binds the right value
			// and never DROPS the skipped node (which would break its served bundle's verification).
			digests[node.ID] = newDigest
			pubKeys[node.ID] = node.WireGuardPublicKey
		}
		// (plan-5) SKIP a node whose freshly compiled bundle is byte-identical to its SERVED bundle: no
		// StageBundle, so PromoteStaged never bumps its DesiredGeneration and its agent never re-applies.
		// FAIL OPEN — any doubt (skip disabled, no digest, no served bundle, unreadable served checksums,
		// or a mismatch) stages normally; never leave a node on a stale config.
		if skipEnabled && !cfg.forced(node.ID) && newDigest != "" {
			if servedDigest, ok := servedBundleDigest(ctx, store, t, node.ID); ok && servedDigest == newDigest {
				unchanged = append(unchanged, node.ID)
				continue
			}
		}
		if err := store.StageBundle(ctx, t, SignedBundle{
			NodeID:     node.ID,
			Generation: nextGen,
			Files:      files,
			IsStaged:   true,
		}); err != nil {
			return StageResult{}, fmt.Errorf("controller: staging bundle for node %s: %w", node.ID, err)
		}
		staged = append(staged, node.ID)
	}

	// (plan-5) ZERO-CHANGED short-circuit: every enrolled node compiled byte-identical to its SERVED
	// bundle (delta-skip active), so nothing was staged. Do NOT re-stage the manifest and do NOT report a
	// new generation — nothing is promotable. But we MUST still PURGE any lingering staged bundle: when
	// len(staged)==0 every enrolled node matches served, so a bundle left staged by a prior
	// stage-without-promote (e.g. an off-host sign-wait) is a SUPERSEDED design — leaving it would let the
	// next /promote flip a reverted/retracted config LIVE (the beta.4-6 stale-config custody bug; a review
	// finding). `staged` is empty here, so this purges ALL staged bundles, exactly as the empty-subgraph
	// path does for the same reason. (Reached only when the skip is enabled — a keystone rotation/first-pin
	// disables it and always stages every node.)
	if len(staged) == 0 {
		purged, pruneErr := store.PruneStagedBundles(ctx, t, staged)
		for _, nodeID := range purged {
			appendStageAudit(ctx, store, t, now, "purge-staged", nodeID)
		}
		if pruneErr != nil {
			return StageResult{}, fmt.Errorf("controller: purging staged bundles on a zero-changed stage: %w", pruneErr)
		}
		appendStageAudit(ctx, store, t, now, "stage-unchanged", "")
		return StageResult{
			UnchangedNodeIDs:  unchanged,
			SkippedUnenrolled: skipped,
			Generation:        cur, // unchanged — no new generation
		}, nil
	}

	// (5b) Purge staged bundles that are NOT part of this stage set (plan-3): a
	// node removed from the design since the previous stage would otherwise leave
	// its stale staged bundle behind, and the next promote would flip it live.
	// One audit entry per purged node keeps the disappearance attributable —
	// written BEFORE the error check, so a prune that failed partway still leaves
	// an audit trace for everything it actually removed (review finding).
	purged, pruneErr := store.PruneStagedBundles(ctx, t, staged)
	for _, nodeID := range purged {
		appendStageAudit(ctx, store, t, now, "purge-staged", nodeID)
	}
	if pruneErr != nil {
		return StageResult{}, fmt.Errorf("controller: purging stale staged bundles: %w", pruneErr)
	}

	// (6) KEYSTONE ON: build the off-host-signable manifest binding each staged node's
	// bundle digest, then STORE it as the staged (unsigned) manifest. Staging does not
	// require a signature; PromoteStaged refuses to promote until a valid off-host
	// signature over THESE exact bytes exists.
	if keystoneOn {
		if err := stageManifest(ctx, store, t, digests, pubKeys); err != nil {
			return StageResult{}, err
		}
	}

	// (7) One audit entry for the whole stage operation. Post-commit (the bundles
	// are staged), so best-effort like the other stage-path audits.
	appendStageAudit(ctx, store, t, now, "stage", "")

	return StageResult{
		Staged:            staged,
		UnchangedNodeIDs:  unchanged,
		SkippedUnenrolled: skipped,
		Generation:        nextGen,
	}, nil
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

// NodeDeployChange is one node's entry in a deploy preview: whether an (unforced) Deploy would RE-STAGE
// it (its bundle content changed vs served, or a keystone full-restage is pending).
type NodeDeployChange struct {
	NodeID  string
	Name    string
	Changed bool
}

// DeployPreviewResult is the plan-6 dry-run of what a Deploy (CompileAndStage) WOULD do, without staging.
type DeployPreviewResult struct {
	// KeystoneFullRestage is true when a keystone rotation/first-pin pends: the delta-skip is disabled, so
	// EVERY node will re-stage regardless of content.
	KeystoneFullRestage bool
	// Nodes is the per-enrolled-node changed/unchanged verdict (subgraph order).
	Nodes []NodeDeployChange
	// SkippedUnenrolled are topology nodes not yet enrolled (excluded from the compile).
	SkippedUnenrolled []string
}

// DeployPreview is the READ-ONLY dry-run of CompileAndStage (plan-6): it compiles + exports the PASSED
// design (the operator's current canvas — a Deploy pushes the canvas via update-topology THEN stages, so
// previewing the STORED design would misreport the blast radius whenever the canvas has unsaved edits),
// computes each enrolled node's bundle digest, and reports whether an unforced Deploy would re-stage it —
// WITHOUT staging, persisting pins, or writing the audit log. Zero-knowledge like HandleCompilePreview:
// keys come from the enrolled registry via CompileSubgraph (AgentHeld placeholders); the canvas key
// fields are ignored. It shares the digest identity (servedBundleDigest) and the keystone skip decision
// (stageSkipEnabled) with CompileAndStage, so the preview cannot disagree with the real stage of the same
// design. Force is an operator override applied at Deploy time, so the preview reports the UNFORCED
// baseline. Heal the colliding pins a save/stage would, so the previewed digests match a real Deploy.
func DeployPreview(ctx context.Context, store Store, t TenantID, topo *model.Topology) (DeployPreviewResult, error) {
	normalize.HealCollidingPins(topo)

	nodes, err := store.ListNodes(ctx, t)
	if err != nil {
		return DeployPreviewResult{}, fmt.Errorf("controller: listing nodes to preview: %w", err)
	}
	cs, err := store.GetSettings(ctx, t)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return DeployPreviewResult{}, fmt.Errorf("controller: loading settings to preview: %w", err)
	}
	fs := BuildFetchSettings(cs.WithDefaults())
	fs.AgentRolloutNodeIDs = AgentRolloutNodeIDs(cs, nodes)
	result, subgraph, skipped, err := CompileSubgraph(ctx, topo, nodes, fs)
	if err != nil {
		return DeployPreviewResult{}, err
	}
	if len(subgraph.Nodes) == 0 {
		return DeployPreviewResult{SkippedUnenrolled: skipped}, nil
	}

	keystoneOn := false
	var opCred OperatorCredential
	if cred, cerr := store.GetOperatorCredential(ctx, t); cerr == nil {
		keystoneOn = true
		opCred = cred
	} else if !errors.Is(cerr, ErrNotFound) {
		return DeployPreviewResult{}, fmt.Errorf("controller: loading operator credential to preview: %w", cerr)
	}
	skipEnabled, err := stageSkipEnabled(ctx, store, t, keystoneOn, opCred)
	if err != nil {
		return DeployPreviewResult{}, err
	}

	tmp, err := os.MkdirTemp("", "yaog-preview-")
	if err != nil {
		return DeployPreviewResult{}, fmt.Errorf("controller: creating preview temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	if _, err := artifacts.Export(result, tmp); err != nil {
		return DeployPreviewResult{}, fmt.Errorf("controller: exporting bundles to preview: %w", err)
	}

	out := DeployPreviewResult{
		KeystoneFullRestage: !skipEnabled,
		SkippedUnenrolled:   skipped,
		Nodes:               make([]NodeDeployChange, 0, len(subgraph.Nodes)),
	}
	for _, node := range subgraph.Nodes {
		files, err := readBundleDir(filepath.Join(tmp, node.Name))
		if err != nil {
			return DeployPreviewResult{}, fmt.Errorf("controller: reading bundle for node %s: %w", node.ID, err)
		}
		changed := true // fail toward "will change" (matches CompileAndStage's fail-open staging)
		if skipEnabled {
			if checks, ok := files["checksums.sha256"]; ok {
				if servedDigest, ok := servedBundleDigest(ctx, store, t, node.ID); ok && servedDigest == bundleSHA256(checks) {
					changed = false
				}
			}
		}
		out.Nodes = append(out.Nodes, NodeDeployChange{NodeID: node.ID, Name: node.Name, Changed: changed})
	}
	return out, nil
}

// appendStageAudit appends one best-effort audit entry for a stage-path action
// (stage / stage-empty / purge-staged). Best-effort by design: these audits record
// state changes that have ALREADY committed, and converting an audit-write hiccup
// into a failed stage would tell the operator the action failed when it happened
// (the same post-commit convention as the update-topology/promote audits).
func appendStageAudit(ctx context.Context, store Store, t TenantID, now time.Time, action, nodeID string) {
	_, _ = store.AppendAudit(ctx, t, AuditEntry{
		Timestamp: now,
		Actor:     "operator",
		Action:    action,
		NodeID:    nodeID,
	})
}

// PromoteStaged flips the tenant's staged bundles to current via Store.PromoteStaged,
// after enforcing the KEYSTONE gate: when an operator credential is pinned (keystone
// ON), a promote is refused unless a NON-EMPTY off-host signature exists over EXACTLY
// the staged manifest bytes AND that signature verifies against the pinned credential.
// This is the deploy-time chokepoint that makes the off-host signature mandatory: a
// breached controller can stage anything, but cannot make a node trust it without a
// signature only the off-host key can produce.
//
// Keystone OFF (no credential pinned): promote exactly as before — Store.PromoteStaged
// with no extra gate.
//
// It returns the new generation, ErrNoStagedBundle when nothing is staged, or a
// descriptive error when the keystone gate refuses.
//
// NOTE: with the keystone on this verifies the off-host SIGNATURE over the stored staged
// manifest as an early, operator-visible defense-in-depth check — it does NOT re-derive
// the staged bundles' checksums digests and compare them to the manifest's BundleSHA256
// values. The authoritative chokepoint is the AGENT, which re-derives
// hex(sha256(checksums.sha256)) offline and binds it to its signed member entry before
// applying. Do not mistake this controller gate for the trust root.
func PromoteStaged(ctx context.Context, store Store, t TenantID) (int64, error) {
	// Serialized against any concurrent stage/promote for this tenant — a promote
	// landing mid-stage would flip a partial stage set (see lockTenantOps).
	defer lockTenantOps(t)()

	cred, err := store.GetOperatorCredential(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Keystone OFF: promote as today.
			return store.PromoteStaged(ctx, t)
		}
		return 0, fmt.Errorf("controller: loading operator credential to promote: %w", err)
	}

	// Keystone ON: a valid off-host signature over the staged manifest is mandatory.
	stored, err := store.GetCurrentSignedTrustList(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, errors.New("controller: keystone is enabled but no membership manifest is staged; stage a deploy before promote")
		}
		return 0, fmt.Errorf("controller: loading staged manifest to promote: %w", err)
	}
	if len(stored.SignatureJSON) == 0 {
		return 0, errors.New("controller: the staged membership manifest is not signed off-host yet; sign it (GET /trustlist, POST /trustlist-signature) before promote")
	}

	// Verify the stored off-host signature over the staged manifest against the pinned
	// credential — exactly what a node does offline (the shared verifyStoredAgainstPin, so the
	// promote gate and the redeploy-required signal can never drift). The promote gate refuses on
	// EITHER failure kind (a corrupt record or a non-verifying signature both block a promote).
	if parseErr, verifyErr := verifyStoredAgainstPin(stored, cred); parseErr != nil || verifyErr != nil {
		blocking := verifyErr
		if blocking == nil {
			blocking = parseErr
		}
		return 0, fmt.Errorf("controller: the staged membership manifest is signed under a credential that is no longer the pinned keystone (it was likely signed before a rotation); re-sign it with the current credential (GET /trustlist, POST /trustlist-signature) before promote: %w", blocking)
	}

	return store.PromoteStaged(ctx, t)
}

// validateManualNodes rejects a stage/preview whose topology carries a MANUAL (deployment_mode=manual,
// hand-deployed, agent-less) node that is not deployable. A manual node is admitted from its
// OPERATOR-ASSERTED topology public key (no enrollment token proves it), so the controller validates
// that asserted identity here — before it is rendered into managed peers' bundles AND bound into the
// off-host-signed membership manifest:
//
//   - it MUST carry a WireGuard public key (without one, enrolledSubgraph would silently exclude it;
//     surfacing a clear error is the plan-1 deferred rule, now in its correct controller-side home —
//     the shared pre-keygen validator can't host it because a LOCAL-mode manual node legitimately has
//     no key until compile generates one);
//   - that key MUST be unique across the fleet: not duplicating another manual node's, and not
//     colliding with an enrolled node's registry key — the same one-pubkey-one-node invariant
//     CheckWGKeyUnique enforces for enrolling managed nodes, extended across the manual+enrolled split
//     so a manual node can never claim (or be confused with) an enrolled node's identity.
//
// A managed node carrying a stray deployment_mode is not affected (IsManual gates on exactly "manual").
func validateManualNodes(topo *model.Topology, nodes []Node) error {
	// Enrolled public key -> node ID, for the cross-source (manual-vs-enrolled) collision check.
	enrolledByKey := make(map[string]string, len(nodes))
	for _, n := range nodes {
		// Trim the enrolled key too (symmetry with the manual side + CheckWGKeyUnique), so a padded
		// registry key still matches a clean manual key of the same value.
		if n.Status == NodeApproved {
			if k := strings.TrimSpace(n.WGPublicKey); k != "" {
				enrolledByKey[k] = n.NodeID
			}
		}
	}
	manualByKey := make(map[string]string)
	for i := range topo.Nodes {
		node := &topo.Nodes[i]
		if !node.IsManual() {
			continue
		}
		// Identify the offending node by its stable, unique ID (a name may be empty or duplicated).
		// Whitespace-insensitive comparison, matching CheckWGKeyUnique (a padded key cannot evade the
		// gate, and would also break the rendered WG config).
		key := strings.TrimSpace(node.WireGuardPublicKey)
		if key == "" {
			return apierr.New(apierr.CodeManualNodeInvalid).With("node", node.ID).
				With("detail", "no WireGuard public key — a manual node is hand-deployed, so it must carry its own pre-known public key")
		}
		// The manual key is operator-asserted and rendered VERBATIM (the raw, untrimmed value flows to
		// the peer config), so validate the raw key: a valid Curve25519 key has no surrounding
		// whitespace, and bad base64 / wrong length / an embedded newline is rejected. Same source of
		// truth as the schema validator + enroll/rekey.
		if !validator.ValidWGPublicKey(node.WireGuardPublicKey) {
			return apierr.New(apierr.CodeManualNodeInvalid).With("node", node.ID).
				With("detail", "its WireGuard public key is not a valid base64/32-byte Curve25519 key")
		}
		if other, ok := enrolledByKey[key]; ok {
			return apierr.New(apierr.CodeManualNodeInvalid).With("node", node.ID).
				With("detail", fmt.Sprintf("its WireGuard public key collides with enrolled node %s", other))
		}
		if other, ok := manualByKey[key]; ok {
			return apierr.New(apierr.CodeManualNodeInvalid).With("node", node.ID).
				With("detail", fmt.Sprintf("its WireGuard public key duplicates manual node %s", other))
		}
		manualByKey[key] = node.ID
	}
	return nil
}

// manualKeyConflict reports a conflict when a MANUAL node in the stored topology (other than
// selfNodeID) already claims wgPubKey. It is the TOPOLOGY half of the cross-source one-pubkey-one-node
// invariant; CheckWGKeyUnique (the registry half) calls it so a node can never enroll/rekey to a key a
// manual node already holds (the enrolled→manual direction; validateManualNodes covers manual→enrolled).
// A missing topology or empty key is never a conflict. Whitespace-insensitive, matching CheckWGKeyUnique.
func manualKeyConflict(ctx context.Context, store Store, t TenantID, wgPubKey, selfNodeID string) (string, error) {
	key := strings.TrimSpace(wgPubKey)
	if key == "" {
		return "", nil
	}
	rec, err := store.GetTopology(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("controller: loading topology for manual-key dedupe: %w", err)
	}
	var topo model.Topology
	if err := json.Unmarshal(rec.JSON, &topo); err != nil {
		return "", fmt.Errorf("controller: parsing topology for manual-key dedupe: %w", err)
	}
	for i := range topo.Nodes {
		n := &topo.Nodes[i]
		if n.IsManual() && n.ID != selfNodeID && strings.TrimSpace(n.WireGuardPublicKey) == key {
			return n.ID, ErrDuplicateWGKey
		}
	}
	return "", nil
}

// enrolledSubgraph projects a stored topology down to its ready subgraph under the
// render-what's-ready policy.
//
// A topology node is "ready" (included) when its authoritative WireGuard PUBLIC key
// is known. For a MANAGED node (the default) that key comes from the enrollment
// registry — the node must be NodeApproved with a non-empty WGPublicKey, and the
// agent holds the matching private key. For a MANUAL node (deployment_mode=="manual",
// hand-deployed, no agent, never enrolls) the key comes from the TOPOLOGY itself: the
// operator registered the node's design-time WireGuardPublicKey and holds its private
// key off-controller, so the controller stays zero-knowledge — it only ever sees the
// public half. Either way the included node has WireGuardPrivateKey CLEARED — a stray
// private key from an imported topology must never reach a rendered bundle.
//
// Any edge whose FromNodeID or ToNodeID is not ready is dropped; a managed edge
// activates on a later deploy once its far end enrolls, while a manual far end is
// ready immediately from its topology key.
//
// It returns the subgraph plus the list of excluded MANAGED node IDs (skipped =
// not-yet-enrolled). Manual nodes are NEVER listed as skipped — they are intentionally
// agent-less, not "not yet ready". The input topology is never mutated (value copy).
func enrolledSubgraph(topo *model.Topology, nodes []Node) (model.Topology, []string) {
	// registry indexes the enrolled public key by node ID. A managed node is enrolled iff
	// it is NodeApproved with a non-empty WGPublicKey — the admission test.
	registry := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if n.Status == NodeApproved && n.WGPublicKey != "" {
			registry[n.NodeID] = n.WGPublicKey
		}
	}

	sub := model.Topology{
		Project:            topo.Project,
		Domains:            topo.Domains,
		RoutePolicies:      topo.RoutePolicies,
		AllocSchemaVersion: topo.AllocSchemaVersion,
	}

	// readyKey resolves a node's authoritative public key: the topology key for a manual
	// node, the enrollment registry for a managed node. Empty == not ready.
	readyKey := func(node model.Node) string {
		if node.IsManual() {
			return node.WireGuardPublicKey
		}
		return registry[node.ID]
	}

	// First pass: the set of nodes whose public key is known (enrolled-managed OR manual).
	ready := make(map[string]bool, len(topo.Nodes))
	for _, node := range topo.Nodes {
		if readyKey(node) != "" {
			ready[node.ID] = true
		}
	}

	// Render-what's-ready for the client role. A client requires EXACTLY ONE enabled
	// outbound edge (compiler validateClientEdges is a HARD error otherwise), so a ready
	// client whose dial target is not yet ready would be left edgeless and fail the whole
	// stage. Treat such a client as itself not-ready: exclude it now and let it activate
	// on a later deploy once its router/relay/gateway is ready.
	for _, node := range topo.Nodes {
		if ready[node.ID] && node.Role == "client" && !clientTargetEnrolled(topo, node.ID, ready) {
			delete(ready, node.ID)
		}
	}

	var skipped []string
	for _, node := range topo.Nodes { // value copy: never mutate the caller's slice
		if !ready[node.ID] {
			// A not-ready MANAGED node is "skipped" (waiting to enroll). A not-ready manual
			// node would mean a missing topology pubkey — a design error the controller-registration
			// validator will reject (plan-2); until then this branch defensively excludes it. Either
			// way a manual node is never reported as a transient enrollment skip.
			if !node.IsManual() {
				skipped = append(skipped, node.ID)
			}
			continue
		}
		node.WireGuardPublicKey = readyKey(node)
		node.WireGuardPrivateKey = ""
		sub.Nodes = append(sub.Nodes, node)
	}

	// Drop any edge whose far end is not ready: it activates on a later deploy.
	for _, edge := range topo.Edges {
		if ready[edge.FromNodeID] && ready[edge.ToNodeID] {
			sub.Edges = append(sub.Edges, edge)
		}
	}

	return sub, skipped
}

// clientTargetEnrolled reports whether a client node has an enabled outbound edge
// whose dial target is enrolled — the readiness condition for compiling the client
// (a client must have exactly one enabled outbound edge).
func clientTargetEnrolled(topo *model.Topology, clientID string, enrolled map[string]bool) bool {
	for _, e := range topo.Edges {
		if e.FromNodeID == clientID && e.IsEnabled && enrolled[e.ToNodeID] {
			return true
		}
	}
	return false
}

// persistAllocations merges the allocation pins the compiler stamped onto the
// compiled subgraph back into the FULL stored topology, then re-stores it. It copies
// per-node OverlayIP and the per-edge pin set (transit IPs, link-locals, ports,
// CompiledPort) by ID — never any key material, so the stored topology stays
// public-keys-only — and stamps AllocSchemaVersion. The next CompileAndStage then
// finds these pins in the stored topology and the compiler reuses them (sticky-pin),
// which is what keeps allocations stable across incremental enrollment (I10).
//
// Note (plan-2): a PutTopology write-back that CHANGES the stored topology counts
// as a retained version like any other — the pinned post-stage shape is itself a
// state an operator may want to recover. A write-back whose bytes equal the stored
// record (sticky pins re-derived identically, the common re-stage case) is SKIPPED:
// burning one of the bounded history slots per no-op stage would let routine
// incremental-enrollment staging flush every operator-authored version out of the
// recovery window (review finding, D7).
func persistAllocations(ctx context.Context, store Store, t TenantID, full, compiled *model.Topology, originalJSON []byte) error {
	ipByID := make(map[string]string, len(compiled.Nodes))
	for _, n := range compiled.Nodes {
		ipByID[n.ID] = n.OverlayIP
	}
	edgeByID := make(map[string]model.Edge, len(compiled.Edges))
	for _, e := range compiled.Edges {
		edgeByID[e.ID] = e
	}

	for i := range full.Nodes {
		if ip, ok := ipByID[full.Nodes[i].ID]; ok && ip != "" {
			full.Nodes[i].OverlayIP = ip
		}
	}
	for i := range full.Edges {
		c, ok := edgeByID[full.Edges[i].ID]
		if !ok {
			continue // edge not in the compiled subgraph (far end unenrolled) — leave unpinned
		}
		full.Edges[i].CompiledPort = c.CompiledPort
		full.Edges[i].PinnedFromPort = c.PinnedFromPort
		full.Edges[i].PinnedToPort = c.PinnedToPort
		full.Edges[i].PinnedFromTransitIP = c.PinnedFromTransitIP
		full.Edges[i].PinnedToTransitIP = c.PinnedToTransitIP
		full.Edges[i].PinnedFromLinkLocal = c.PinnedFromLinkLocal
		full.Edges[i].PinnedToLinkLocal = c.PinnedToLinkLocal
	}
	full.AllocSchemaVersion = compiled.AllocSchemaVersion

	raw, err := json.Marshal(full)
	if err != nil {
		return fmt.Errorf("controller: marshaling topology with persisted allocations: %w", err)
	}
	// No-op write-back: the stored record is canonical json.Marshal output (the
	// update-topology custody gate canonicalizes), so byte equality here means the
	// pins changed nothing. Skip the put — do not burn a history slot.
	if bytes.Equal(raw, originalJSON) {
		return nil
	}
	if _, err := store.PutTopology(ctx, t, raw); err != nil {
		return fmt.Errorf("controller: persisting allocations: %w", err)
	}
	return nil
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
