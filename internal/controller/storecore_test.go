package controller

// storecore_test.go — the PERPETUAL storecore invariant set (plan-8): one focused test per custody/
// allocation rule the behavioral core (storecore.go) now authors ONCE. These exercise the core directly
// over the fast in-memory backend (NewMemStore); the cross-impl compat suite (store_compat_test.go) and
// the dedicated per-domain files re-run the same core over BOTH backends for storage conformance, and
// filestore_durability_test.go + the M1/M2/M3 overlay characterizations pin the filekv-specific and
// volatility properties. Together they are the net guarding the fleet-stranding-class invariants.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
)

// INVARIANT 1 — promote generation-scoping (rule 1). Only staged bundles whose provisional Generation
// equals the generation being promoted (current+1) flip; a provisional invalidated by an interleaved
// BumpGeneration is stale and stays staged; a promote that matches nothing returns ErrNoStagedBundle and
// advances NOTHING (the counter is not bumped); a re-stage refreshes it.
func TestStoreCore_PromoteScoping(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	// Stage + promote gen 1.
	if err := s.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 1, IsStaged: true, Files: map[string][]byte{"x": []byte("v1")}}); err != nil {
		t.Fatalf("StageBundle v1: %v", err)
	}
	if gen, err := s.PromoteStaged(ctx, tenant); err != nil || gen != 1 {
		t.Fatalf("PromoteStaged #1 = (%d,%v), want (1,nil)", gen, err)
	}

	// Stage gen 2, then BUMP (→ gen 2). The staged provisional (2) is now stale: the next promote targets
	// current+1 = 3, so it must NOT flip.
	if err := s.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 2, IsStaged: true, Files: map[string][]byte{"x": []byte("v2")}}); err != nil {
		t.Fatalf("StageBundle v2: %v", err)
	}
	if gen, err := s.BumpGeneration(ctx, tenant); err != nil || gen != 2 {
		t.Fatalf("BumpGeneration = (%d,%v), want (2,nil)", gen, err)
	}
	if _, err := s.PromoteStaged(ctx, tenant); !errors.Is(err, ErrNoStagedBundle) {
		t.Fatalf("PromoteStaged(stale provisional) = %v, want ErrNoStagedBundle", err)
	}
	// Counter unchanged (a no-flip promote advances nothing), and the current bundle is still gen 1.
	if gen, err := s.CurrentGeneration(ctx, tenant); err != nil || gen != 2 {
		t.Fatalf("CurrentGeneration after no-flip promote = (%d,%v), want (2,nil) — counter must not advance", gen, err)
	}
	if b, err := s.GetCurrentBundle(ctx, tenant, "alpha"); err != nil || b.Generation != 1 {
		t.Fatalf("current bundle after no-flip promote = gen %d (%v), want the UNCHANGED gen 1", b.Generation, err)
	}

	// Re-stage at the correct provisional generation (3) → it flips.
	if err := s.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 3, IsStaged: true, Files: map[string][]byte{"x": []byte("v3")}}); err != nil {
		t.Fatalf("StageBundle v3: %v", err)
	}
	if gen, err := s.PromoteStaged(ctx, tenant); err != nil || gen != 3 {
		t.Fatalf("PromoteStaged(refreshed) = (%d,%v), want (3,nil)", gen, err)
	}
	if b, err := s.GetCurrentBundle(ctx, tenant, "alpha"); err != nil || b.Generation != 3 || string(b.Files["x"]) != "v3" {
		t.Fatalf("current bundle after refresh = gen %d content %q (%v), want gen 3 / v3", b.Generation, b.Files["x"], err)
	}
}

// INVARIANT 2 — served-vs-staged keystone predicate + atomic snapshot (rules 2, 3). PromoteStaged copies
// the staged trust-list into the SERVED slot ATOMICALLY with the bundle flip ONLY when it is signed; an
// unsigned/absent staged manifest leaves the served slot intact; GetServedConfig reads the {bundle,
// keystone-on, served-TL} triple under one lock.
func TestStoreCore_ServedVsStaged(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if err := s.SetOperatorCredential(ctx, tenant, OperatorCredential{Alg: "ed25519", PublicKeyPEM: "pub"}); err != nil {
		t.Fatalf("SetOperatorCredential: %v", err)
	}

	// Signed staged manifest → promoted to the served slot.
	if err := s.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 1, IsStaged: true, Files: map[string][]byte{"checksums.sha256": []byte("s1")}}); err != nil {
		t.Fatalf("StageBundle: %v", err)
	}
	signed := StoredTrustList{TrustListJSON: []byte(`{"epoch":1}` + "\n"), SignatureJSON: []byte(`{"sig":"a"}`), Epoch: 1}
	if err := s.PutSignedTrustList(ctx, tenant, signed); err != nil {
		t.Fatalf("PutSignedTrustList: %v", err)
	}
	if _, err := s.PromoteStaged(ctx, tenant); err != nil {
		t.Fatalf("PromoteStaged: %v", err)
	}
	sc, err := s.GetServedConfig(ctx, tenant, "alpha")
	if err != nil {
		t.Fatalf("GetServedConfig: %v", err)
	}
	if !sc.KeystoneOn || !sc.HasTrustList || sc.Bundle.Generation != 1 || sc.TrustList.Epoch != 1 {
		t.Fatalf("served snapshot = %+v, want KeystoneOn/HasTrustList/gen1/epoch1", sc)
	}

	// An UNSIGNED re-stage (no promote) must NOT touch the served slot.
	if err := s.PutSignedTrustList(ctx, tenant, StoredTrustList{TrustListJSON: []byte(`{"epoch":2}` + "\n"), Epoch: 2}); err != nil {
		t.Fatalf("re-stage unsigned: %v", err)
	}
	if served, err := s.GetServedTrustList(ctx, tenant); err != nil || served.Epoch != 1 {
		t.Fatalf("served after unsigned re-stage = epoch %d (%v), want frozen at 1", served.Epoch, err)
	}

	// Promoting an UNSIGNED staged manifest leaves the served slot intact (fail-closed): the served epoch
	// stays 1 even though the bundle advances.
	if err := s.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 2, IsStaged: true, Files: map[string][]byte{"checksums.sha256": []byte("s2")}}); err != nil {
		t.Fatalf("StageBundle v2: %v", err)
	}
	if _, err := s.PromoteStaged(ctx, tenant); err != nil {
		t.Fatalf("PromoteStaged unsigned: %v", err)
	}
	sc2, err := s.GetServedConfig(ctx, tenant, "alpha")
	if err != nil {
		t.Fatalf("GetServedConfig #2: %v", err)
	}
	if sc2.Bundle.Generation != 2 || sc2.TrustList.Epoch != 1 {
		t.Fatalf("after unsigned promote: bundle gen %d served epoch %d, want gen2 / served epoch1 (unsigned never copied)", sc2.Bundle.Generation, sc2.TrustList.Epoch)
	}
}

// INVARIANT 3 — monotonic anti-rollback (rules 4, 5). The generation counter and the served trust-list
// only ADVANCE (newGen = current+1); there is no regress path in the store surface.
func TestStoreCore_AntiRollback(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if err := s.SetOperatorCredential(ctx, tenant, OperatorCredential{Alg: "ed25519", PublicKeyPEM: "pub"}); err != nil {
		t.Fatalf("SetOperatorCredential: %v", err)
	}

	promoteSigned := func(gen int64, epoch int64) {
		t.Helper()
		if err := s.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: gen, IsStaged: true, Files: map[string][]byte{"x": []byte("v")}}); err != nil {
			t.Fatalf("StageBundle gen%d: %v", gen, err)
		}
		if err := s.PutSignedTrustList(ctx, tenant, StoredTrustList{TrustListJSON: []byte(`{}`), SignatureJSON: []byte(`{"sig":"s"}`), Epoch: epoch}); err != nil {
			t.Fatalf("PutSignedTrustList epoch%d: %v", epoch, err)
		}
		if g, err := s.PromoteStaged(ctx, tenant); err != nil || g != gen {
			t.Fatalf("PromoteStaged gen%d = (%d,%v)", gen, g, err)
		}
	}

	promoteSigned(1, 1)
	promoteSigned(2, 2)
	if g, err := s.CurrentGeneration(ctx, tenant); err != nil || g != 2 {
		t.Fatalf("generation = (%d,%v), want strictly-advanced 2", g, err)
	}
	if served, err := s.GetServedTrustList(ctx, tenant); err != nil || served.Epoch != 2 {
		t.Fatalf("served epoch = %d (%v), want advanced to 2 (never regresses)", served.Epoch, err)
	}
	// A BumpGeneration only advances the counter (no bundle flip); it can never lower it.
	if g, err := s.BumpGeneration(ctx, tenant); err != nil || g != 3 {
		t.Fatalf("BumpGeneration = (%d,%v), want 3 (monotonic advance)", g, err)
	}
}

// INVARIANT 4 — node API-token rotation (rule 8). Issuing a fresh token for a node that already has one
// invalidates the OLD token at the lookup chokepoint (the stale reverse-index entry is dropped and the
// lookup requires the node's current hash to match).
func TestStoreCore_TokenRotation(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	a, b := tokenHash("A"), tokenHash("B")
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if err := s.IssueNodeAPIToken(ctx, tenant, "alpha", a); err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	if n, err := s.LookupNodeByAPIToken(ctx, tenant, a); err != nil || n.NodeID != "alpha" {
		t.Fatalf("Lookup A = (%+v,%v), want alpha", n, err)
	}
	if err := s.IssueNodeAPIToken(ctx, tenant, "alpha", b); err != nil {
		t.Fatalf("Issue B (rotate): %v", err)
	}
	if _, err := s.LookupNodeByAPIToken(ctx, tenant, a); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("Lookup A after rotation = %v, want ErrTokenInvalid", err)
	}
	if n, err := s.LookupNodeByAPIToken(ctx, tenant, b); err != nil || n.APITokenHash != b {
		t.Fatalf("Lookup B = (%+v,%v), want alpha with hash B", n, err)
	}
}

// INVARIANT 5 — TOTP watermark monotonic CAS (rule 6). A strictly-greater step advances (returns true and
// persists); an equal or older step is refused WITHOUT writing (returns false); an absent operator is
// ErrNotFound. (Concurrent single-winner is exercised under -race in totp_store_test.go.)
func TestStoreCore_TOTPWatermark(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.PutOperator(ctx, tenant, Operator{Username: "admin", PasswordHash: "x"}); err != nil {
		t.Fatalf("PutOperator: %v", err)
	}
	if ok, err := s.AdvanceTOTPStep(ctx, tenant, "admin", 100); err != nil || !ok {
		t.Fatalf("advance 100 = (%v,%v), want true,nil", ok, err)
	}
	if ok, err := s.AdvanceTOTPStep(ctx, tenant, "admin", 100); err != nil || ok {
		t.Fatalf("re-advance 100 = (%v,%v), want false,nil (replay refused)", ok, err)
	}
	if ok, err := s.AdvanceTOTPStep(ctx, tenant, "admin", 99); err != nil || ok {
		t.Fatalf("advance 99 = (%v,%v), want false,nil (older refused)", ok, err)
	}
	if ok, err := s.AdvanceTOTPStep(ctx, tenant, "admin", 101); err != nil || !ok {
		t.Fatalf("advance 101 = (%v,%v), want true,nil", ok, err)
	}
	if _, err := s.AdvanceTOTPStep(ctx, tenant, "ghost", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("advance(absent) = %v, want ErrNotFound", err)
	}
}

// INVARIANT 6 — telemetry overlay (rule 9). A heartbeat writes ONLY the four observability fields
// (Conditions/Telemetry/LastAgentVersion/LastSeen), leaves every deploy-custody field untouched, keeps
// the stored version on an empty-version beat, clears conditions/metrics on a nil beat, and returns
// ErrNotFound for an absent node. (Volatility + the monotonic gate are pinned by M1/M2/M3.)
func TestStoreCore_TelemetryOverlay(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved, AppliedGeneration: 5, LastChecksum: "csum-5", LastHealth: "applied"}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if err := s.RecordTelemetry(ctx, tenant, "ghost", nil, nil, "", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RecordTelemetry(absent) = %v, want ErrNotFound", err)
	}

	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	conds := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Reason: "AllPeersUp"}}
	metrics := map[string]json.RawMessage{"resource": json.RawMessage(`{"load1":0.5}`)}
	if err := s.RecordTelemetry(ctx, tenant, "alpha", conds, metrics, "v-new", at); err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}
	got, err := s.GetNode(ctx, tenant, "alpha")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(got.Conditions) != 1 || got.Conditions[0].Reason != "AllPeersUp" || !got.Conditions[0].ObservedAt.Equal(at) {
		t.Fatalf("Conditions = %+v, want the AllPeersUp set server-stamped at %v", got.Conditions, at)
	}
	if string(got.Telemetry["resource"]) != `{"load1":0.5}` || got.LastAgentVersion != "v-new" || !got.LastSeen.Equal(at) {
		t.Fatalf("observability not merged: telemetry=%v version=%q lastSeen=%v", got.Telemetry, got.LastAgentVersion, got.LastSeen)
	}
	// Custody untouched by the heartbeat.
	if got.AppliedGeneration != 5 || got.LastChecksum != "csum-5" || got.LastHealth != "applied" {
		t.Fatalf("telemetry disturbed a custody field: %+v", got)
	}

	// An empty-version, nil-conditions, nil-metrics beat keeps the stored version and CLEARS conditions +
	// metrics (the latest report is the truth).
	if err := s.RecordTelemetry(ctx, tenant, "alpha", nil, nil, "", at.Add(time.Minute)); err != nil {
		t.Fatalf("RecordTelemetry(nil): %v", err)
	}
	got, _ = s.GetNode(ctx, tenant, "alpha")
	if got.Conditions != nil || got.Telemetry != nil {
		t.Fatalf("nil beat must clear conditions+metrics: conds=%+v telemetry=%v", got.Conditions, got.Telemetry)
	}
	if got.LastAgentVersion != "v-new" {
		t.Fatalf("empty-version beat must keep the stored version: got %q, want v-new", got.LastAgentVersion)
	}
	if got.AppliedGeneration != 5 {
		t.Fatalf("custody still untouched: AppliedGeneration = %d, want 5", got.AppliedGeneration)
	}
}
