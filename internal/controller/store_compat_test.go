package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
	"time"
)

// tokenHash returns the hex SHA-256 of a plaintext, matching the on-the-wire
// TokenHash convention (the plaintext token itself is never stored). Using a real
// hash keeps the key a clean path component, which FileStore sanitizes on disk.
func tokenHash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// storeFactory builds a fresh, empty Store for one sub-test. FileStore variants
// use t.TempDir() so nothing ever touches real /var or /etc.
type storeFactory func(t *testing.T) Store

// storeImpls is the table of every Store implementation the compatibility suite
// runs against. Both MemStore and FileStore must satisfy the identical contract,
// so each test below iterates this table.
func storeImpls() []struct {
	name    string
	factory storeFactory
} {
	return []struct {
		name    string
		factory storeFactory
	}{
		{
			name:    "MemStore",
			factory: func(_ *testing.T) Store { return NewMemStore() },
		},
		{
			name: "FileStore",
			factory: func(t *testing.T) Store {
				t.Helper()
				s, err := NewFileStore(t.TempDir())
				if err != nil {
					t.Fatalf("NewFileStore: %v", err)
				}
				return s
			},
		},
	}
}

// tenant is a fixed tenant used throughout the compat suite. Cross-tenant
// isolation is exercised separately in tenant_isolation_test.go.
const tenant = TenantID("compat-tenant")

// TestStoreNodeRoundTrip covers UpsertNode/GetNode round-trip, the ErrNotFound
// path, and ListNodes stable ordering, across both impls.
func TestStoreNodeRoundTrip(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			// GetNode on an empty store -> ErrNotFound.
			if _, err := s.GetNode(ctx, tenant, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetNode(missing): err = %v, want ErrNotFound", err)
			}

			enrolled := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
			want := Node{
				NodeID:            "alpha",
				WGPublicKey:       "pubkey-alpha",
				APITokenHash:      tokenHash("api-alpha"),
				Status:            NodeApproved,
				DesiredGeneration: 3,
				AppliedGeneration: 2,
				LastChecksum:      "sum-alpha",
				LastSeen:          enrolled,
				EnrolledAt:        enrolled,
			}
			if err := s.UpsertNode(ctx, tenant, want); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}

			got, err := s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode(alpha): %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("GetNode round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
			}

			// Upsert matches by NodeID: a second upsert updates in place.
			updated := want
			updated.WGPublicKey = "pubkey-alpha-v2"
			updated.Status = NodeRevoked
			if err := s.UpsertNode(ctx, tenant, updated); err != nil {
				t.Fatalf("UpsertNode(update): %v", err)
			}
			got, err = s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode(alpha) after update: %v", err)
			}
			if got.WGPublicKey != "pubkey-alpha-v2" || got.Status != NodeRevoked {
				t.Fatalf("update not reflected: got %+v", got)
			}

			// ListNodes returns a stable order by NodeID regardless of insert order.
			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "gamma", Status: NodePending}); err != nil {
				t.Fatalf("UpsertNode(gamma): %v", err)
			}
			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "beta", Status: NodePending}); err != nil {
				t.Fatalf("UpsertNode(beta): %v", err)
			}
			nodes, err := s.ListNodes(ctx, tenant)
			if err != nil {
				t.Fatalf("ListNodes: %v", err)
			}
			gotIDs := make([]string, len(nodes))
			for i, n := range nodes {
				gotIDs[i] = n.NodeID
			}
			wantIDs := []string{"alpha", "beta", "gamma"}
			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("ListNodes order = %v, want %v", gotIDs, wantIDs)
			}
		})
	}
}

// TestStoreRekeyRequestedRoundTrip pins that the Node.RekeyRequested flag persists
// across an UpsertNode/GetNode round-trip on both Store impls (the FileStore path is
// the one that must serialize/deserialize it to disk). It also confirms the flag is
// independently flippable via a whole-Node upsert (set true, then clear back to
// false) without disturbing the other fields — the shape the /rekey-all and /rekey
// handlers rely on.
func TestStoreRekeyRequestedRoundTrip(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			want := Node{
				NodeID:      "alpha",
				WGPublicKey: "pubkey-alpha",
				Status:      NodeApproved,
				// RekeyRequested defaults false; flip it on so the round-trip is meaningful.
				RekeyRequested: true,
			}
			if err := s.UpsertNode(ctx, tenant, want); err != nil {
				t.Fatalf("UpsertNode(rekey true): %v", err)
			}
			got, err := s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode(alpha): %v", err)
			}
			if !got.RekeyRequested {
				t.Fatalf("RekeyRequested did not round-trip: got %+v", got)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Node round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
			}

			// Clearing the flag via a whole-Node upsert (the /rekey shape) preserves the
			// other fields and drops the flag back to false.
			cleared := got
			cleared.RekeyRequested = false
			cleared.WGPublicKey = "pubkey-alpha-rotated"
			if err := s.UpsertNode(ctx, tenant, cleared); err != nil {
				t.Fatalf("UpsertNode(rekey clear): %v", err)
			}
			again, err := s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode(alpha) after clear: %v", err)
			}
			if again.RekeyRequested {
				t.Fatalf("RekeyRequested still set after clear: %+v", again)
			}
			if again.WGPublicKey != "pubkey-alpha-rotated" {
				t.Fatalf("WGPublicKey = %q, want pubkey-alpha-rotated", again.WGPublicKey)
			}
		})
	}
}

// TestStoreTopologyVersioning covers PutTopology version increment and the
// GetTopology round-trip (including ErrNotFound before any put).
func TestStoreTopologyVersioning(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			if _, err := s.GetTopology(ctx, tenant); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetTopology(empty): err = %v, want ErrNotFound", err)
			}

			rec1, err := s.PutTopology(ctx, tenant, []byte(`{"v":1}`))
			if err != nil {
				t.Fatalf("PutTopology v1: %v", err)
			}
			if rec1.Version != 1 {
				t.Fatalf("first PutTopology Version = %d, want 1", rec1.Version)
			}

			rec2, err := s.PutTopology(ctx, tenant, []byte(`{"v":2}`))
			if err != nil {
				t.Fatalf("PutTopology v2: %v", err)
			}
			if rec2.Version != 2 {
				t.Fatalf("second PutTopology Version = %d, want 2", rec2.Version)
			}

			cur, err := s.GetTopology(ctx, tenant)
			if err != nil {
				t.Fatalf("GetTopology: %v", err)
			}
			if cur.Version != 2 {
				t.Fatalf("GetTopology Version = %d, want 2", cur.Version)
			}
			if string(cur.JSON) != `{"v":2}` {
				t.Fatalf("GetTopology JSON = %q, want %q", cur.JSON, `{"v":2}`)
			}
		})
	}
}

// TestStoreBundlePromotion covers the stage -> promote -> current lifecycle, the
// generation increments, and the ErrNotFound/ErrNoStagedBundle edge cases.
func TestStoreBundlePromotion(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			// A node is registered at enrollment before any bundle is staged for it.
			// Register it so PromoteStaged has a registry record whose DesiredGeneration
			// it can bump (promote updates existing nodes only — it does not create them).
			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}

			// CurrentGeneration is 0 before any promote.
			if gen, err := s.CurrentGeneration(ctx, tenant); err != nil || gen != 0 {
				t.Fatalf("CurrentGeneration(initial) = (%d, %v), want (0, nil)", gen, err)
			}

			// PromoteStaged with nothing staged -> ErrNoStagedBundle.
			if _, err := s.PromoteStaged(ctx, tenant); !errors.Is(err, ErrNoStagedBundle) {
				t.Fatalf("PromoteStaged(empty): err = %v, want ErrNoStagedBundle", err)
			}

			created := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
			b1 := SignedBundle{
				NodeID:     "alpha",
				Generation: 1,
				Files:      map[string][]byte{"install.sh": []byte("#!/bin/sh\n")},
				IsStaged:   true,
				CreatedAt:  created,
			}
			if err := s.StageBundle(ctx, tenant, b1); err != nil {
				t.Fatalf("StageBundle: %v", err)
			}

			// GetCurrentBundle is ErrNotFound while only a staged bundle exists.
			if _, err := s.GetCurrentBundle(ctx, tenant, "alpha"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetCurrentBundle(staged-only): err = %v, want ErrNotFound", err)
			}

			gen, err := s.PromoteStaged(ctx, tenant)
			if err != nil {
				t.Fatalf("PromoteStaged: %v", err)
			}
			if gen != 1 {
				t.Fatalf("first PromoteStaged gen = %d, want 1", gen)
			}
			if cur, err := s.CurrentGeneration(ctx, tenant); err != nil || cur != 1 {
				t.Fatalf("CurrentGeneration after promote = (%d, %v), want (1, nil)", cur, err)
			}

			got, err := s.GetCurrentBundle(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetCurrentBundle after promote: %v", err)
			}
			if got.NodeID != "alpha" || got.Generation != 1 {
				t.Fatalf("GetCurrentBundle = %+v, want NodeID=alpha Generation=1", got)
			}
			if !got.IsCurrent || got.IsStaged {
				t.Fatalf("promoted bundle flags: IsCurrent=%v IsStaged=%v, want true/false", got.IsCurrent, got.IsStaged)
			}
			if string(got.Files["install.sh"]) != "#!/bin/sh\n" {
				t.Fatalf("GetCurrentBundle Files lost content: %q", got.Files["install.sh"])
			}

			// A second stage+promote advances the generation to 2.
			b2 := SignedBundle{
				NodeID:     "alpha",
				Generation: 2,
				Files:      map[string][]byte{"install.sh": []byte("#!/bin/sh\n# v2\n")},
				IsStaged:   true,
				CreatedAt:  created,
			}
			if err := s.StageBundle(ctx, tenant, b2); err != nil {
				t.Fatalf("StageBundle v2: %v", err)
			}
			gen2, err := s.PromoteStaged(ctx, tenant)
			if err != nil {
				t.Fatalf("PromoteStaged v2: %v", err)
			}
			if gen2 != 2 {
				t.Fatalf("second PromoteStaged gen = %d, want 2", gen2)
			}
			if cur, err := s.CurrentGeneration(ctx, tenant); err != nil || cur != 2 {
				t.Fatalf("CurrentGeneration after second promote = (%d, %v), want (2, nil)", cur, err)
			}
			got2, err := s.GetCurrentBundle(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetCurrentBundle v2: %v", err)
			}
			if got2.Generation != 2 || string(got2.Files["install.sh"]) != "#!/bin/sh\n# v2\n" {
				t.Fatalf("second current bundle = %+v, want gen 2 with v2 content", got2)
			}

			// Promote sets the promoted node's DesiredGeneration to the new gen.
			node, err := s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode(alpha) after promote: %v", err)
			}
			if node.DesiredGeneration != 2 {
				t.Fatalf("node DesiredGeneration = %d, want 2 after promote", node.DesiredGeneration)
			}
		})
	}
}

// TestStorePromoteMultiNode covers the load-bearing promote semantics that the
// single-node test cannot: a promote flips ALL staged bundles at once, bumps
// DesiredGeneration on each PROMOTED node, and a node that is NOT re-staged keeps
// its prior current bundle and its prior DesiredGeneration across a later promote.
func TestStorePromoteMultiNode(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)
			created := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)

			ids := []string{"alpha", "beta", "gamma"}
			for _, id := range ids {
				if err := s.UpsertNode(ctx, tenant, Node{NodeID: id, Status: NodeApproved}); err != nil {
					t.Fatalf("UpsertNode(%s): %v", id, err)
				}
				if err := s.StageBundle(ctx, tenant, SignedBundle{
					NodeID: id, Generation: 1, IsStaged: true, CreatedAt: created,
					Files: map[string][]byte{"install.sh": []byte("# " + id + " v1\n")},
				}); err != nil {
					t.Fatalf("StageBundle(%s): %v", id, err)
				}
			}

			// First promote: all three become current at generation 1.
			if gen, err := s.PromoteStaged(ctx, tenant); err != nil || gen != 1 {
				t.Fatalf("PromoteStaged #1 = (%d, %v), want (1, nil)", gen, err)
			}
			for _, id := range ids {
				b, err := s.GetCurrentBundle(ctx, tenant, id)
				if err != nil || b.Generation != 1 || !b.IsCurrent {
					t.Fatalf("%s current after promote#1 = (%+v, %v), want gen1 current", id, b, err)
				}
				n, _ := s.GetNode(ctx, tenant, id)
				if n.DesiredGeneration != 1 {
					t.Fatalf("%s DesiredGeneration = %d, want 1", id, n.DesiredGeneration)
				}
			}

			// Re-stage ONLY beta and promote again -> generation 2.
			if err := s.StageBundle(ctx, tenant, SignedBundle{
				NodeID: "beta", Generation: 2, IsStaged: true, CreatedAt: created,
				Files: map[string][]byte{"install.sh": []byte("# beta v2\n")},
			}); err != nil {
				t.Fatalf("StageBundle(beta v2): %v", err)
			}
			if gen, err := s.PromoteStaged(ctx, tenant); err != nil || gen != 2 {
				t.Fatalf("PromoteStaged #2 = (%d, %v), want (2, nil)", gen, err)
			}

			// beta advanced; alpha & gamma kept their gen-1 current bundle AND their
			// gen-1 DesiredGeneration (they were not re-staged).
			betaCur, _ := s.GetCurrentBundle(ctx, tenant, "beta")
			if betaCur.Generation != 2 || string(betaCur.Files["install.sh"]) != "# beta v2\n" {
				t.Fatalf("beta current after promote#2 = %+v, want gen2 v2", betaCur)
			}
			betaNode, _ := s.GetNode(ctx, tenant, "beta")
			if betaNode.DesiredGeneration != 2 {
				t.Fatalf("beta DesiredGeneration = %d, want 2", betaNode.DesiredGeneration)
			}
			for _, id := range []string{"alpha", "gamma"} {
				b, err := s.GetCurrentBundle(ctx, tenant, id)
				if err != nil || b.Generation != 1 {
					t.Fatalf("%s current after promote#2 = (%+v, %v), want UNCHANGED gen1", id, b, err)
				}
				n, _ := s.GetNode(ctx, tenant, id)
				if n.DesiredGeneration != 1 {
					t.Fatalf("%s DesiredGeneration = %d, want UNCHANGED 1 (not re-staged)", id, n.DesiredGeneration)
				}
			}
			if cur, err := s.CurrentGeneration(ctx, tenant); err != nil || cur != 2 {
				t.Fatalf("CurrentGeneration = (%d, %v), want (2, nil)", cur, err)
			}
		})
	}
}

// TestStoreAgentReports covers SetAppliedGeneration and TouchLastSeen reflecting
// into the node record returned by GetNode.
func TestStoreAgentReports(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}

			if err := s.SetAppliedGeneration(ctx, tenant, "alpha", 7, "checksum-7", "healthy"); err != nil {
				t.Fatalf("SetAppliedGeneration: %v", err)
			}
			seen := time.Date(2026, 6, 8, 15, 30, 0, 0, time.UTC)
			if err := s.TouchLastSeen(ctx, tenant, "alpha", seen); err != nil {
				t.Fatalf("TouchLastSeen: %v", err)
			}

			got, err := s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode: %v", err)
			}
			if got.AppliedGeneration != 7 {
				t.Fatalf("AppliedGeneration = %d, want 7", got.AppliedGeneration)
			}
			if got.LastChecksum != "checksum-7" {
				t.Fatalf("LastChecksum = %q, want %q", got.LastChecksum, "checksum-7")
			}
			if got.LastHealth != "healthy" {
				t.Fatalf("LastHealth = %q, want %q", got.LastHealth, "healthy")
			}
			if !got.LastSeen.Equal(seen) {
				t.Fatalf("LastSeen = %v, want %v", got.LastSeen, seen)
			}
		})
	}
}

// TestStoreAuditRoundTrip covers AppendAudit/ListAudit round-trip across both
// impls: entries come back in Seq order, chained so VerifyAuditChain accepts them.
func TestStoreAuditRoundTrip(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			ts := time.Date(2026, 6, 8, 8, 0, 0, 0, time.UTC)
			actions := []string{"enroll", "stage", "promote"}
			for i, action := range actions {
				stored, err := s.AppendAudit(ctx, tenant, AuditEntry{
					Timestamp: ts.Add(time.Duration(i) * time.Minute),
					Actor:     "operator",
					Action:    action,
					NodeID:    "alpha",
				})
				if err != nil {
					t.Fatalf("AppendAudit(%s): %v", action, err)
				}
				if stored.Hash == "" {
					t.Fatalf("AppendAudit(%s): returned empty Hash", action)
				}
				if i == 0 && stored.PrevHash != "" {
					t.Fatalf("AppendAudit(first): PrevHash = %q, want empty", stored.PrevHash)
				}
			}

			entries, err := s.ListAudit(ctx, tenant)
			if err != nil {
				t.Fatalf("ListAudit: %v", err)
			}
			if len(entries) != len(actions) {
				t.Fatalf("ListAudit len = %d, want %d", len(entries), len(actions))
			}
			for i, e := range entries {
				if e.Action != actions[i] {
					t.Fatalf("entry[%d].Action = %q, want %q (Seq order)", i, e.Action, actions[i])
				}
				if i > 0 && e.Seq <= entries[i-1].Seq {
					t.Fatalf("Seq not monotonic: entries[%d].Seq=%d entries[%d].Seq=%d", i-1, entries[i-1].Seq, i, e.Seq)
				}
			}
			if bad := VerifyAuditChain(entries); bad != -1 {
				t.Fatalf("VerifyAuditChain on store-built chain = %d, want -1", bad)
			}
		})
	}
}

// TestStoreWaitForGeneration covers the long-poll primitive: a concurrent promote
// wakes a blocked waiter, and a cancelled ctx makes the waiter return promptly.
func TestStoreWaitForGeneration(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Run("promote-wakes-waiter", func(t *testing.T) {
				ctx := context.Background()
				s := impl.factory(t)

				if err := s.StageBundle(ctx, tenant, SignedBundle{
					NodeID:     "alpha",
					Generation: 1,
					Files:      map[string][]byte{"install.sh": []byte("x")},
					IsStaged:   true,
				}); err != nil {
					t.Fatalf("StageBundle: %v", err)
				}

				type result struct {
					gen int64
					err error
				}
				done := make(chan result, 1)
				go func() {
					gen, err := s.WaitForGeneration(ctx, tenant, 0)
					done <- result{gen, err}
				}()

				// Give the waiter time to block, then promote from this goroutine.
				go func() {
					time.Sleep(50 * time.Millisecond)
					if _, err := s.PromoteStaged(ctx, tenant); err != nil {
						t.Errorf("PromoteStaged: %v", err)
					}
				}()

				select {
				case r := <-done:
					if r.err != nil {
						t.Fatalf("WaitForGeneration: %v", r.err)
					}
					if r.gen != 1 {
						t.Fatalf("WaitForGeneration gen = %d, want 1", r.gen)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("WaitForGeneration did not return after promote")
				}
			})

			t.Run("cancelled-ctx-returns-promptly", func(t *testing.T) {
				s := impl.factory(t)
				ctx, cancel := context.WithCancel(context.Background())

				type result struct {
					gen int64
					err error
				}
				done := make(chan result, 1)
				go func() {
					gen, err := s.WaitForGeneration(ctx, tenant, 0)
					done <- result{gen, err}
				}()

				// Cancel after the waiter has had a chance to block.
				time.Sleep(50 * time.Millisecond)
				cancel()

				select {
				case r := <-done:
					if r.err == nil {
						t.Fatalf("WaitForGeneration(cancelled): err = nil, want non-nil")
					}
					if r.gen != 0 {
						t.Fatalf("WaitForGeneration(cancelled): gen = %d, want 0", r.gen)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("WaitForGeneration did not return after ctx cancel")
				}
			})
		})
	}
}

// TestStoreBumpGeneration covers the WAKE primitive across both impls: BumpGeneration
// advances CurrentGeneration by one WITHOUT touching any bundle, and a concurrent
// WaitForGeneration(prev) blocked at the prior generation returns the new generation.
// This is the BLOCKER-1 fix — a rekey-all bumps the generation so parked daemon agents
// (waiting on WaitForGeneration, which only fires on an advance) wake to observe the
// rekey signal — so the test asserts both the counter advance and that the bumped (no-
// bundle-change) generation still wakes a waiter.
func TestStoreBumpGeneration(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Run("advances-current-generation", func(t *testing.T) {
				ctx := context.Background()
				s := impl.factory(t)

				if gen, err := s.CurrentGeneration(ctx, tenant); err != nil || gen != 0 {
					t.Fatalf("CurrentGeneration(initial) = (%d, %v), want (0, nil)", gen, err)
				}

				// A bump with a promoted bundle present must NOT change the bundle: stage +
				// promote one generation first, then bump and confirm the bundle is intact.
				if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
					t.Fatalf("UpsertNode: %v", err)
				}
				if err := s.StageBundle(ctx, tenant, SignedBundle{
					NodeID: "alpha", Generation: 1, IsStaged: true,
					Files: map[string][]byte{"install.sh": []byte("# promoted v1\n")},
				}); err != nil {
					t.Fatalf("StageBundle: %v", err)
				}
				if gen, err := s.PromoteStaged(ctx, tenant); err != nil || gen != 1 {
					t.Fatalf("PromoteStaged = (%d, %v), want (1, nil)", gen, err)
				}

				// Bump advances the counter to 2 without staging/promoting anything.
				newGen, err := s.BumpGeneration(ctx, tenant)
				if err != nil {
					t.Fatalf("BumpGeneration: %v", err)
				}
				if newGen != 2 {
					t.Fatalf("BumpGeneration returned %d, want 2", newGen)
				}
				if cur, err := s.CurrentGeneration(ctx, tenant); err != nil || cur != 2 {
					t.Fatalf("CurrentGeneration after bump = (%d, %v), want (2, nil)", cur, err)
				}

				// The current bundle is UNCHANGED: GetCurrentBundle still returns the gen-1
				// bundle (a bump is a WAKE, not a deploy — it must not flip a new bundle live).
				b, err := s.GetCurrentBundle(ctx, tenant, "alpha")
				if err != nil {
					t.Fatalf("GetCurrentBundle after bump: %v", err)
				}
				if b.Generation != 1 || string(b.Files["install.sh"]) != "# promoted v1\n" {
					t.Fatalf("bump changed the current bundle: got gen %d content %q, want gen 1 unchanged", b.Generation, b.Files["install.sh"])
				}

				// A second bump advances again.
				if gen, err := s.BumpGeneration(ctx, tenant); err != nil || gen != 3 {
					t.Fatalf("second BumpGeneration = (%d, %v), want (3, nil)", gen, err)
				}
			})

			t.Run("wakes-waiter", func(t *testing.T) {
				ctx := context.Background()
				s := impl.factory(t)

				type result struct {
					gen int64
					err error
				}
				done := make(chan result, 1)
				go func() {
					gen, err := s.WaitForGeneration(ctx, tenant, 0)
					done <- result{gen, err}
				}()

				// Give the waiter time to block, then BUMP (not promote) from this goroutine.
				go func() {
					time.Sleep(50 * time.Millisecond)
					if _, err := s.BumpGeneration(ctx, tenant); err != nil {
						t.Errorf("BumpGeneration: %v", err)
					}
				}()

				select {
				case r := <-done:
					if r.err != nil {
						t.Fatalf("WaitForGeneration: %v", r.err)
					}
					if r.gen != 1 {
						t.Fatalf("WaitForGeneration gen = %d, want 1 (the bumped generation)", r.gen)
					}
				case <-time.After(5 * time.Second):
					t.Fatalf("WaitForGeneration did not return after BumpGeneration (BLOCKER-1: a bump must wake a parked waiter)")
				}
			})
		})
	}
}

// TestStoreEnrollmentTokens covers the enrollment-token contract across both Store
// impls: create then consume (happy path under TTL), single-use (a second consume
// is ErrTokenConsumed), and the three ErrTokenInvalid paths (unknown hash, wrong
// nodeID, expired). The check-and-burn is atomic in each impl; this test pins the
// observable error mapping, not the concurrency (that holds by construction under
// the store lock).
func TestStoreEnrollmentTokens(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
			expires := now.Add(15 * time.Minute)
			tok := EnrollmentToken{
				TokenHash: tokenHash("plaintext-alpha"),
				NodeID:    "alpha",
				ExpiresAt: expires,
			}
			if err := s.CreateEnrollmentToken(ctx, tenant, tok); err != nil {
				t.Fatalf("CreateEnrollmentToken: %v", err)
			}

			// Consuming an unknown hash -> ErrTokenInvalid.
			if err := s.ConsumeEnrollmentToken(ctx, tenant, tokenHash("nope"), "alpha", now); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("Consume(unknown hash): err = %v, want ErrTokenInvalid", err)
			}

			// Consuming the right hash with the wrong nodeID -> ErrTokenInvalid (the
			// token is node-scoped; it is not visible to any other node).
			if err := s.ConsumeEnrollmentToken(ctx, tenant, tok.TokenHash, "beta", now); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("Consume(wrong node): err = %v, want ErrTokenInvalid", err)
			}

			// Happy path: now < ExpiresAt and the nodeID matches -> nil (burned).
			if err := s.ConsumeEnrollmentToken(ctx, tenant, tok.TokenHash, "alpha", now); err != nil {
				t.Fatalf("Consume(happy): err = %v, want nil", err)
			}

			// Single-use: a second consume of the same token -> ErrTokenConsumed.
			if err := s.ConsumeEnrollmentToken(ctx, tenant, tok.TokenHash, "alpha", now.Add(time.Minute)); !errors.Is(err, ErrTokenConsumed) {
				t.Fatalf("Consume(second): err = %v, want ErrTokenConsumed", err)
			}

			// Expiry: a fresh, never-consumed token is ErrTokenInvalid once now is at
			// or after ExpiresAt. Consume exactly at ExpiresAt (the boundary is
			// exclusive: valid only while now.Before(ExpiresAt)).
			expTok := EnrollmentToken{
				TokenHash: tokenHash("plaintext-gamma"),
				NodeID:    "gamma",
				ExpiresAt: expires,
			}
			if err := s.CreateEnrollmentToken(ctx, tenant, expTok); err != nil {
				t.Fatalf("CreateEnrollmentToken(gamma): %v", err)
			}
			if err := s.ConsumeEnrollmentToken(ctx, tenant, expTok.TokenHash, "gamma", expires); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("Consume(at ExpiresAt): err = %v, want ErrTokenInvalid", err)
			}
			if err := s.ConsumeEnrollmentToken(ctx, tenant, expTok.TokenHash, "gamma", expires.Add(time.Hour)); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("Consume(after ExpiresAt): err = %v, want ErrTokenInvalid", err)
			}
		})
	}
}

// TestStoreAPITokens covers the per-node bearer-token contract across both Store
// impls: issuing stamps APITokenHash and makes the token resolvable; lookup of an
// unknown hash is ErrTokenInvalid; issuing for an absent node is ErrNotFound; a
// revoked node's token never authorizes (ErrTokenInvalid) even before the index is
// cleared; RevokeNodeAPIToken clears the hash + index and is idempotent.
func TestStoreAPITokens(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			hash := tokenHash("plaintext-api-alpha")

			// Issuing a token for an absent node -> ErrNotFound (the node must be
			// registered at enrollment before its token is stamped).
			if err := s.IssueNodeAPIToken(ctx, tenant, "alpha", hash); !errors.Is(err, ErrNotFound) {
				t.Fatalf("IssueNodeAPIToken(absent node): err = %v, want ErrNotFound", err)
			}

			// Looking up an unmapped hash -> ErrTokenInvalid.
			if _, err := s.LookupNodeByAPIToken(ctx, tenant, hash); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupNodeByAPIToken(unknown): err = %v, want ErrTokenInvalid", err)
			}

			// Register the node, then issue: APITokenHash is stamped and the token
			// resolves back to the node.
			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", WGPublicKey: "pub-alpha", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}
			if err := s.IssueNodeAPIToken(ctx, tenant, "alpha", hash); err != nil {
				t.Fatalf("IssueNodeAPIToken: %v", err)
			}
			node, err := s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode after issue: %v", err)
			}
			if node.APITokenHash != hash {
				t.Fatalf("APITokenHash = %q, want %q", node.APITokenHash, hash)
			}
			got, err := s.LookupNodeByAPIToken(ctx, tenant, hash)
			if err != nil {
				t.Fatalf("LookupNodeByAPIToken(issued): %v", err)
			}
			if got.NodeID != "alpha" || got.WGPublicKey != "pub-alpha" {
				t.Fatalf("LookupNodeByAPIToken = %+v, want alpha/pub-alpha", got)
			}

			// A revoked node's token never authorizes, even though the index still
			// maps the hash: lookup -> ErrTokenInvalid.
			revoked := node
			revoked.Status = NodeRevoked
			if err := s.UpsertNode(ctx, tenant, revoked); err != nil {
				t.Fatalf("UpsertNode(revoked): %v", err)
			}
			if _, err := s.LookupNodeByAPIToken(ctx, tenant, hash); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupNodeByAPIToken(revoked node): err = %v, want ErrTokenInvalid", err)
			}

			// RevokeNodeAPIToken clears the hash and deletes the index entry.
			if err := s.RevokeNodeAPIToken(ctx, tenant, "alpha"); err != nil {
				t.Fatalf("RevokeNodeAPIToken: %v", err)
			}
			cleared, err := s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode after revoke: %v", err)
			}
			if cleared.APITokenHash != "" {
				t.Fatalf("APITokenHash after revoke = %q, want empty", cleared.APITokenHash)
			}
			if _, err := s.LookupNodeByAPIToken(ctx, tenant, hash); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupNodeByAPIToken after revoke: err = %v, want ErrTokenInvalid", err)
			}

			// Revoke is idempotent: a second revoke (no issued token) is a no-op
			// success, and so is revoking a node that was never issued a token / an
			// absent node.
			if err := s.RevokeNodeAPIToken(ctx, tenant, "alpha"); err != nil {
				t.Fatalf("RevokeNodeAPIToken(idempotent): %v", err)
			}
			if err := s.RevokeNodeAPIToken(ctx, tenant, "never-issued"); err != nil {
				t.Fatalf("RevokeNodeAPIToken(absent node): %v", err)
			}
		})
	}
}

// TestStoreOperatorCredential covers the keystone operator-credential contract across
// both Store impls: ErrNotFound before any pin (keystone OFF), a SetOperatorCredential
// round-trips every field, and a second Set replaces the prior credential in place.
func TestStoreOperatorCredential(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			// No credential pinned -> ErrNotFound (keystone OFF, behave as today).
			if _, err := s.GetOperatorCredential(ctx, tenant); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetOperatorCredential(unpinned): err = %v, want ErrNotFound", err)
			}

			want := OperatorCredential{
				Alg:          "ed25519",
				CredentialID: "cred-abc",
				PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQ==\n-----END PUBLIC KEY-----\n",
				RPID:         "yaog.example",
				Origin:       "https://yaog.example",
			}
			if err := s.SetOperatorCredential(ctx, tenant, want); err != nil {
				t.Fatalf("SetOperatorCredential: %v", err)
			}
			got, err := s.GetOperatorCredential(ctx, tenant)
			if err != nil {
				t.Fatalf("GetOperatorCredential after set: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("OperatorCredential round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
			}

			// A second Set replaces the prior credential in place.
			want2 := want
			want2.Alg = "webauthn-es256"
			want2.CredentialID = "cred-xyz"
			if err := s.SetOperatorCredential(ctx, tenant, want2); err != nil {
				t.Fatalf("SetOperatorCredential(replace): %v", err)
			}
			got2, err := s.GetOperatorCredential(ctx, tenant)
			if err != nil {
				t.Fatalf("GetOperatorCredential after replace: %v", err)
			}
			if !reflect.DeepEqual(got2, want2) {
				t.Fatalf("OperatorCredential replace mismatch:\n got = %+v\nwant = %+v", got2, want2)
			}
		})
	}
}

// TestStoreSignedTrustList covers the keystone signed-trust-list contract across both
// Store impls: ErrNotFound before any sign, a PutSignedTrustList round-trips the raw
// byte fields and the epoch (the FileStore path serializes the bytes as base64), and a
// second Put replaces the prior trust-list in place.
func TestStoreSignedTrustList(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			// No trust-list signed -> ErrNotFound.
			if _, err := s.GetCurrentSignedTrustList(ctx, tenant); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetCurrentSignedTrustList(none): err = %v, want ErrNotFound", err)
			}

			want := StoredTrustList{
				TrustListJSON: []byte(`{"schema_version":1,"tenant":"compat-tenant","epoch":0,"members":[],"created_at":"2026-06-08T00:00:00Z"}` + "\n"),
				SignatureJSON: []byte(`{"alg":"ed25519","credential_id":"k","public_key":"p","signature":"s"}`),
				Epoch:         0,
			}
			if err := s.PutSignedTrustList(ctx, tenant, want); err != nil {
				t.Fatalf("PutSignedTrustList: %v", err)
			}
			got, err := s.GetCurrentSignedTrustList(ctx, tenant)
			if err != nil {
				t.Fatalf("GetCurrentSignedTrustList after put: %v", err)
			}
			if !bytes.Equal(got.TrustListJSON, want.TrustListJSON) ||
				!bytes.Equal(got.SignatureJSON, want.SignatureJSON) ||
				got.Epoch != want.Epoch {
				t.Fatalf("StoredTrustList round-trip mismatch:\n got = %+v\nwant = %+v", got, want)
			}

			// A second Put replaces the prior trust-list in place (new epoch + bytes).
			want2 := StoredTrustList{
				TrustListJSON: []byte(`{"epoch":1}` + "\n"),
				SignatureJSON: []byte(`{"alg":"ed25519"}`),
				Epoch:         1,
			}
			if err := s.PutSignedTrustList(ctx, tenant, want2); err != nil {
				t.Fatalf("PutSignedTrustList(replace): %v", err)
			}
			got2, err := s.GetCurrentSignedTrustList(ctx, tenant)
			if err != nil {
				t.Fatalf("GetCurrentSignedTrustList after replace: %v", err)
			}
			if !bytes.Equal(got2.TrustListJSON, want2.TrustListJSON) || got2.Epoch != 1 {
				t.Fatalf("StoredTrustList replace mismatch:\n got = %+v\nwant = %+v", got2, want2)
			}
		})
	}
}

// TestStoreAPITokenRotation pins the rotation invariant across both Store impls:
// issuing a fresh token for a node that already has one must invalidate the OLD
// token at the lookup chokepoint. This is the re-enroll-leaves-old-token bug: a
// second IssueNodeAPIToken (e.g. a re-enrollment) used to leave the prior
// reverse-index entry in place, so the stale token kept resolving. IssueNodeAPIToken
// now drops the old index entry on rotation, and LookupNodeByAPIToken additionally
// requires the resolved node's APITokenHash to still equal the presented hash, so a
// stale token can never authorize.
func TestStoreAPITokenRotation(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			hashA := tokenHash("plaintext-api-A")
			hashB := tokenHash("plaintext-api-B")

			// Register the node (approved) then issue token A.
			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", WGPublicKey: "pub-alpha", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}
			if err := s.IssueNodeAPIToken(ctx, tenant, "alpha", hashA); err != nil {
				t.Fatalf("IssueNodeAPIToken(A): %v", err)
			}
			gotA, err := s.LookupNodeByAPIToken(ctx, tenant, hashA)
			if err != nil {
				t.Fatalf("LookupNodeByAPIToken(A) before rotation: %v", err)
			}
			if gotA.NodeID != "alpha" {
				t.Fatalf("LookupNodeByAPIToken(A) = %+v, want alpha", gotA)
			}

			// Rotate: issue token B for the same node. This must invalidate A.
			if err := s.IssueNodeAPIToken(ctx, tenant, "alpha", hashB); err != nil {
				t.Fatalf("IssueNodeAPIToken(B): %v", err)
			}

			// The OLD token A no longer authorizes (rotation invalidated it).
			if _, err := s.LookupNodeByAPIToken(ctx, tenant, hashA); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("LookupNodeByAPIToken(A) after rotation: err = %v, want ErrTokenInvalid", err)
			}
			// The NEW token B resolves to the node.
			gotB, err := s.LookupNodeByAPIToken(ctx, tenant, hashB)
			if err != nil {
				t.Fatalf("LookupNodeByAPIToken(B) after rotation: %v", err)
			}
			if gotB.NodeID != "alpha" || gotB.APITokenHash != hashB {
				t.Fatalf("LookupNodeByAPIToken(B) = %+v, want alpha with APITokenHash=B", gotB)
			}
		})
	}
}
