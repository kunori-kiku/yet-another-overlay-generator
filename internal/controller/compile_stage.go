package controller

// compile_stage.go — CompileAndStage (compile the enrolled subgraph, export, and stage
// per-node bundles at the next generation) plus its force-option machinery and the
// stage-path audit helper. Split from compile.go (plan-2).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
)

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
//  5. Read every enrolled node's exported dir into memory and select the exact changed set.
//  6. KEYSTONE ON: compute every ready node's bundle digest (including unchanged nodes
//     retained by delta-skip) and assemble the unsigned monotonic-epoch manifest.
//  7. ReplaceStagedSet writes/prunes all candidate records and publishes its durable seal LAST.
//  8. Append one "stage" audit entry.
//
// Bundles are signed iff YAOG_BUNDLE_SIGNING_KEY is set. The key is resolved once per
// stage and the same ConfigSigner is threaded through render, signing-anchor enforcement,
// and artifacts.ExportWithSigner so a mid-stage key-file change cannot split trust.
// StageOption configures a CompileAndStage run. The only knob today is FORCE — re-staging a node even
// when its bundle digest is unchanged (the operator escape hatch for on-host drift/rescue; the delta-skip
// otherwise leaves an unchanged node alone).
type StageOption func(*stageConfig)

type stageConfig struct {
	forceAll            bool
	forceNodes          map[string]bool
	telemetryPolicyMode TelemetryPolicyDeployMode
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

func WithTelemetryPolicyDeployMode(mode TelemetryPolicyDeployMode) StageOption {
	return func(c *stageConfig) { c.telemetryPolicyMode = mode }
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
	if err := reconcileKeystoneTrustBoundaryLocked(ctx, store, t); err != nil {
		return StageResult{}, err
	}

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
	// (AgentHeld keys → compile → render) via the shared CompileSubgraph helper. Readiness is
	// preflighted below so the empty cleanup path runs before any signing-key dependency.
	nodes, err := store.ListNodes(ctx, t)
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: listing nodes to stage: %w", err)
	}
	deploymentTopo, telemetryPolicyOmitted, err := PrepareTelemetryPolicyDeployment(&topo, nodes, cfg.telemetryPolicyMode)
	if err != nil {
		return StageResult{}, err
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
	// Project readiness before resolving the signer. An empty stage is a destructive cleanup
	// operation: it must purge any superseded staged bundles even if an unrelated configured
	// signing-key file is currently broken/unreadable.
	ready, skipped, err := projectEnrolledSubgraph(deploymentTopo, nodes)
	if err != nil {
		return StageResult{}, err
	}
	if len(ready.Nodes) == 0 {
		purged, err := store.ReplaceStagedSet(ctx, t, StagedSet{})
		if err != nil {
			return StageResult{}, fmt.Errorf("controller: clearing staged set on empty stage: %w", err)
		}
		for _, nodeID := range purged {
			appendStageAudit(ctx, store, t, now, "purge-staged", nodeID)
		}
		appendStageAudit(ctx, store, t, now, "stage-empty", "")
		return StageResult{SkippedUnenrolled: skipped, TelemetryPolicyOmittedNodeIDs: telemetryPolicyOmitted}, nil
	}
	// Resolve the signer exactly once for this tenant-serialized stage. The same in-memory object
	// feeds render, signing-anchor reconciliation, and export, so a key-file change mid-stage cannot
	// produce an install script, pinned anchor, and signature from different keys.
	signer, err := bundlesig.LoadConfigSignerFromEnv()
	if err != nil {
		return StageResult{}, fmt.Errorf("controller: loading bundle signing key: %w", err)
	}
	result, subgraph, skipped, err := CompileSubgraphWithSigner(ctx, deploymentTopo, nodes, fs, signer)
	if err != nil {
		return StageResult{}, err
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
	// A probe policy authorizes outbound traffic from the node, so it must be keystone-bound.
	// Refuse before allocation write-back, signing-anchor mutation, export, or staged-set mutation.
	if err := requireTelemetryProbeKeystone(result, keystoneOn); err != nil {
		return StageResult{}, err
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
	// actual signing happens in artifacts.ExportWithSigner using this same signer.)
	if err := enforceSigningAnchorWithSigner(ctx, store, t, now, signer); err != nil {
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
	if _, err := artifacts.ExportWithSigner(result, tmp, signer); err != nil {
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
	var stagedBundles []SignedBundle
	var unchanged []string
	digests := make(map[string]string, len(subgraph.Nodes)) // nodeID -> bundleSHA256 (ALL nodes, for the manifest)
	pubKeys := make(map[string]string, len(subgraph.Nodes)) // nodeID -> wg public key (stamped on the subgraph node: enrollment registry for a managed node, topology for a manual node)
	for _, node := range subgraph.Nodes {
		nodeDir := filepath.Join(tmp, node.ID)
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
		// (plan-5) SKIP a node whose freshly compiled bundle is byte-identical to its SERVED bundle: it
		// is absent from the replacement candidate, so promote never bumps its DesiredGeneration and its
		// agent never re-applies.
		// FAIL OPEN — any doubt (skip disabled, no digest, no served bundle, unreadable served checksums,
		// or a mismatch) stages normally; never leave a node on a stale config.
		if skipEnabled && !cfg.forced(node.ID) && newDigest != "" {
			if servedDigest, ok := servedBundleDigest(ctx, store, t, node.ID); ok && servedDigest == newDigest {
				unchanged = append(unchanged, node.ID)
				continue
			}
		}
		stagedBundles = append(stagedBundles, SignedBundle{
			NodeID:     node.ID,
			Generation: nextGen,
			Files:      files,
			IsStaged:   true,
		})
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
		purged, replaceErr := store.ReplaceStagedSet(ctx, t, StagedSet{})
		for _, nodeID := range purged {
			appendStageAudit(ctx, store, t, now, "purge-staged", nodeID)
		}
		if replaceErr != nil {
			return StageResult{}, fmt.Errorf("controller: clearing staged set on a zero-changed stage: %w", replaceErr)
		}
		appendStageAudit(ctx, store, t, now, "stage-unchanged", "")
		return StageResult{
			UnchangedNodeIDs:              unchanged,
			SkippedUnenrolled:             skipped,
			TelemetryPolicyOmittedNodeIDs: telemetryPolicyOmitted,
			Generation:                    cur, // unchanged — no new generation
		}, nil
	}

	// (6) KEYSTONE ON: build the off-host-signable manifest binding every ready node's
	// bundle digest, including nodes retained at their served generation by delta-skip,
	// without mutating the Store yet.
	var stagedManifest *StoredTrustList
	if keystoneOn {
		manifest, err := buildStagedManifest(ctx, store, t, digests, pubKeys)
		if err != nil {
			return StageResult{}, err
		}
		stagedManifest = &manifest
	}

	// (7) Publish the exact candidate in one store operation. FileStore writes the seal LAST, after
	// every bundle/prune/manifest mutation; any partial write after a crash remains unpromotable.
	purged, replaceErr := store.ReplaceStagedSet(ctx, t, StagedSet{
		Generation: nextGen,
		Bundles:    stagedBundles,
		TrustList:  stagedManifest,
	})
	for _, nodeID := range purged {
		appendStageAudit(ctx, store, t, now, "purge-staged", nodeID)
	}
	if replaceErr != nil {
		return StageResult{}, fmt.Errorf("controller: replacing staged set: %w", replaceErr)
	}

	// (8) One audit entry for the whole stage operation. Post-commit (the bundles
	// are staged), so best-effort like the other stage-path audits.
	appendStageAudit(ctx, store, t, now, "stage", "")

	return StageResult{
		Staged:                        staged,
		UnchangedNodeIDs:              unchanged,
		SkippedUnenrolled:             skipped,
		TelemetryPolicyOmittedNodeIDs: telemetryPolicyOmitted,
		Generation:                    nextGen,
	}, nil
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
