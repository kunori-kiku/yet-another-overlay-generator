package controller

// compile_manualnode.go — manual-node identity validation (validateManualNodes and the
// topology-half collision check manualKeyConflict). Split from compile.go (plan-2);
// no logic change.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/apierr"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/validator"
)

// validateManualNodes rejects a stage/preview whose topology carries a MANUAL (deployment_mode=manual,
// hand-deployed, agent-less) node that is not deployable. A manual node is admitted from its
// OPERATOR-ASSERTED topology public key (no enrollment token proves it), so the controller validates
// that asserted identity here — before it is rendered into managed peers' bundles AND bound into the
// off-host-signed membership manifest:
//
//   - it MUST carry a WireGuard public key (without one, enrolledSubgraph would silently exclude it;
//     surfacing a clear error is the plan-1 deferred rule, now in its correct controller-side home —
//     the shared pre-keygen validator can't host it because a LOCAL-mode manual node legitimately has
//     no key until compile generates one);
//   - that key MUST be unique across the fleet: not duplicating another manual node's, and not
//     colliding with an enrolled node's registry key — the same one-pubkey-one-node invariant
//     CheckWGKeyUnique enforces for enrolling managed nodes, extended across the manual+enrolled split
//     so a manual node can never claim (or be confused with) an enrolled node's identity.
//
// A managed node carrying a stray deployment_mode is not affected (IsManual gates on exactly "manual").
func validateManualNodes(topo *model.Topology, nodes []Node) error {
	// Enrolled public key -> node ID, for the cross-source (manual-vs-enrolled) collision check.
	enrolledByKey := make(map[string]string, len(nodes))
	for _, n := range nodes {
		// Trim the enrolled key too (symmetry with the manual side + CheckWGKeyUnique), so a padded
		// registry key still matches a clean manual key of the same value.
		if n.Status == NodeApproved {
			if k := strings.TrimSpace(n.WGPublicKey); k != "" {
				enrolledByKey[k] = n.NodeID
			}
		}
	}
	manualByKey := make(map[string]string)
	for i := range topo.Nodes {
		node := &topo.Nodes[i]
		if !node.IsManual() {
			continue
		}
		// Identify the offending node by its stable, unique ID (a name may be empty or duplicated).
		// Whitespace-insensitive comparison, matching CheckWGKeyUnique (a padded key cannot evade the
		// gate, and would also break the rendered WG config).
		key := strings.TrimSpace(node.WireGuardPublicKey)
		if key == "" {
			return apierr.New(apierr.CodeManualNodeInvalid).With("node", node.ID).
				With("detail", "no WireGuard public key — a manual node is hand-deployed, so it must carry its own pre-known public key")
		}
		// The manual key is operator-asserted and rendered VERBATIM (the raw, untrimmed value flows to
		// the peer config), so validate the raw key: a valid Curve25519 key has no surrounding
		// whitespace, and bad base64 / wrong length / an embedded newline is rejected. Same source of
		// truth as the schema validator + enroll/rekey.
		if !validator.ValidWGPublicKey(node.WireGuardPublicKey) {
			return apierr.New(apierr.CodeManualNodeInvalid).With("node", node.ID).
				With("detail", "its WireGuard public key is not a valid base64/32-byte Curve25519 key")
		}
		if other, ok := enrolledByKey[key]; ok {
			return apierr.New(apierr.CodeManualNodeInvalid).With("node", node.ID).
				With("detail", fmt.Sprintf("its WireGuard public key collides with enrolled node %s", other))
		}
		if other, ok := manualByKey[key]; ok {
			return apierr.New(apierr.CodeManualNodeInvalid).With("node", node.ID).
				With("detail", fmt.Sprintf("its WireGuard public key duplicates manual node %s", other))
		}
		manualByKey[key] = node.ID
	}
	return nil
}

// manualKeyConflict reports a conflict when a MANUAL node in the stored topology (other than
// selfNodeID) already claims wgPubKey. It is the TOPOLOGY half of the cross-source one-pubkey-one-node
// invariant; CheckWGKeyUnique (the registry half) calls it so a node can never enroll/rekey to a key a
// manual node already holds (the enrolled→manual direction; validateManualNodes covers manual→enrolled).
// A missing topology or empty key is never a conflict. Whitespace-insensitive, matching CheckWGKeyUnique.
func manualKeyConflict(ctx context.Context, store Store, t TenantID, wgPubKey, selfNodeID string) (string, error) {
	key := strings.TrimSpace(wgPubKey)
	if key == "" {
		return "", nil
	}
	rec, err := store.GetTopology(ctx, t)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("controller: loading topology for manual-key dedupe: %w", err)
	}
	var topo model.Topology
	if err := json.Unmarshal(rec.JSON, &topo); err != nil {
		return "", fmt.Errorf("controller: parsing topology for manual-key dedupe: %w", err)
	}
	for i := range topo.Nodes {
		n := &topo.Nodes[i]
		if n.IsManual() && n.ID != selfNodeID && strings.TrimSpace(n.WireGuardPublicKey) == key {
			return n.ID, ErrDuplicateWGKey
		}
	}
	return "", nil
}
