package normalize

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

func edgeByID(edges []model.Edge, id string) model.Edge {
	for _, e := range edges {
		if e.ID == id {
			return e
		}
	}
	return model.Edge{}
}

func hasAnyPin(e model.Edge) bool {
	return e.CompiledPort != 0 || e.PinnedFromPort != 0 || e.PinnedToPort != 0 ||
		e.PinnedFromTransitIP != "" || e.PinnedToTransitIP != "" ||
		e.PinnedFromLinkLocal != "" || e.PinnedToLinkLocal != ""
}

// TestHealCollidingPins covers every collision shape and every skip rule in one topology:
//   - cross-pair transit collision (the user's case): a-b vs b-c both pin .1/.2 -> b-c stripped;
//   - backup vs same-pair primary: a-d primary vs a-d backup both pin .5/.6 -> the backup stripped
//     (distinct link identity via #id);
//   - mirrored reverse primary (same link): a-b and its reverse b-a carry the SAME values -> kept;
//   - disabled colliding edge -> skipped (kept, neither checked nor claimed);
//   - client-touched colliding edge -> skipped;
//
// and confirms the heal is idempotent (a second pass reports no change).
func TestHealCollidingPins(t *testing.T) {
	topo := &model.Topology{
		Nodes: []model.Node{
			{ID: "a", Role: "router"},
			{ID: "b", Role: "router"},
			{ID: "c", Role: "router"},
			{ID: "d", Role: "router"},
			{ID: "cl", Role: "client"},
		},
		Edges: []model.Edge{
			// First claimant of .1/.2 + port 51820: kept.
			{ID: "a-b", FromNodeID: "a", ToNodeID: "b", IsEnabled: true,
				PinnedFromPort: 51820, PinnedToPort: 51820,
				PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2",
				PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2"},
			// Mirrored reverse of a-b (same linkKey): same values, must be KEPT.
			{ID: "b-a", FromNodeID: "b", ToNodeID: "a", IsEnabled: true,
				PinnedFromPort: 51820, PinnedToPort: 51820,
				PinnedFromTransitIP: "10.10.0.2", PinnedToTransitIP: "10.10.0.1",
				PinnedFromLinkLocal: "fe80::2", PinnedToLinkLocal: "fe80::1"},
			// Cross-pair collision with a-b on transit .1/.2: STRIPPED.
			{ID: "b-c", FromNodeID: "b", ToNodeID: "c", IsEnabled: true,
				PinnedFromPort: 51821, PinnedToPort: 51821,
				PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2",
				PinnedFromLinkLocal: "fe80::1", PinnedToLinkLocal: "fe80::2"},
			// Primary a-d claims .5/.6: kept.
			{ID: "a-d", FromNodeID: "a", ToNodeID: "d", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.5", PinnedToTransitIP: "10.10.0.6"},
			// Backup a-d (distinct link via #id) collides with the primary's .5/.6: STRIPPED.
			{ID: "a-d-bk", FromNodeID: "a", ToNodeID: "d", Role: "backup", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.5", PinnedToTransitIP: "10.10.0.6"},
			// Disabled edge colliding on .1/.2: SKIPPED (kept, never checked/claimed).
			{ID: "dis", FromNodeID: "c", ToNodeID: "d", IsEnabled: false,
				PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2"},
			// Client-touched edge colliding on .1/.2: SKIPPED.
			{ID: "cli", FromNodeID: "cl", ToNodeID: "c", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2"},
		},
	}

	if changed := HealCollidingPins(topo); !changed {
		t.Fatalf("HealCollidingPins reported no change, expected it to strip colliders")
	}

	kept := map[string]bool{"a-b": true, "b-a": true, "a-d": true, "dis": true, "cli": true}
	stripped := map[string]bool{"b-c": true, "a-d-bk": true}
	for id := range kept {
		if !hasAnyPin(edgeByID(topo.Edges, id)) {
			t.Errorf("edge %q lost its pins but should have been kept", id)
		}
	}
	for id := range stripped {
		if hasAnyPin(edgeByID(topo.Edges, id)) {
			t.Errorf("edge %q kept pins but should have been stripped (colliding)", id)
		}
	}

	// Idempotent + converged: a second pass finds nothing to strip.
	if changed := HealCollidingPins(topo); changed {
		t.Errorf("second HealCollidingPins reported a change; heal is not idempotent/converged")
	}
}

// TestHealCollidingPins_NoChange confirms a clean topology is returned untouched (false), so the
// write path never burns a topology version on a no-op normalization.
func TestHealCollidingPins_NoChange(t *testing.T) {
	topo := &model.Topology{
		Nodes: []model.Node{{ID: "a", Role: "router"}, {ID: "b", Role: "router"}, {ID: "c", Role: "router"}},
		Edges: []model.Edge{
			{ID: "a-b", FromNodeID: "a", ToNodeID: "b", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.1", PinnedToTransitIP: "10.10.0.2"},
			{ID: "b-c", FromNodeID: "b", ToNodeID: "c", IsEnabled: true,
				PinnedFromTransitIP: "10.10.0.3", PinnedToTransitIP: "10.10.0.4"},
		},
	}
	if HealCollidingPins(topo) {
		t.Errorf("HealCollidingPins reported a change on a collision-free topology")
	}
}
