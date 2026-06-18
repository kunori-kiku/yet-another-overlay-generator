package linkid

import (
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestPinKeySymmetry verifies that PinKey is direction-independent: PinKey(A, B) == PinKey(B, A).
func TestPinKeySymmetry(t *testing.T) {
	pairs := [][2]string{
		{"alpha", "beta"},
		{"beta", "alpha"},
		{"node-1", "node-2"},
		{"zzz", "aaa"},
	}
	for _, p := range pairs {
		fwd := PinKey(p[0], p[1])
		rev := PinKey(p[1], p[0])
		if fwd != rev {
			t.Errorf("PinKey should be direction-independent: PinKey(%q,%q)=%q != PinKey(%q,%q)=%q",
				p[0], p[1], fwd, p[1], p[0], rev)
		}
	}
}

// TestPinKeyOrder verifies PinKey's concatenation order: the smaller node ID comes first,
// separated by a pipe.
func TestPinKeyOrder(t *testing.T) {
	tests := []struct {
		a, b, expected string
	}{
		{"alpha", "beta", "alpha|beta"},
		{"beta", "alpha", "alpha|beta"},
		{"a", "b", "a|b"},
		{"b", "a", "a|b"},
		// Equal inputs: min == max, still concatenated with itself via a pipe.
		{"x", "x", "x|x"},
	}
	for _, tt := range tests {
		got := PinKey(tt.a, tt.b)
		if got != tt.expected {
			t.Errorf("PinKey(%q,%q) = %q, want %q", tt.a, tt.b, got, tt.expected)
		}
	}
}

// TestLinkKeyPrimaryReduction verifies that a primary-class edge (role empty or "primary")
// has its LinkKey reduce to PinKey -- this is the no-drift guarantee for single-edge node
// pairs relative to the pre-parallel-links compiler. Two primary-class edges of the same
// node pair (including the reverse) share the same LinkKey.
func TestLinkKeyPrimaryReduction(t *testing.T) {
	want := PinKey("alpha", "beta")

	roleless := &model.Edge{ID: "e1", FromNodeID: "alpha", ToNodeID: "beta"}
	if got := LinkKey(roleless); got != want {
		t.Errorf("LinkKey for an empty role should reduce to PinKey: got %q, want %q", got, want)
	}

	explicitPrimary := &model.Edge{ID: "e2", FromNodeID: "alpha", ToNodeID: "beta", Role: model.EdgeRolePrimary}
	if got := LinkKey(explicitPrimary); got != want {
		t.Errorf("LinkKey for an explicit primary should reduce to PinKey: got %q, want %q", got, want)
	}

	// A reverse primary-class edge should share the same LinkKey as the forward one (direction-independent).
	reverse := &model.Edge{ID: "e3", FromNodeID: "beta", ToNodeID: "alpha"}
	if got := LinkKey(reverse); got != want {
		t.Errorf("LinkKey for a reverse primary-class edge should match the forward one: got %q, want %q", got, want)
	}
}

// TestLinkKeyBackupDiscrimination verifies that each backup edge's LinkKey carries its own
// edge ID, so within the same node pair: two backups differ from each other, and a backup
// differs from a primary.
func TestLinkKeyBackupDiscrimination(t *testing.T) {
	pair := PinKey("alpha", "beta")

	backup1 := &model.Edge{ID: "b1", FromNodeID: "alpha", ToNodeID: "beta", Role: model.EdgeRoleBackup}
	backup2 := &model.Edge{ID: "b2", FromNodeID: "alpha", ToNodeID: "beta", Role: model.EdgeRoleBackup}

	want1 := pair + "#b1"
	if got := LinkKey(backup1); got != want1 {
		t.Errorf("LinkKey for a backup edge should be PinKey#ID: got %q, want %q", got, want1)
	}

	if LinkKey(backup1) == LinkKey(backup2) {
		t.Errorf("two backups of the same node pair should have different LinkKeys: both are %q", LinkKey(backup1))
	}

	primary := &model.Edge{ID: "p1", FromNodeID: "alpha", ToNodeID: "beta"}
	if LinkKey(primary) == LinkKey(backup1) {
		t.Errorf("a backup and a primary should not share a LinkKey: both are %q", LinkKey(primary))
	}

	// Even if a backup is the node pair's only edge, it is still discriminated by edge ID (identity-no-drift guarantee).
	soleBackup := &model.Edge{ID: "only", FromNodeID: "gamma", ToNodeID: "delta", Role: model.EdgeRoleBackup}
	if got := LinkKey(soleBackup); got == PinKey("gamma", "delta") {
		t.Errorf("a sole backup edge should still be discriminated by edge ID and must not reduce to a bare PinKey: got %q", got)
	}
}

// TestIsBackup verifies the role check: only "backup" is true; empty and "primary" are false.
func TestIsBackup(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{"", false},
		{model.EdgeRolePrimary, false},
		{model.EdgeRoleBackup, true},
	}
	for _, tt := range tests {
		e := &model.Edge{ID: "e", FromNodeID: "a", ToNodeID: "b", Role: tt.role}
		if got := IsBackup(e); got != tt.want {
			t.Errorf("IsBackup(role=%q) = %v, want %v", tt.role, got, tt.want)
		}
	}
}
