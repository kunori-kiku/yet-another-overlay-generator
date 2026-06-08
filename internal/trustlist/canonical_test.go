package trustlist

import (
	"bytes"
	"encoding/json"
	"testing"
)

// sampleTL returns a representative trust list with members in a deliberately
// non-sorted order, so canonicalization's sort is exercised.
func sampleTL() TrustList {
	return TrustList{
		SchemaVersion: 1,
		Tenant:        "acme",
		Epoch:         7,
		Members: []Member{
			{NodeID: "gamma", WGPublicKey: "GGG=", BundleSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
			{NodeID: "alpha", WGPublicKey: "AAA=", BundleSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{NodeID: "beta", WGPublicKey: "BBB=", BundleSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
}

// TestCanonicalDeterministic: the same trust list yields byte-identical output
// across repeated calls.
func TestCanonicalDeterministic(t *testing.T) {
	base, err := Canonical(sampleTL())
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	for i := 0; i < 50; i++ {
		got, err := Canonical(sampleTL())
		if err != nil {
			t.Fatalf("Canonical call %d: %v", i, err)
		}
		if !bytes.Equal(got, base) {
			t.Fatalf("call %d differs from base output", i)
		}
	}
	// Exactly one trailing newline.
	if len(base) == 0 || base[len(base)-1] != '\n' {
		t.Fatalf("canonical output must end with a trailing newline")
	}
	if len(base) >= 2 && base[len(base)-2] == '\n' {
		t.Fatalf("canonical output must end with EXACTLY one trailing newline")
	}
}

// TestCanonicalOrderIndependent: members supplied in different orders produce
// identical canonical bytes (the sort is what makes this true).
func TestCanonicalOrderIndependent(t *testing.T) {
	a := sampleTL()
	b := sampleTL()
	// Reverse b's members.
	for i, j := 0, len(b.Members)-1; i < j; i, j = i+1, j-1 {
		b.Members[i], b.Members[j] = b.Members[j], b.Members[i]
	}

	ca, err := Canonical(a)
	if err != nil {
		t.Fatalf("Canonical(a): %v", err)
	}
	cb, err := Canonical(b)
	if err != nil {
		t.Fatalf("Canonical(b): %v", err)
	}
	if !bytes.Equal(ca, cb) {
		t.Fatalf("order-dependent output:\n a=%q\n b=%q", ca, cb)
	}
}

// TestCanonicalDoesNotMutateInput: Canonical copies the members slice; the
// caller's slice order is preserved.
func TestCanonicalDoesNotMutateInput(t *testing.T) {
	tl := sampleTL()
	before := make([]Member, len(tl.Members))
	copy(before, tl.Members)
	if _, err := Canonical(tl); err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	for i := range before {
		if tl.Members[i] != before[i] {
			t.Fatalf("Canonical mutated caller's members slice at %d", i)
		}
	}
}

// TestCanonicalDuplicateNodeID: a duplicate node_id is rejected.
func TestCanonicalDuplicateNodeID(t *testing.T) {
	tl := sampleTL()
	tl.Members = append(tl.Members, Member{NodeID: "alpha", WGPublicKey: "ZZZ="})
	if _, err := Canonical(tl); err == nil {
		t.Fatalf("expected error for duplicate node_id")
	}
}

// TestCanonicalRoundTrip: the emitted JSON parses back into an equivalent
// (member-sorted) TrustList.
func TestCanonicalRoundTrip(t *testing.T) {
	c, err := Canonical(sampleTL())
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	var parsed TrustList
	if err := json.Unmarshal(c, &parsed); err != nil {
		t.Fatalf("unmarshal canonical JSON: %v", err)
	}
	// Re-canonicalize the parsed value; it must match the original bytes.
	re, err := Canonical(parsed)
	if err != nil {
		t.Fatalf("re-Canonical: %v", err)
	}
	if !bytes.Equal(c, re) {
		t.Fatalf("round-trip not stable:\n orig=%q\n re  =%q", c, re)
	}
}

// TestCanonicalFieldChangesDiffer: changing any signed field changes the bytes.
func TestCanonicalFieldChangesDiffer(t *testing.T) {
	base, err := Canonical(sampleTL())
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	cases := map[string]func(tl *TrustList){
		"member wg key":  func(tl *TrustList) { tl.Members[0].WGPublicKey = "CHANGED=" },
		"member node id": func(tl *TrustList) { tl.Members[0].NodeID = "alpha2" },
		"member bundle digest": func(tl *TrustList) {
			tl.Members[0].BundleSHA256 = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		},
		"add member": func(tl *TrustList) {
			tl.Members = append(tl.Members, Member{NodeID: "delta", WGPublicKey: "DDD=", BundleSHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"})
		},
		"epoch":          func(tl *TrustList) { tl.Epoch = 8 },
		"tenant":         func(tl *TrustList) { tl.Tenant = "other" },
		"schema version": func(tl *TrustList) { tl.SchemaVersion = 2 },
	}
	for name, mut := range cases {
		tl := sampleTL()
		mut(&tl)
		got, err := Canonical(tl)
		if err != nil {
			t.Fatalf("%s: Canonical: %v", name, err)
		}
		if bytes.Equal(got, base) {
			t.Fatalf("%s: canonical bytes unchanged after mutation", name)
		}
	}
}

// TestChallengeMatchesHash: Challenge is SHA-256 of Canonical, and changes with
// content.
func TestChallengeMatchesHash(t *testing.T) {
	c1, err := Challenge(sampleTL())
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	if len(c1) != 32 {
		t.Fatalf("Challenge length = %d, want 32", len(c1))
	}
	tl := sampleTL()
	tl.Epoch = 99
	c2, err := Challenge(tl)
	if err != nil {
		t.Fatalf("Challenge: %v", err)
	}
	if bytes.Equal(c1, c2) {
		t.Fatalf("Challenge did not change with content")
	}
}
