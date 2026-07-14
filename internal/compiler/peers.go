package compiler

import (
	"crypto/sha256"
	"fmt"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/allocconst"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// transitCIDRForNode resolves the transit CIDR ownership for a link (or for an
// external edge awaiting reservation): it takes the TransitCIDR of the domain the
// from-node belongs to, falling back to the default pool on empty (no domain /
// unconfigured). This is the single ownership logic shared by "link construction"
// (derivePeersWithDomains) and "external pin reservation" (ReservedAllocations);
// both must coalesce transit IPs under the same CIDR key, otherwise cross-subgraph
// reservation would mismatch pools and miss a real collision.
func transitCIDRForNode(from *model.Node, domainMap map[string]*model.Domain) string {
	if from != nil {
		if domain := domainMap[from.DomainID]; domain != nil && domain.TransitCIDR != "" {
			return domain.TransitCIDR
		}
	}
	return allocconst.DefaultTransitCIDR
}

// transportTCP is the literal for edge.Transport taking "tcp" (mimic shaping
// transport). mimic has no key and no new field; transport=="tcp" is the only
// signal that a link is wrapped by mimic (docs/spec/data-model/edge.md §TCP transport).
const transportTCP = "tcp"

// defaultMimicBaseMTU is the base WireGuard MTU for a mimic link when the node has
// no explicit MTU set. node.MTU==0 usually means "use the system default (~1420)";
// but a mimic link must explicitly emit the reduced MTU, so for mimic links 0 is
// resolved to 1420 as the base, then the mimic overhead is subtracted. See
// docs/spec/artifacts/mimic.md (MTU −12).
const defaultMimicBaseMTU = 1420

// mimicMTUOverhead is the byte overhead mimic (UDP->fake TCP) introduces on each
// WireGuard interface. docs/spec/artifacts/mimic.md: "MTU −12 on each mimic
// WireGuard interface".
const mimicMTUOverhead = 12

// isMimicEdge reports whether an edge has mimic (tcp shaping transport) enabled.
// Spec: whether a link is mimic is determined entirely by its primaryEdge's
// transport (docs/spec/data-model/edge.md §TCP transport) — a primary-class link's
// mimic-ness depends on its primaryEdge, and each backup link takes its own (whose
// primaryEdge is the backup edge itself).
func isMimicEdge(edge *model.Edge) bool {
	return edge != nil && edge.Transport == transportTCP
}

// Mimic-fallback policy values (the closed enum for Edge.MimicFallback and the resolved
// PeerInfo.MimicFallback). "" on an EDGE means "inherit the fleet default"; the RESOLVED value on a
// PeerInfo is always one of mimicFallbackUDP / mimicFallbackNone (never "").
const (
	mimicFallbackInherit = ""     // edge-level only: defer to the fleet-wide default
	mimicFallbackUDP     = "udp"  // fall back to plain UDP if mimic provisioning fails
	mimicFallbackNone    = "none" // fail closed; do not fall back (the shipped default, D1)
)

// resolveMimicFallback computes the EFFECTIVE per-link mimic-fallback policy:
//   - edgePolicy "udp"/"none"                 -> that explicit edge choice;
//   - edgePolicy "" (inherit) + default "udp" -> "udp";
//   - otherwise                               -> "none" (the fail-closed floor — D1, shipped OFF).
//
// PURE: deterministic in its two string args only, so the Go and TS ports stay byte-identical and it
// never touches allocation. An unrecognized defaultPolicy (defensive) also floors to "none". With
// defaultPolicy=="" (the air-gap/CLI + conformance default) every link resolves to "none" — which
// plan-5 renders identically to today's fail-closed mimic install, so no rendered artifact changes.
func resolveMimicFallback(edgePolicy, defaultPolicy string) string {
	switch edgePolicy {
	case mimicFallbackUDP:
		return mimicFallbackUDP
	case mimicFallbackNone:
		return mimicFallbackNone
	}
	if defaultPolicy == mimicFallbackUDP {
		return mimicFallbackUDP
	}
	return mimicFallbackNone
}

// effectiveLinkDirection resolves an edge's dial-direction policy: "forward" passes through;
// "" / "both" / anything unrecognized floors to EdgeLinkDirectionBoth (defensive — the validator
// rejects unknown values at schema time; the compiler never re-errors). There is no "reverse"
// value by design (D11: one spelling — single-linking the other way is expressed by flipping the
// edge). PURE and allocation-blind: like resolveMimicFallback it is deterministic in its input
// string only, so the Go and TS ports stay byte-identical and allocation never observes it
// (docs/spec/data-model/edge.md §Link direction).
func effectiveLinkDirection(edge *model.Edge) string {
	if edge.LinkDirection == model.EdgeLinkDirectionForward {
		return model.EdgeLinkDirectionForward
	}
	return model.EdgeLinkDirectionBoth
}

// effectiveMTU computes the effective MTU a WireGuard interface on a link should emit.
// Spec (docs/spec/artifacts/mimic.md "MTU −12" / docs/spec/data-model/edge.md §TCP transport):
//   - non-mimic: keep node.MTU as is (0 ⇒ still 0 ⇒ renderer omits the MTU line,
//     byte-identical to before the change);
//   - mimic: ((node.MTU>0 ? node.MTU : 1420) − 12), subtracting mimic's 12-byte
//     overhead explicitly.
func effectiveMTU(nodeMTU int, mimic bool) int {
	if !mimic {
		return nodeMTU
	}
	base := nodeMTU
	if base <= 0 {
		base = defaultMimicBaseMTU
	}
	return base - mimicMTUOverhead
}

// KeyPair is a WireGuard key pair.
type KeyPair struct {
	PrivateKey string
	PublicKey  string
}

// PeerInfo describes the complete configuration of a point-to-point WireGuard interface.
// New architecture: one WireGuard interface per peer.
type PeerInfo struct {
	// Remote node ID
	NodeID string

	// Remote node name
	NodeName string

	// Remote node public key
	PublicKey string

	// Remote node overlay IP
	OverlayIP string

	// AllowedIPs (the per-peer model uses a permissive policy: 0.0.0.0/0, ::/0)
	AllowedIPs []string

	// Endpoint (remote public address)
	Endpoint string

	// PersistentKeepalive
	PersistentKeepalive int

	// WireGuard interface name (e.g. wg-dmit, capped at 15 chars on Linux)
	InterfaceName string

	// === The fields below are added by the per-peer-interface architecture ===

	// Dedicated listen port for this interface
	ListenPort int

	// Local transit IP (point-to-point link address)
	LocalTransitIP string

	// Remote transit IP
	RemoteTransitIP string

	// Local IPv6 link-local address (required by Babel)
	LocalLinkLocal string

	// Remote IPv6 link-local address
	RemoteLinkLocal string

	// Whether this is the router-side interface connecting to a client
	IsClientPeer bool

	// Client overlay IP (set only when IsClientPeer=true, used for PostUp route injection)
	ClientOverlayIP string

	// This link's Babel rxcost override, derived from the corresponding edge (D63).
	// 0 means adopt the role preset's default cost (decided by the Babel renderer).
	LinkCost int

	// Whether this link has mimic (tcp shaping transport) enabled: equivalent to
	// link.primaryEdge.Transport=="tcp". mimic has no key and no new field;
	// transport=="tcp" is the only signal (docs/spec/data-model/edge.md §TCP transport).
	// The renderer uses this (together with ListenPort) to derive this node's set of
	// mimic listen ports.
	Mimic bool

	// MimicFallback is the RESOLVED per-link mimic-fallback policy ("udp" or "none", never "").
	// Pure policy: plan-5's install-script branch reads it; this plan only carries it. It is NOT
	// part of any rendered artifact yet, so adding it leaves all current output byte-identical.
	MimicFallback string

	// The effective WireGuard MTU this interface emits.
	// non-mimic: keep node.MTU as is (0 ⇒ renderer omits the MTU line, byte-unchanged).
	// mimic: ((node.MTU>0 ? node.MTU : 1420) − 12), subtracting mimic's 12-byte overhead
	// (docs/spec/artifacts/mimic.md "MTU −12").
	MTU int
}

// formatEndpoint formats an endpoint address.
func formatEndpoint(host string, port int) string {
	if isIPv6(host) {
		return "[" + host + "]:" + itoa(port)
	}
	return host + ":" + itoa(port)
}

func isIPv6(host string) bool {
	for _, c := range host {
		if c == ':' {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}

// GenerateRouterID generates a stable Babel router-id (MAC-48 format).
// It is derived from the SHA-256 hash of the node ID, ensuring stability and uniqueness.
func GenerateRouterID(nodeID string) string {
	h := sha256.Sum256([]byte(nodeID))

	// Take the first 6 bytes as the MAC-48
	b0 := h[0]
	b0 = (b0 | 0x02) & 0xFE // set the locally administered bit, clear the multicast bit
	b1 := h[1]
	b2 := h[2]
	b3 := h[3]
	b4 := h[4]
	b5 := h[5]

	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b0, b1, b2, b3, b4, b5)
}
