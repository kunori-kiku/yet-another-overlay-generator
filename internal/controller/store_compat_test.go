package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// TestStoreTopologyHistory covers the bounded version history (plan-2, D7):
// every PutTopology is retained, the list is newest-first and pruned to
// TopologyHistoryLimit, retained versions round-trip byte-exact, and pruned or
// unknown versions are ErrNotFound. Perpetual: the history contract is a durable
// Store invariant (the recovery substrate for a bad overwrite).
func TestStoreTopologyHistory(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			// Empty tenant: empty list (not an error), unknown version ErrNotFound.
			if infos, err := s.ListTopologyVersions(ctx, tenant); err != nil || len(infos) != 0 {
				t.Fatalf("ListTopologyVersions(empty) = %v, %v; want empty, nil", infos, err)
			}
			if _, err := s.GetTopologyVersion(ctx, tenant, 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetTopologyVersion(empty, 1): err = %v, want ErrNotFound", err)
			}

			// TopologyHistoryLimit+2 successive puts: versions 1..limit+2.
			total := TopologyHistoryLimit + 2
			for i := 1; i <= total; i++ {
				if _, err := s.PutTopology(ctx, tenant, []byte(fmt.Sprintf(`{"v":%d}`, i))); err != nil {
					t.Fatalf("PutTopology v%d: %v", i, err)
				}
			}

			// The list holds exactly the limit, newest first.
			infos, err := s.ListTopologyVersions(ctx, tenant)
			if err != nil {
				t.Fatalf("ListTopologyVersions: %v", err)
			}
			if len(infos) != TopologyHistoryLimit {
				t.Fatalf("retained %d versions, want %d", len(infos), TopologyHistoryLimit)
			}
			for i, info := range infos {
				wantVersion := int64(total - i)
				if info.Version != wantVersion {
					t.Fatalf("infos[%d].Version = %d, want %d (newest first)", i, info.Version, wantVersion)
				}
				if info.UpdatedAt.IsZero() {
					t.Errorf("infos[%d].UpdatedAt is zero", i)
				}
				if info.Bytes <= 0 {
					t.Errorf("infos[%d].Bytes = %d, want > 0", i, info.Bytes)
				}
			}

			// A retained version round-trips byte-exact; the oldest retained one too.
			oldest := int64(total - TopologyHistoryLimit + 1)
			rec, err := s.GetTopologyVersion(ctx, tenant, oldest)
			if err != nil {
				t.Fatalf("GetTopologyVersion(%d): %v", oldest, err)
			}
			if want := fmt.Sprintf(`{"v":%d}`, oldest); string(rec.JSON) != want {
				t.Fatalf("GetTopologyVersion(%d).JSON = %q, want %q", oldest, rec.JSON, want)
			}

			// Pruned and never-existed versions are ErrNotFound.
			if _, err := s.GetTopologyVersion(ctx, tenant, oldest-1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetTopologyVersion(pruned %d): err = %v, want ErrNotFound", oldest-1, err)
			}
			if _, err := s.GetTopologyVersion(ctx, tenant, int64(total+50)); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetTopologyVersion(unknown): err = %v, want ErrNotFound", err)
			}

			// The CURRENT record is unaffected by history mechanics.
			cur, err := s.GetTopology(ctx, tenant)
			if err != nil || cur.Version != int64(total) {
				t.Fatalf("GetTopology after history churn = v%d, %v; want v%d, nil", cur.Version, err, total)
			}
		})
	}
}

// TestFileStoreTopologyHistoryCrashShape: a crash between the history write and
// the topology.json flip leaves an orphan history file one version ahead of the
// current record. The orphan was NEVER the committed topology, so it must be
// INVISIBLE — not listed, not servable (an operator must not be offered to
// "recover" a write that never committed) — and the next PutTopology reassigns
// that version number and overwrites it (self-heal). FileStore-specific by nature
// (MemStore cannot crash mid-put).
func TestFileStoreTopologyHistoryCrashShape(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	if _, err := s.PutTopology(ctx, tenant, []byte(`{"v":1}`)); err != nil {
		t.Fatalf("PutTopology v1: %v", err)
	}

	// Simulate the crash shape: history file for v2 exists, topology.json still v1.
	orphan := TopologyRecord{Version: 2, JSON: []byte(`{"v":"orphan"}`), UpdatedAt: time.Now().UTC()}
	orphanJSON, _ := json.Marshal(orphan)
	orphanPath := filepath.Join(dir, string(tenant), "topology-history", "2.json")
	if err := os.WriteFile(orphanPath, orphanJSON, 0600); err != nil {
		t.Fatalf("plant orphan: %v", err)
	}

	// The orphan is invisible: the list shows only the committed v1, and the
	// orphan's version is not servable.
	infos, err := s.ListTopologyVersions(ctx, tenant)
	if err != nil {
		t.Fatalf("ListTopologyVersions with orphan: %v", err)
	}
	if len(infos) != 1 || infos[0].Version != 1 {
		t.Fatalf("list with orphan = %+v, want only the committed [v1]", infos)
	}
	if _, err := s.GetTopologyVersion(ctx, tenant, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTopologyVersion(orphan 2): err = %v, want ErrNotFound (never committed)", err)
	}

	// The next put self-heals: it assigns version 2 (current was 1) and overwrites.
	rec, err := s.PutTopology(ctx, tenant, []byte(`{"v":"healed"}`))
	if err != nil {
		t.Fatalf("PutTopology after orphan: %v", err)
	}
	if rec.Version != 2 {
		t.Fatalf("post-orphan PutTopology Version = %d, want 2", rec.Version)
	}
	got, err := s.GetTopologyVersion(ctx, tenant, 2)
	if err != nil || string(got.JSON) != `{"v":"healed"}` {
		t.Fatalf("history v2 after self-heal = %q, %v; want healed bytes", got.JSON, err)
	}
}

// TestFileStoreTopologyHistoryUpgradeShape: a deployment whose current topology
// predates the history feature has topology.json but NO history files. The
// recovery surface must still work: the current record lists and serves, and the
// next put lazily backfills the displaced version so it stays recoverable.
func TestFileStoreTopologyHistoryUpgradeShape(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Hand-place a pre-history deployment shape: topology.json at v37, no history dir.
	tdir := filepath.Join(dir, string(tenant))
	if err := os.MkdirAll(tdir, 0700); err != nil {
		t.Fatal(err)
	}
	pre := TopologyRecord{Version: 37, JSON: []byte(`{"v":37}`), UpdatedAt: time.Now().UTC()}
	preJSON, _ := json.Marshal(pre)
	if err := os.WriteFile(filepath.Join(tdir, "topology.json"), preJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// The current record lists and serves despite having no history file.
	infos, err := s.ListTopologyVersions(ctx, tenant)
	if err != nil || len(infos) != 1 || infos[0].Version != 37 {
		t.Fatalf("upgrade-shape list = %+v, %v; want [v37]", infos, err)
	}
	got, err := s.GetTopologyVersion(ctx, tenant, 37)
	if err != nil || string(got.JSON) != `{"v":37}` {
		t.Fatalf("upgrade-shape GetTopologyVersion(37) = %q, %v; want stored bytes", got.JSON, err)
	}

	// The next put backfills v37 into history before storing v38 — the displaced
	// pre-upgrade version stays recoverable.
	if _, err := s.PutTopology(ctx, tenant, []byte(`{"v":38}`)); err != nil {
		t.Fatalf("PutTopology v38: %v", err)
	}
	got, err = s.GetTopologyVersion(ctx, tenant, 37)
	if err != nil || string(got.JSON) != `{"v":37}` {
		t.Fatalf("backfilled v37 = %q, %v; want pre-upgrade bytes", got.JSON, err)
	}
	infos, err = s.ListTopologyVersions(ctx, tenant)
	if err != nil || len(infos) != 2 || infos[0].Version != 38 || infos[1].Version != 37 {
		t.Fatalf("post-backfill list = %+v, %v; want [v38 v37]", infos, err)
	}
}

// TestFileStoreTopologyHistoryCorruptEntrySkipped: one bit-rotted/corrupt history
// file must not brick the whole recovery list — it is skipped; intact versions
// keep listing and serving.
func TestFileStoreTopologyHistoryCorruptEntrySkipped(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := s.PutTopology(ctx, tenant, []byte(fmt.Sprintf(`{"v":%d}`, i))); err != nil {
			t.Fatalf("PutTopology v%d: %v", i, err)
		}
	}
	// Corrupt v2's retained file (truncated JSON).
	corrupt := filepath.Join(dir, string(tenant), "topology-history", "2.json")
	if err := os.WriteFile(corrupt, []byte(`{"Version": 2, "JSO`), 0600); err != nil {
		t.Fatal(err)
	}

	infos, err := s.ListTopologyVersions(ctx, tenant)
	if err != nil {
		t.Fatalf("ListTopologyVersions with corrupt entry: %v (the recovery list must not brick)", err)
	}
	if len(infos) != 2 || infos[0].Version != 3 || infos[1].Version != 1 {
		t.Fatalf("list with corrupt v2 = %+v, want [v3 v1] (corrupt entry skipped)", infos)
	}
	// Intact versions still serve.
	if _, err := s.GetTopologyVersion(ctx, tenant, 1); err != nil {
		t.Errorf("GetTopologyVersion(1) after corruption elsewhere: %v", err)
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

			if err := s.SetAppliedGeneration(ctx, tenant, "alpha", 7, "checksum-7", "healthy", "v2.0.0-beta.1"); err != nil {
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
			if got.LastAgentVersion != "v2.0.0-beta.1" {
				t.Fatalf("LastAgentVersion = %q, want %q", got.LastAgentVersion, "v2.0.0-beta.1")
			}
			if !got.LastSeen.Equal(seen) {
				t.Fatalf("LastSeen = %v, want %v", got.LastSeen, seen)
			}

			// A later report from a legacy (versionless) agent must NOT wipe the known version,
			// while still advancing the generation. Pins the empty-agentVersion guard in both impls.
			if err := s.SetAppliedGeneration(ctx, tenant, "alpha", 8, "checksum-8", "healthy", ""); err != nil {
				t.Fatalf("SetAppliedGeneration (empty version): %v", err)
			}
			got2, err := s.GetNode(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetNode (after empty version): %v", err)
			}
			if got2.LastAgentVersion != "v2.0.0-beta.1" {
				t.Fatalf("empty agentVersion must leave the stored version unchanged: got %q, want v2.0.0-beta.1", got2.LastAgentVersion)
			}
			if got2.AppliedGeneration != 8 {
				t.Fatalf("AppliedGeneration after second report = %d, want 8", got2.AppliedGeneration)
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
				TrustListJSON: []byte(`{"schema_version":1,"tenant":"compat-tenant","epoch":0,"members":[]}` + "\n"),
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

// TestStoreServedTrustList covers the SERVED (last-promoted) trust-list slot and the atomic
// GetServedConfig snapshot across both Store impls — the served-slot split that fixes the
// re-stage-bricks-the-fleet bug. Invariants:
//   - GetServedTrustList / GetServedConfig are ErrNotFound before anything is promoted.
//   - PromoteStaged copies a SIGNED staged trust-list into the served slot atomically with the
//     bundle flip; GetServedConfig then reports KeystoneOn + the served (bundle, trust-list) pair.
//   - A subsequent RE-STAGE (PutSignedTrustList of a new UNSIGNED manifest + StageBundle, no promote)
//     leaves the served slot UNTOUCHED — the staged slot diverges, the served slot does not. This is
//     the bug: before the split, the single slot was clobbered and /config served no signature.
//   - Promoting a bundle while the staged manifest is UNSIGNED leaves the served slot absent, so
//     GetServedConfig reports KeystoneOn but HasTrustList=false (the /config fail-closed case).
func TestStoreServedTrustList(t *testing.T) {
	for _, impl := range storeImpls() {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			ctx := context.Background()
			s := impl.factory(t)

			if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode: %v", err)
			}
			// Keystone ON: pin a credential so GetServedConfig reports KeystoneOn (its presence, not
			// its bytes, is what flips the flag).
			if err := s.SetOperatorCredential(ctx, tenant, OperatorCredential{
				Alg: "ed25519", PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA\n-----END PUBLIC KEY-----\n",
			}); err != nil {
				t.Fatalf("SetOperatorCredential: %v", err)
			}

			// Nothing promoted yet -> both served reads are ErrNotFound.
			if _, err := s.GetServedTrustList(ctx, tenant); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetServedTrustList(none): err = %v, want ErrNotFound", err)
			}
			if _, err := s.GetServedConfig(ctx, tenant, "alpha"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetServedConfig(none): err = %v, want ErrNotFound", err)
			}

			// Stage a bundle + a SIGNED staged trust-list (epoch 1), then promote.
			b1 := SignedBundle{NodeID: "alpha", Generation: 1, Files: map[string][]byte{"checksums.sha256": []byte("sum-v1")}, IsStaged: true}
			if err := s.StageBundle(ctx, tenant, b1); err != nil {
				t.Fatalf("StageBundle v1: %v", err)
			}
			signedTL := StoredTrustList{TrustListJSON: []byte(`{"epoch":1,"members":[{"node_id":"alpha"}]}` + "\n"), SignatureJSON: []byte(`{"alg":"ed25519","signature":"sig-v1"}`), Epoch: 1}
			if err := s.PutSignedTrustList(ctx, tenant, signedTL); err != nil {
				t.Fatalf("PutSignedTrustList v1: %v", err)
			}
			if _, err := s.PromoteStaged(ctx, tenant); err != nil {
				t.Fatalf("PromoteStaged v1: %v", err)
			}

			// Served slot now mirrors the promoted manifest.
			served, err := s.GetServedTrustList(ctx, tenant)
			if err != nil {
				t.Fatalf("GetServedTrustList after promote: %v", err)
			}
			if !bytes.Equal(served.TrustListJSON, signedTL.TrustListJSON) || !bytes.Equal(served.SignatureJSON, signedTL.SignatureJSON) || served.Epoch != 1 {
				t.Fatalf("served trust-list mismatch:\n got = %+v\nwant = %+v", served, signedTL)
			}
			sc, err := s.GetServedConfig(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetServedConfig after promote: %v", err)
			}
			if !sc.KeystoneOn || !sc.HasTrustList {
				t.Fatalf("GetServedConfig flags = KeystoneOn:%v HasTrustList:%v, want true/true", sc.KeystoneOn, sc.HasTrustList)
			}
			if sc.Bundle.Generation != 1 || string(sc.Bundle.Files["checksums.sha256"]) != "sum-v1" {
				t.Fatalf("GetServedConfig bundle = %+v, want gen 1 / sum-v1", sc.Bundle)
			}
			if !bytes.Equal(sc.TrustList.SignatureJSON, signedTL.SignatureJSON) {
				t.Fatalf("GetServedConfig trust-list sig = %q, want %q", sc.TrustList.SignatureJSON, signedTL.SignatureJSON)
			}

			// BUG #1 invariant: a RE-STAGE (new UNSIGNED manifest + new staged bundle, NO promote) must
			// NOT touch the served slot. The staged slot diverges; the served slot is frozen at v1.
			restaged := StoredTrustList{TrustListJSON: []byte(`{"epoch":2,"members":[{"node_id":"alpha"},{"node_id":"beta"}]}` + "\n"), Epoch: 2}
			if err := s.PutSignedTrustList(ctx, tenant, restaged); err != nil {
				t.Fatalf("PutSignedTrustList(re-stage unsigned): %v", err)
			}
			if err := s.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 2, Files: map[string][]byte{"checksums.sha256": []byte("sum-v2")}, IsStaged: true}); err != nil {
				t.Fatalf("StageBundle v2: %v", err)
			}
			staged, err := s.GetCurrentSignedTrustList(ctx, tenant)
			if err != nil {
				t.Fatalf("GetCurrentSignedTrustList(staged) after re-stage: %v", err)
			}
			if staged.Epoch != 2 || len(staged.SignatureJSON) != 0 {
				t.Fatalf("staged slot after re-stage = epoch %d sig-len %d, want epoch 2 unsigned", staged.Epoch, len(staged.SignatureJSON))
			}
			servedAfter, err := s.GetServedTrustList(ctx, tenant)
			if err != nil {
				t.Fatalf("GetServedTrustList after re-stage: %v", err)
			}
			if servedAfter.Epoch != 1 || !bytes.Equal(servedAfter.SignatureJSON, signedTL.SignatureJSON) {
				t.Fatalf("re-stage clobbered the served slot: got epoch %d, want frozen at epoch 1 (bug #1)", servedAfter.Epoch)
			}
			scAfter, err := s.GetServedConfig(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetServedConfig after re-stage: %v", err)
			}
			if !scAfter.HasTrustList || scAfter.Bundle.Generation != 1 {
				t.Fatalf("GetServedConfig after re-stage = HasTrustList:%v gen:%d, want true/1 (served still v1)", scAfter.HasTrustList, scAfter.Bundle.Generation)
			}

			// Completing the deploy: promote the (still unsigned, in this raw store-level test) staged
			// slot. PromoteStaged copies staged->served ONLY when the staged manifest is signed, so an
			// unsigned promote leaves the served trust-list ABSENT — GetServedConfig then reports
			// KeystoneOn but HasTrustList=false (the /config fail-closed case).
			s2 := impl.factory(t)
			if err := s2.SetOperatorCredential(ctx, tenant, OperatorCredential{Alg: "ed25519", PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA\n-----END PUBLIC KEY-----\n"}); err != nil {
				t.Fatalf("SetOperatorCredential s2: %v", err)
			}
			if err := s2.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
				t.Fatalf("UpsertNode s2: %v", err)
			}
			if err := s2.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 1, Files: map[string][]byte{"checksums.sha256": []byte("x")}, IsStaged: true}); err != nil {
				t.Fatalf("StageBundle s2: %v", err)
			}
			if err := s2.PutSignedTrustList(ctx, tenant, StoredTrustList{TrustListJSON: []byte(`{"epoch":1}` + "\n"), Epoch: 1}); err != nil {
				t.Fatalf("PutSignedTrustList(unsigned) s2: %v", err)
			}
			if _, err := s2.PromoteStaged(ctx, tenant); err != nil {
				t.Fatalf("PromoteStaged(unsigned) s2: %v", err)
			}
			scUnsigned, err := s2.GetServedConfig(ctx, tenant, "alpha")
			if err != nil {
				t.Fatalf("GetServedConfig s2: %v", err)
			}
			if !scUnsigned.KeystoneOn || scUnsigned.HasTrustList {
				t.Fatalf("unsigned promote: GetServedConfig = KeystoneOn:%v HasTrustList:%v, want true/false", scUnsigned.KeystoneOn, scUnsigned.HasTrustList)
			}
		})
	}
}

// TestFileStoreServedConfigSurvivesGenerationLagCrash simulates a FileStore crash mid-PromoteStaged
// AFTER the per-node current-bundle and served_trustlist.json atomic renames but BEFORE the final
// generation.json commit (generation.json is written LAST). /config reads via GetServedConfig, which
// is NOT gated on the generation counter, so on reopen the node still gets a COHERENT
// (new-bundle, new-served-manifest) pair; only the counter lags (it self-heals on the next
// promote/bump). This pins the "generation committed last, /config independent of it" ordering
// invariant the PromoteStaged comment documents. The OTHER crash shape (a node's new bundle paired
// with an OLD served manifest, from a kill mid per-node loop) is covered by the agent's offline
// digest-binding rejection in the regression suite — the store faithfully returns whatever is on
// disk; the agent fails closed on the mismatch.
func TestFileStoreServedConfigSurvivesGenerationLagCrash(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := s.SetOperatorCredential(ctx, tenant, OperatorCredential{Alg: "ed25519", PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA\n-----END PUBLIC KEY-----\n"}); err != nil {
		t.Fatalf("SetOperatorCredential: %v", err)
	}
	if err := s.UpsertNode(ctx, tenant, Node{NodeID: "alpha", Status: NodeApproved}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	if err := s.StageBundle(ctx, tenant, SignedBundle{NodeID: "alpha", Generation: 1, Files: map[string][]byte{"checksums.sha256": []byte("sum-v1")}, IsStaged: true}); err != nil {
		t.Fatalf("StageBundle: %v", err)
	}
	signed := StoredTrustList{TrustListJSON: []byte(`{"epoch":1}` + "\n"), SignatureJSON: []byte(`{"alg":"ed25519","signature":"s"}`), Epoch: 1}
	if err := s.PutSignedTrustList(ctx, tenant, signed); err != nil {
		t.Fatalf("PutSignedTrustList: %v", err)
	}
	if _, err := s.PromoteStaged(ctx, tenant); err != nil {
		t.Fatalf("PromoteStaged: %v", err)
	}

	// Simulate the crash: roll generation.json back to the PRIOR counter (0), as if the process died
	// after the bundle + served_trustlist atomic renames but before the final generation commit.
	genPath := filepath.Join(dir, string(tenant), "generation.json")
	rolled, err := json.Marshal(generationFile{Generation: 0})
	if err != nil {
		t.Fatalf("marshal generationFile: %v", err)
	}
	if err := os.WriteFile(genPath, rolled, 0600); err != nil {
		t.Fatalf("roll back generation.json: %v", err)
	}

	// Reopen (crash + restart) and assert /config is still coherent even though the counter lags.
	s2, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if gen, err := s2.CurrentGeneration(ctx, tenant); err != nil || gen != 0 {
		t.Fatalf("counter should read the lagged 0 (the crash dropped the commit) = (%d, %v)", gen, err)
	}
	sc, err := s2.GetServedConfig(ctx, tenant, "alpha")
	if err != nil {
		t.Fatalf("GetServedConfig after reopen: %v", err)
	}
	if !sc.HasTrustList || sc.Bundle.Generation != 1 || string(sc.Bundle.Files["checksums.sha256"]) != "sum-v1" {
		t.Fatalf("post-crash /config must be coherent (new bundle + served manifest); got HasTrustList=%v gen=%d", sc.HasTrustList, sc.Bundle.Generation)
	}
	if !bytes.Equal(sc.TrustList.SignatureJSON, signed.SignatureJSON) {
		t.Fatalf("post-crash served manifest signature mismatch: the (bundle, manifest) pair must stay coherent")
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
