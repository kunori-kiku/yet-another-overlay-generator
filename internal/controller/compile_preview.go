package controller

// compile_preview.go — DeployPreview (the read-only Deploy dry-run) and its result types.
// Split from compile.go (plan-2); no logic change.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/artifacts"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/normalize"
)

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
