package controller

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

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
				MTLSCertFP:        "fp-alpha",
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

			if err := s.SetAppliedGeneration(ctx, tenant, "alpha", 7, "checksum-7"); err != nil {
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
