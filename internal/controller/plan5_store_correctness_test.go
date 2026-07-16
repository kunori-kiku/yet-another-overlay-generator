package controller

// plan5_store_correctness_test.go — the plan-5 controller-store custody regressions:
//
//	(A) sign-vs-restage serialization: an off-host signature over manifest M1 is REJECTED once a
//	    re-stage has moved the staged manifest to M2, so a signed (M_old, B_new) pair can never promote
//	    (InstallTrustListSignature — the read-verify-write is now atomic under lockTenantOps).
//	(C) durable-only read: a GetNode→UpsertNode read-modify-write no longer bakes the volatile
//	    telemetry overlay into the durable record — the writeback reads via GetNodeRecord.
//	(D) writeJSONL bookkeeping: an appended batch is written exactly once (never duplicated), and the
//	    fileLines running count tracks the on-disk lines — the property the close-after-write log-and-
//	    continue fix preserves (requeuing an already-written batch is what would duplicate).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/bundlesig"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/runtimecontract"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/trustlist"
)

// signCanonical parses a stored manifest's canonical bytes and signs the manifest off-host with the
// test signer, returning the detached SignedTrustList — the shape POST /trustlist-signature submits.
func signCanonical(t *testing.T, signer *trustlist.Ed25519Signer, canonical []byte) trustlist.SignedTrustList {
	t.Helper()
	var manifest trustlist.TrustList
	if err := json.Unmarshal(canonical, &manifest); err != nil {
		t.Fatalf("unmarshal canonical manifest: %v", err)
	}
	signed, err := signer.Sign(manifest)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return signed
}

// TestInstallTrustListSignature_RejectsStaleAfterRestage is the PART A custody regression. It stages
// manifest M1, captures + signs it off-host, then RE-STAGES (a membership change → M2). Installing the
// M1 signature must now be rejected by the substitution guard (ErrStagedManifestMismatch), the staged
// manifest must remain UNSIGNED (the stale signature was not welded onto B_new), and promote must still
// refuse — so a desynced (signed M_old, staged B_new) pair can never reach the fleet. Signing the
// CURRENT bytes (M2) then promotes cleanly.
func TestInstallTrustListSignature_RejectsStaleAfterRestage(t *testing.T) {
	store := NewMemStore()
	ctx := putNoClientTopo(t, store)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := store.CompareAndSetOperatorCredential(ctx, tenant, nil, OperatorCredential{
		Alg:          string(trustlist.AlgEd25519),
		PublicKeyPEM: string(bundlesig.MarshalPublicKeyPEM(pub)),
	}); err != nil {
		t.Fatalf("CompareAndSetOperatorCredential: %v", err)
	}
	approveNode(t, ctx, store, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	signer := trustlist.NewEd25519Signer(priv)

	// Stage #1 → manifest M1; capture and sign its canonical bytes off-host.
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage #1: %v", err)
	}
	m1, err := store.GetCurrentSignedTrustList(ctx, tenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList (M1): %v", err)
	}
	signedM1 := signCanonical(t, signer, m1.TrustListJSON)

	// A re-stage moves the staged manifest to M2 (node-peer rekeys → membership + digests change).
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage #2 (re-stage): %v", err)
	}
	m2, err := store.GetCurrentSignedTrustList(ctx, tenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList (M2): %v", err)
	}
	if bytes.Equal(m1.TrustListJSON, m2.TrustListJSON) {
		t.Fatalf("re-stage did not change the staged manifest; the guard cannot be exercised")
	}

	// Installing the STALE M1 signature must be rejected — this is the custody fix.
	if _, err := InstallTrustListSignature(ctx, store, tenant, m1.TrustListJSON, signedM1); !errors.Is(err, ErrStagedManifestMismatch) {
		t.Fatalf("InstallTrustListSignature(stale M1): err = %v, want ErrStagedManifestMismatch", err)
	}
	// The staged manifest stays UNSIGNED — the M1 signature was not welded onto M2/B_new.
	after, err := store.GetCurrentSignedTrustList(ctx, tenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList (after reject): %v", err)
	}
	if len(after.SignatureJSON) != 0 {
		t.Fatalf("a stale signature was installed onto the re-staged manifest (desync!): %s", after.SignatureJSON)
	}
	// Promote must still refuse (keystone ON, manifest unsigned).
	if _, err := PromoteStaged(ctx, store, tenant); err == nil {
		t.Fatalf("PromoteStaged with an unsigned re-staged manifest: err = nil, want refusal")
	}

	// Signing the CURRENT staged bytes (M2) succeeds and promotes cleanly.
	epoch, err := InstallTrustListSignature(ctx, store, tenant, m2.TrustListJSON, signCanonical(t, signer, m2.TrustListJSON))
	if err != nil {
		t.Fatalf("InstallTrustListSignature(current M2): %v", err)
	}
	if epoch != m2.Epoch {
		t.Fatalf("returned epoch = %d, want the staged epoch %d", epoch, m2.Epoch)
	}
	if _, err := PromoteStaged(ctx, store, tenant); err != nil {
		t.Fatalf("PromoteStaged after signing the current manifest: %v", err)
	}
}

// TestInstallTrustListSignature_ErrorMapping covers the remaining typed sentinels the api layer maps:
// no pinned credential (keystone OFF), no staged manifest, and a rogue (non-verifying) signature.
func TestInstallTrustListSignature_ErrorMapping(t *testing.T) {
	store := NewMemStore()
	ctx := putNoClientTopo(t, store)

	// Keystone OFF: no credential pinned → ErrNoPinnedCredential.
	if _, err := InstallTrustListSignature(ctx, store, tenant, []byte("{}"), trustlist.SignedTrustList{}); !errors.Is(err, ErrNoPinnedCredential) {
		t.Fatalf("keystone OFF: err = %v, want ErrNoPinnedCredential", err)
	}

	// Pin a credential but stage nothing → ErrNoStagedManifest. (We sign with a ROGUE key below, so
	// the pinned private half is intentionally discarded.)
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := store.CompareAndSetOperatorCredential(ctx, tenant, nil, OperatorCredential{
		Alg:          string(trustlist.AlgEd25519),
		PublicKeyPEM: string(bundlesig.MarshalPublicKeyPEM(pub)),
	}); err != nil {
		t.Fatalf("CompareAndSetOperatorCredential: %v", err)
	}
	if _, err := InstallTrustListSignature(ctx, store, tenant, []byte("{}"), trustlist.SignedTrustList{}); !errors.Is(err, ErrNoStagedManifest) {
		t.Fatalf("nothing staged: err = %v, want ErrNoStagedManifest", err)
	}

	// Stage, then submit a signature from a DIFFERENT key over the correct bytes → ErrManifestSignatureInvalid.
	approveNode(t, ctx, store, tenant, "node-router", genWGPubKey(t))
	approveNode(t, ctx, store, tenant, "node-peer", genWGPubKey(t))
	if _, err := CompileAndStage(ctx, store, tenant, time.Now()); err != nil {
		t.Fatalf("CompileAndStage: %v", err)
	}
	staged, err := store.GetCurrentSignedTrustList(ctx, tenant)
	if err != nil {
		t.Fatalf("GetCurrentSignedTrustList: %v", err)
	}
	_, roguePriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey(rogue): %v", err)
	}
	rogue := trustlist.NewEd25519Signer(roguePriv)
	if _, err := InstallTrustListSignature(ctx, store, tenant, staged.TrustListJSON, signCanonical(t, rogue, staged.TrustListJSON)); !errors.Is(err, ErrManifestSignatureInvalid) {
		t.Fatalf("rogue signature: err = %v, want ErrManifestSignatureInvalid", err)
	}
	// A rejected verify leaves the staged manifest unsigned.
	if got, _ := store.GetCurrentSignedTrustList(ctx, tenant); len(got.SignatureJSON) != 0 {
		t.Fatalf("a non-verifying signature was installed: %s", got.SignatureJSON)
	}
}

// TestGetNodeRecord_RMWDoesNotBakeOverlay is the PART C durable-read regression, across both Store
// impls. A GetNode-based read-modify-write BAKES the volatile telemetry overlay into the durable record
// (the leak); a GetNodeRecord-based writeback does NOT, while GetNode (the fleet-view read path) still
// merges the live overlay.
func TestGetNodeRecord_RMWDoesNotBakeOverlay(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
			conds := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Reason: "AllPeersUp", Message: "live"}}
			metrics := map[string]json.RawMessage{"resource": json.RawMessage(`{"load1":0.5}`)}
			mk := func(id string) {
				if err := s.UpsertNode(ctx, tenant, Node{NodeID: id, Status: NodeApproved, WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw="}); err != nil {
					t.Fatalf("UpsertNode(%s): %v", id, err)
				}
			}

			// --- The BUGGY shape (GetNode) BAKES the overlay: characterize the leak we prevent. ---
			mk("bug")
			if err := s.RecordTelemetry(ctx, tenant, "bug", conds, metrics, "v1", base); err != nil {
				t.Fatalf("RecordTelemetry(bug): %v", err)
			}
			bugNode, err := s.GetNode(ctx, tenant, "bug") // overlay-merged read
			if err != nil {
				t.Fatalf("GetNode(bug): %v", err)
			}
			if len(bugNode.Conditions) == 0 {
				t.Fatalf("precondition: GetNode did not merge the overlay")
			}
			bugNode.RekeyRequested = true
			if err := s.UpsertNode(ctx, tenant, bugNode); err != nil { // welds the overlay into durable
				t.Fatalf("UpsertNode(bug): %v", err)
			}
			if durable, _ := s.GetNodeRecord(ctx, tenant, "bug"); len(durable.Conditions) == 0 || len(durable.Telemetry) == 0 {
				t.Fatalf("expected the GetNode-based RMW to BAKE the overlay (proving the durable read is needed); got conds=%v telem=%v", durable.Conditions, durable.Telemetry)
			}

			// --- The FIXED shape (GetNodeRecord) does NOT bake the overlay. ---
			mk("ok")
			if err := s.RecordTelemetry(ctx, tenant, "ok", conds, metrics, "v1", base); err != nil {
				t.Fatalf("RecordTelemetry(ok): %v", err)
			}
			okNode, err := s.GetNodeRecord(ctx, tenant, "ok") // durable read for the writeback
			if err != nil {
				t.Fatalf("GetNodeRecord(ok): %v", err)
			}
			if len(okNode.Conditions) != 0 || len(okNode.Telemetry) != 0 {
				t.Fatalf("GetNodeRecord must skip the overlay: conds=%v telem=%v", okNode.Conditions, okNode.Telemetry)
			}
			okNode.RekeyRequested = true
			if err := s.UpsertNode(ctx, tenant, okNode); err != nil {
				t.Fatalf("UpsertNode(ok): %v", err)
			}
			durable, err := s.GetNodeRecord(ctx, tenant, "ok")
			if err != nil {
				t.Fatalf("GetNodeRecord(ok) after RMW: %v", err)
			}
			if len(durable.Conditions) != 0 || len(durable.Telemetry) != 0 || durable.LastAgentVersion != "" || !durable.LastSeen.IsZero() {
				t.Fatalf("GetNodeRecord-based RMW baked the volatile overlay into the durable record: %+v", durable)
			}
			if !durable.RekeyRequested {
				t.Fatalf("the durable custody mutation (RekeyRequested) was lost")
			}
			// The READ path (fleet view) still shows the live overlay after the durable RMW.
			if view, _ := s.GetNode(ctx, tenant, "ok"); len(view.Conditions) == 0 {
				t.Fatalf("GetNode (fleet view) must still merge the live overlay after a durable RMW")
			}
		})
	}
}

// TestFlagFleetRekey_DoesNotBakeOverlay ties PART C + PART D on the actual rekey op: FlagFleetRekey
// flips RekeyRequested via the durable read, so a live telemetry overlay is never persisted, and the
// custody flag is set. (ClearNodeRekey shares the GetNodeRecord read, so the same property holds.)
func TestFlagFleetRekey_DoesNotBakeOverlay(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "n1", Status: NodeApproved, WGPublicKey: "AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw="}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	conds := []runtimecontract.Condition{{Type: runtimecontract.ConditionTypeWireGuard, Status: runtimecontract.ConditionStatusOK, Reason: "AllPeersUp"}}
	if err := s.RecordTelemetry(ctx, tenant, "n1", conds, map[string]json.RawMessage{"resource": json.RawMessage(`{"load1":1}`)}, "v1", base); err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}

	n, err := FlagFleetRekey(ctx, s, tenant)
	if err != nil || n != 1 {
		t.Fatalf("FlagFleetRekey: n=%d err=%v, want 1/nil", n, err)
	}
	durable, err := s.GetNodeRecord(ctx, tenant, "n1")
	if err != nil {
		t.Fatalf("GetNodeRecord: %v", err)
	}
	if !durable.RekeyRequested {
		t.Fatalf("FlagFleetRekey did not set RekeyRequested on the durable record")
	}
	if len(durable.Conditions) != 0 || len(durable.Telemetry) != 0 || !durable.LastSeen.IsZero() {
		t.Fatalf("FlagFleetRekey baked the volatile overlay into the durable record: %+v", durable)
	}

	// ClearNodeRekey clears it (idempotent second call → cleared=false), also without baking.
	if cleared, err := ClearNodeRekey(ctx, s, tenant, "n1"); err != nil || !cleared {
		t.Fatalf("ClearNodeRekey: cleared=%v err=%v, want true/nil", cleared, err)
	}
	if cleared, err := ClearNodeRekey(ctx, s, tenant, "n1"); err != nil || cleared {
		t.Fatalf("ClearNodeRekey (idempotent): cleared=%v err=%v, want false/nil", cleared, err)
	}
	if cleared := durableRekey(t, ctx, s, "n1"); cleared {
		t.Fatalf("ClearNodeRekey left RekeyRequested set")
	}
}

func durableRekey(t *testing.T, ctx context.Context, s Store, id string) bool {
	t.Helper()
	n, err := s.GetNodeRecord(ctx, tenant, id)
	if err != nil {
		t.Fatalf("GetNodeRecord(%s): %v", id, err)
	}
	return n.RekeyRequested
}

// TestWriteJSONL_NoDuplicateBookkeeping is the PART D characterization: each writeJSONL append lands
// exactly once (never duplicated), and the in-memory fileLines running count tracks the on-disk line
// count. It then documents WHY the close-after-write fix is log-and-continue rather than error-return:
// requeuing an ALREADY-written batch (what the old close-error path triggered via flushOnce) duplicates
// it — so writeJSONL must not report a post-write Close() error as a flush failure.
func TestWriteJSONL_NoDuplicateBookkeeping(t *testing.T) {
	dir := t.TempDir()
	h := newTelemetryHistory(dir, DefaultTelemetryHistoryCap, nil)
	nt := TenantID("t1")
	node := "n1"
	const cap = 100
	h.setCap(nt, cap)

	base := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	batch := func(n, off int) []ResourceSample {
		out := make([]ResourceSample, n)
		for i := range out {
			out[i] = ResourceSample{TS: base.Add(time.Duration(off+i) * time.Second), Load1: float64(off + i)}
		}
		return out
	}
	p, err := h.nodeFile(nt, node)
	if err != nil {
		t.Fatalf("nodeFile: %v", err)
	}
	fileLines := func() int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.entryLocked(nt, node).fileLines
	}

	// Append #1 (10 samples): 10 lines on disk, fileLines counted-from-disk == 10.
	if err := h.writeJSONL(nt, node, batch(10, 0), cap); err != nil {
		t.Fatalf("writeJSONL #1: %v", err)
	}
	if got := countLines(p); got != 10 {
		t.Fatalf("after append #1: %d lines on disk, want 10", got)
	}
	if got := fileLines(); got != 10 {
		t.Fatalf("after append #1: fileLines = %d, want 10", got)
	}

	// Append #2 (5 samples): total 15 on disk (the first 10 NOT rewritten), fileLines incremented.
	if err := h.writeJSONL(nt, node, batch(5, 10), cap); err != nil {
		t.Fatalf("writeJSONL #2: %v", err)
	}
	if got := countLines(p); got != 15 {
		t.Fatalf("after append #2: %d lines on disk, want 15 (a rewrite/dup would differ)", got)
	}
	if got := fileLines(); got != 15 {
		t.Fatalf("after append #2: fileLines = %d, want 15", got)
	}

	// Rationale guard: a flushOnce that REQUEUES an already-written batch (the old close-error
	// behaviour) DOES duplicate it — this is exactly what the log-and-continue fix avoids. We show the
	// hazard end-to-end via the buffer path: append to the buffer, flush, then simulate the buggy
	// requeue and flush again → the batch appears twice on disk.
	h.append(nt, node, batch(1, 100)[0]) // one buffered sample
	h.flushOnce()                        // writes it: disk now 16
	if got := countLines(p); got != 16 {
		t.Fatalf("after buffered flush: %d lines, want 16", got)
	}
	h.mu.Lock()
	writtenSample := batch(1, 100)[0]
	h.entryLocked(nt, node).inflight = []telemetryHistoryRecord{{Resource: &writtenSample, RecordedAt: writtenSample.TS}}
	h.mu.Unlock()
	h.requeueInflight(nt, node, cap) // SIMULATE the old close-error requeue of a written batch
	h.flushOnce()                    // re-writes it → duplication
	if got := countLines(p); got != 17 {
		t.Fatalf("requeuing an already-written batch must duplicate it (documenting why the fix is log-and-continue): got %d lines, want 17", got)
	}
}
