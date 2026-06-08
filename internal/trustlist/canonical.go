package trustlist

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// Canonical produces the canonical byte serialization of a trust list.
//
// These bytes ARE the trustlist.json artifact AND the exact payload that is
// signed and verified — Sign/Verify and the WebAuthn content binding all operate
// on this output, so its determinism is load-bearing for security.
//
// Determinism rules:
//   - Members are copied and sorted by NodeID in byte order, so the same logical
//     trust list yields identical bytes regardless of input member order.
//   - A duplicate NodeID is rejected (returns an error). Duplicates would make
//     "membership" ambiguous and are a signing-time mistake, never something a
//     verifier should silently tolerate.
//   - Encoding uses a json.Encoder with HTML escaping DISABLED (so '<', '>', '&'
//     are not turned into < etc.), in struct field declaration order.
//   - The output ends with exactly one trailing '\n' (the encoder emits one).
func Canonical(tl TrustList) ([]byte, error) {
	// Copy members so we never mutate the caller's slice, then sort by NodeID.
	members := make([]Member, len(tl.Members))
	copy(members, tl.Members)
	sort.Slice(members, func(i, j int) bool {
		return members[i].NodeID < members[j].NodeID
	})

	// Reject duplicate node IDs. After sorting, duplicates are adjacent.
	for i := 1; i < len(members); i++ {
		if members[i].NodeID == members[i-1].NodeID {
			return nil, fmt.Errorf("trustlist: duplicate node_id %q", members[i].NodeID)
		}
	}

	out := tl
	out.Members = members

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Disable HTML escaping so the signed bytes are the literal JSON, not an
	// escaped variant. json.Encoder.Encode appends exactly one trailing '\n'.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, fmt.Errorf("trustlist: encode canonical: %w", err)
	}
	return buf.Bytes(), nil
}

// Challenge is the WebAuthn challenge bound to a trust list: SHA-256 of the
// canonical bytes. The signer presents base64url(Challenge(tl)) as the
// navigator.credentials.get challenge, and the verifier recomputes it from the
// trust list it actually holds, so a valid assertion proves the user authorized
// THESE bytes — the content binding that defends against trust-list swaps.
func Challenge(tl TrustList) ([]byte, error) {
	c, err := Canonical(tl)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(c)
	return sum[:], nil
}
