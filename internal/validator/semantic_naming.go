package validator

import (
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

func validateIDUniqueness(topo *model.Topology, result *ValidationResult) {
	// Domain IDs.
	domainIDs := make(map[string]bool)
	for i, d := range topo.Domains {
		if domainIDs[d.ID] {
			result.AddError(fmt.Sprintf("domains[%d].id", i), CodeDomainIDDuplicate, P{"id", d.ID})
		}
		domainIDs[d.ID] = true
	}

	// Node IDs. Exact duplicates are invalid everywhere; case-folded duplicates are additionally
	// invalid because the canonical bundle directories must survive Windows extraction unchanged.
	nodeIDs := make(map[string]bool)
	portableNodeIDs := make(map[string]string)
	for i, n := range topo.Nodes {
		if nodeIDs[n.ID] {
			result.AddError(fmt.Sprintf("nodes[%d].id", i), CodeNodeIDDuplicate, P{"id", n.ID})
		}
		nodeIDs[n.ID] = true
		if n.ID == "" {
			continue
		}
		key := naming.PortableNodeIDKey(n.ID)
		if firstID, exists := portableNodeIDs[key]; exists && firstID != n.ID {
			result.AddError(fmt.Sprintf("nodes[%d].id", i), CodeNodeIDPortableCollision, P{"other", firstID}, P{"id", n.ID})
		} else {
			portableNodeIDs[key] = n.ID
		}
	}

	// Edge IDs.
	edgeIDs := make(map[string]bool)
	for i, e := range topo.Edges {
		if edgeIDs[e.ID] {
			result.AddError(fmt.Sprintf("edges[%d].id", i), CodeEdgeIDDuplicate, P{"id", e.ID})
		}
		edgeIDs[e.ID] = true
	}
}

// validateNodeNameCollisions checks the two name-derived identities that remain in the artifact
// contract. Bundle directories use node IDs and therefore need no phantom installer-filename rule.
// If any two distinct nodes collide in any one of these forms, the name-derived artifacts will
// overwrite one another or be silently skipped:
//   - Raw name (N1): operators and every name-derived artifact cannot tell two same-named nodes apart.
//   - WireGuard interface name WgInterfaceName (N3): identical interface names let one WireGuard config
//     and one Babel interface line overwrite another.
//
// For each normalized form it keeps a "normalized key -> first node name that used that key" map,
// errors when a second node falls into the same key, and names both conflicting nodes in the message.
func validateNodeNameCollisions(topo *model.Topology, result *ValidationResult) {
	// Each map's key is a normalized form; the value is the first node name that used that key.
	rawNames := make(map[string]string)       // raw name -> first node name
	interfaceNames := make(map[string]string) // WireGuard interface name -> first node name

	for i, node := range topo.Nodes {
		if node.Name == "" {
			// Schema validation already covers empty names; skip here to avoid an empty-string collision.
			continue
		}
		prefix := fmt.Sprintf("nodes[%d].name", i)

		// N1: raw-name collision.
		if firstNode, exists := rawNames[node.Name]; exists {
			result.AddError(prefix, CodeNodeNameDuplicate, P{"other", firstNode}, P{"node", node.Name}, P{"name", fmt.Sprintf("%q", node.Name)})
		} else {
			rawNames[node.Name] = node.Name
		}

		// N3: WireGuard interface-name collision (e.g. "db.east" and "db-east" both normalize to wg-db-east).
		interfaceName := naming.WgInterfaceName(node.Name)
		if firstNode, exists := interfaceNames[interfaceName]; exists {
			if firstNode != node.Name {
				result.AddError(prefix, CodeNodeNameInterfaceCollision, P{"other", firstNode}, P{"node", node.Name}, P{"name", fmt.Sprintf("%q", interfaceName)})
			}
		} else {
			interfaceNames[interfaceName] = node.Name
		}
	}
}
