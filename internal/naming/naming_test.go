package naming

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestSafeInstallerFileName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple lowercase", "alpha", "alpha.install.sh"},
		{"uppercase to lowercase", "Alpha", "alpha.install.sh"},
		{"spaces to hyphens", "Web 1", "web-1.install.sh"},
		{"already hyphenated", "web-1", "web-1.install.sh"},
		{"special chars and folding", "Edge Router", "edge-router.install.sh"},
		{"all-special falls back to node", "  ***  ", "node.install.sh"},
		{"underscores preserved", "my_server", "my_server.install.sh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeInstallerFileName(tt.input)
			if got != tt.expected {
				t.Errorf("SafeInstallerFileName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestSafeInstallerFileNameCollision verifies that the two distinct original
// names cited in Spec D ("Web 1" and "web-1") normalize to the same installer
// script filename -- precisely the collision case that the N2 uniqueness
// invariant requires semantic validation to catch.
func TestSafeInstallerFileNameCollision(t *testing.T) {
	a := SafeInstallerFileName("Web 1")
	b := SafeInstallerFileName("web-1")
	if a != b {
		t.Fatalf("expected %q and %q to normalize to the same filename, got %q != %q", "Web 1", "web-1", a, b)
	}
	if a != "web-1.install.sh" {
		t.Fatalf("collision result should be %q, got %q", "web-1.install.sh", a)
	}
}

func TestWgInterfaceName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"short name", "alpha", "wg-alpha"},
		{"another short name", "beta", "wg-beta"},
		{"uppercase to lowercase", "Alpha", "wg-alpha"},
		{"underscores to hyphens", "my_server", "wg-my-server"},
		{"dots to hyphens", "db.east", "wg-db-east"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WgInterfaceName(tt.input)
			if got != tt.expected {
				t.Errorf("WgInterfaceName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
			if len(got) > 15 {
				t.Errorf("WgInterfaceName(%q) = %q exceeds 15 characters", tt.input, got)
			}
		})
	}
}

// TestWgInterfaceNameLongHashBranch verifies that a name exceeding 15 characters
// (>12 characters after cleaning) takes the hash-suffix branch, and that the
// output is byte-for-byte identical to the algorithm definition: the wg- prefix
// + clean[:8] + sha256(name)[:4]. The expected hash fragment is computed
// independently in the test to pin down the implementation behavior.
func TestWgInterfaceNameLongHashBranch(t *testing.T) {
	const input = "my-long-server-name"

	// Recompute the expected value independently: after cleaning, every
	// character of this name is valid, length 19, "wg-"+clean = 22 > 15, so it
	// takes the long path: wg- + clean[:8] + sha256(input)[:4].
	clean := "my-long-server-name"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(input)))
	expected := "wg-" + clean[:8] + hash[:4]

	got := WgInterfaceName(input)
	if got != expected {
		t.Fatalf("WgInterfaceName(%q) = %q, want hash-branch output %q", input, got, expected)
	}
	if len(got) != 15 {
		t.Fatalf("hash-branch output should be exactly 15 characters, got %q (len=%d)", got, len(got))
	}
}

// TestWgInterfaceNamePinnedLongName pins a long-name case that was already
// fixed in the historical implementation, ensuring behavior is completely
// unchanged after the migration out of internal/compiler.
func TestWgInterfaceNamePinnedLongName(t *testing.T) {
	got := WgInterfaceName("abcdefghijklmnop")
	const expected = "wg-abcdefghf39d"
	if got != expected {
		t.Fatalf("WgInterfaceName(%q) = %q, want %q", "abcdefghijklmnop", got, expected)
	}
}

// TestWgInterfaceNameForEdgePrimaryByteIdentical verifies that when backup ==
// false, the edge-aware interface name is byte-for-byte identical to
// WgInterfaceName(remoteName) -- including short names, names that need
// cleaning, and names that take the long hash path (>12 characters after
// cleaning). Deployed clusters therefore see zero interface renames.
func TestWgInterfaceNameForEdgePrimaryByteIdentical(t *testing.T) {
	names := []string{
		"alpha",               // short path
		"my_server",           // needs cleaning (underscore -> hyphen)
		"db.east",             // needs cleaning (dot -> hyphen)
		"my-long-server-name", // long path, takes hash suffix
		"abcdefghijklmnop",    // historical pinned long name
	}
	for _, n := range names {
		// edgeID must be ignored on the primary path, so pass a non-empty value
		// to confirm it does not affect the result.
		got := WgInterfaceNameForEdge(n, "some-edge-id", false)
		want := WgInterfaceName(n)
		if got != want {
			t.Errorf("primary path should be byte-for-byte identical to WgInterfaceName: WgInterfaceNameForEdge(%q,_,false) = %q, want %q", n, got, want)
		}
		if len(got) > 15 {
			t.Errorf("WgInterfaceNameForEdge(%q,_,false) = %q exceeds 15 characters", n, got)
		}
	}
}

// TestWgInterfaceNameForEdgeBackupShape verifies that when backup == true the
// long-path shape is taken unconditionally: wg- + clean[:8] +
// sha256(remoteName+"|"+edgeID)[:4], and is exactly 15 characters. Even when
// remoteName is short (the primary path would take the short path), the backup
// path still takes the hash suffix.
func TestWgInterfaceNameForEdgeBackupShape(t *testing.T) {
	const remote = "alpha"
	const edgeID = "edge-1"

	clean := "alpha"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(remote+"|"+edgeID)))
	// clean has length 5 < 8, so clean[:8] is bounded by the actual length and
	// yields the whole of "alpha".
	expected := "wg-" + clean + hash[:4]

	got := WgInterfaceNameForEdge(remote, edgeID, true)
	if got != expected {
		t.Fatalf("WgInterfaceNameForEdge(%q,%q,true) = %q, want %q", remote, edgeID, got, expected)
	}
}

// TestWgInterfaceNameForEdgeBackupDistinct verifies that two backup edges
// toward the same remote produce different interface names because their edge
// IDs differ (the 4-hex-digit suffix diverges).
func TestWgInterfaceNameForEdgeBackupDistinct(t *testing.T) {
	const remote = "gateway-node"
	a := WgInterfaceNameForEdge(remote, "edge-a", true)
	b := WgInterfaceNameForEdge(remote, "edge-b", true)
	if a == b {
		t.Fatalf("two backup interface names with the same remote but different edge IDs should differ, both were %q", a)
	}
	// The backup interface name must also differ from the primary interface name.
	if a == WgInterfaceName(remote) {
		t.Fatalf("backup interface name should not equal the primary interface name: both were %q", a)
	}
}

// TestWgInterfaceNameForEdgeLengthBound verifies that the backup path stays
// within 15 characters for both short and long names (the hash shape's budget
// ceiling is exactly 3 + 8 + 4 = 15).
func TestWgInterfaceNameForEdgeLengthBound(t *testing.T) {
	cases := []struct {
		remote string
		edgeID string
	}{
		// One example each of a very short, a short, and a long remote name.
		{"a", "e1"},
		{"alpha", "edge-1"},
		{"my-long-server-name-that-is-very-long", "edge-xyz-123"},
	}
	for _, c := range cases {
		got := WgInterfaceNameForEdge(c.remote, c.edgeID, true)
		if len(got) > 15 {
			t.Errorf("WgInterfaceNameForEdge(%q,%q,true) = %q exceeds 15 characters (len=%d)", c.remote, c.edgeID, got, len(got))
		}
	}
}

// TestWgInterfaceNameForEdgeDeterminism verifies that the same input always
// produces the same output (compilation should be reproducible).
func TestWgInterfaceNameForEdgeDeterminism(t *testing.T) {
	const remote = "edge-router"
	const edgeID = "edge-42"
	first := WgInterfaceNameForEdge(remote, edgeID, true)
	for i := 0; i < 5; i++ {
		if got := WgInterfaceNameForEdge(remote, edgeID, true); got != first {
			t.Fatalf("the same input should deterministically produce the same interface name: iteration %d got %q, first was %q", i, got, first)
		}
	}
}
