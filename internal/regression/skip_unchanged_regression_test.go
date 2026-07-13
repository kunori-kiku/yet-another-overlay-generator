package regression

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// skip_unchanged_regression_test.go proves the plan-5 DELTA-SKIP end to end against the REAL
// CompileAndStage / PromoteStaged / keystone surfaces: an unchanged node is not re-staged (its
// generation and served bundle stay put), a changed node is, and the keystone seam holds — a rotation
// or first-pin FORCES a full re-stage so the promote can flip the served trust-list (the review blocker).

func (e *regEnv) putTopo(topo *model.Topology) {
	e.t.Helper()
	raw, err := json.Marshal(topo)
	if err != nil {
		e.t.Fatalf("marshal topo: %v", err)
	}
	if _, err := e.store.PutTopology(e.ctx, tenant, raw); err != nil {
		e.t.Fatalf("PutTopology: %v", err)
	}
}

// bundleOnly strips the served trust-list artifacts, leaving just the node's bundle files (which the
// delta-skip leaves byte-identical for an unchanged node; only the trust-list is regenerated).
func bundleOnly(files map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for k, v := range files {
		if k == "trustlist.json" || k == "trustlist.sig" {
			continue
		}
		out[k] = v
	}
	return out
}

func sameBundle(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if bv, ok := b[k]; !ok || !bytes.Equal(av, bv) {
			return false
		}
	}
	return true
}

// (a) A re-deploy of an UNCHANGED topology stages ZERO nodes (all skipped), promote has nothing to flip,
// and every served bundle still verifies.
func TestRegression_SkipUnchanged_ZeroRestage(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	ks := newKeystone(t)
	e.pinKeystone(ks)
	e.deploy(ks)

	res, err := controller.CompileAndStage(e.ctx, e.store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage (unchanged): %v", err)
	}
	if len(res.Staged) != 0 {
		t.Fatalf("an unchanged re-deploy must stage 0 nodes, staged %v", res.Staged)
	}
	if len(res.UnchangedNodeIDs) != 2 {
		t.Fatalf("an unchanged re-deploy must report 2 unchanged, got %v", res.UnchangedNodeIDs)
	}
	if _, err := controller.PromoteStaged(e.ctx, e.store, tenant); !errors.Is(err, controller.ErrNoStagedBundle) {
		t.Fatalf("promote after a zero-changed stage must be ErrNoStagedBundle, got %v", err)
	}
	for _, id := range []string{"node-1", "node-2"} {
		if _, err := verifyAsNode(e.served(id), id, ks.pubPEM, 0); err != nil {
			t.Fatalf("served %s must still verify after a zero-changed deploy: %v", id, err)
		}
	}
}

// (b) Changing ONE node's content re-stages exactly that node; the OTHER node is skipped, its served
// bundle stays byte-identical (only the trust-list is regenerated), and it still verifies against the
// new trust-list.
func TestRegression_SkipUnchanged_OneNodeChanged(t *testing.T) {
	topo := twoNodeTopo()
	e := newRegEnv(t, topo, "node-1", "node-2")
	ks := newKeystone(t)
	e.pinKeystone(ks)
	e.deploy(ks)

	// node-2's served bundle BEFORE the change (excluding the trust-list).
	before := bundleOnly(e.served("node-2"))

	// Change ONLY node-1 (the router): add an extra announced prefix. That alters node-1's babel config
	// (its bundle) but NOT node-2's rendered bundle (the route is learned at runtime via babel).
	topo.Nodes[0].ExtraPrefixes = []string{"10.99.0.0/24"}
	e.putTopo(topo)

	res, err := controller.CompileAndStage(e.ctx, e.store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage (one changed): %v", err)
	}
	if len(res.Staged) != 1 || res.Staged[0] != "node-1" {
		t.Fatalf("only node-1 should re-stage, got staged=%v", res.Staged)
	}
	if len(res.UnchangedNodeIDs) != 1 || res.UnchangedNodeIDs[0] != "node-2" {
		t.Fatalf("node-2 should be skipped, got unchanged=%v", res.UnchangedNodeIDs)
	}
	e.signStaged(ks)
	if _, err := controller.PromoteStaged(e.ctx, e.store, tenant); err != nil {
		t.Fatalf("PromoteStaged (one changed): %v", err)
	}

	// node-2's served bundle is byte-identical; only its trust-list changed; it still verifies.
	if after := bundleOnly(e.served("node-2")); !sameBundle(before, after) {
		t.Fatalf("skipped node-2's served bundle must be byte-identical across a peer's re-stage")
	}
	if _, err := verifyAsNode(e.served("node-2"), "node-2", ks.pubPEM, 0); err != nil {
		t.Fatalf("skipped node-2 must still verify against the regenerated trust-list: %v", err)
	}
}

// (e-rotation) A keystone ROTATION with an otherwise-unchanged topology must DISABLE the skip (full
// re-stage) so the promote flips the served trust-list under the new key and KeystoneRedeployRequired
// clears — the review blocker (a skipped deploy would strand the fleet on the rotated-away credential).
func TestRegression_SkipDisabled_OnKeystoneRotation(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")
	ks1 := newKeystone(t)
	e.pinKeystone(ks1)
	e.deploy(ks1)

	// Rotate: pin a new credential. The served trust-list is now signed under the rotated-away ks1.
	ks2 := newKeystone(t)
	e.pinKeystone(ks2)
	cred, _ := e.store.GetOperatorCredential(e.ctx, tenant)
	if req, err := controller.KeystoneRedeployRequired(e.ctx, e.store, tenant, cred); err != nil || !req {
		t.Fatalf("post-rotation KeystoneRedeployRequired must be true (got req=%v err=%v)", req, err)
	}

	res, err := controller.CompileAndStage(e.ctx, e.store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage (rotation): %v", err)
	}
	if len(res.Staged) != 2 || len(res.UnchangedNodeIDs) != 0 {
		t.Fatalf("a keystone rotation must FORCE a full re-stage (2 staged, 0 unchanged), got staged=%v unchanged=%v", res.Staged, res.UnchangedNodeIDs)
	}
	e.signStaged(ks2)
	if _, err := controller.PromoteStaged(e.ctx, e.store, tenant); err != nil {
		t.Fatalf("PromoteStaged (rotation restage): %v", err)
	}
	cred2, _ := e.store.GetOperatorCredential(e.ctx, tenant)
	if req, err := controller.KeystoneRedeployRequired(e.ctx, e.store, tenant, cred2); err != nil || req {
		t.Fatalf("after the rotation redeploy, KeystoneRedeployRequired must clear (got req=%v err=%v)", req, err)
	}
	for _, id := range []string{"node-1", "node-2"} {
		if _, err := verifyAsNode(e.served(id), id, ks2.pubPEM, 0); err != nil {
			t.Fatalf("served %s must verify under the rotated key ks2: %v", id, err)
		}
	}
}

// (e-first-pin) Enabling keystone on an already-deployed (non-keystone) fleet must DISABLE the skip so
// the first keystone deploy stages every node and the promote SEEDS the served trust-list — even though
// the bundle content is unchanged (so the digest-only skip would otherwise skip everything and seed
// nothing).
func TestRegression_SkipDisabled_OnKeystoneFirstPin(t *testing.T) {
	e := newRegEnv(t, twoNodeTopo(), "node-1", "node-2")

	// A non-keystone deploy: both nodes get SERVED bundles, but there is NO served trust-list.
	if _, err := controller.CompileAndStage(e.ctx, e.store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage (non-keystone): %v", err)
	}
	if _, err := controller.PromoteStaged(e.ctx, e.store, tenant); err != nil {
		t.Fatalf("PromoteStaged (non-keystone): %v", err)
	}

	// First-pin the keystone. Content is unchanged, but with no served trust-list the skip MUST be
	// disabled so the promote seeds the served slot.
	ks := newKeystone(t)
	e.pinKeystone(ks)
	res, err := controller.CompileAndStage(e.ctx, e.store, tenant, time.Now())
	if err != nil {
		t.Fatalf("CompileAndStage (first-pin): %v", err)
	}
	if len(res.Staged) != 2 || len(res.UnchangedNodeIDs) != 0 {
		t.Fatalf("keystone first-pin must FORCE a full re-stage despite unchanged content, got staged=%v unchanged=%v", res.Staged, res.UnchangedNodeIDs)
	}
	e.signStaged(ks)
	if _, err := controller.PromoteStaged(e.ctx, e.store, tenant); err != nil {
		t.Fatalf("PromoteStaged (first-pin): %v", err)
	}
	for _, id := range []string{"node-1", "node-2"} {
		if _, err := verifyAsNode(e.served(id), id, ks.pubPEM, 0); err != nil {
			t.Fatalf("served %s must verify after first-pin seeds the trust-list: %v", id, err)
		}
	}
}
