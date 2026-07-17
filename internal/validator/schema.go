package validator

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/probepolicy"
)

// wgPublicKeyPattern matches a WireGuard public key: 32 bytes of standard base64 = exactly 43 base64
// chars + one '=' pad. A regex — NOT base64.DecodeString — is deliberate and load-bearing: Go's base64
// decoder SILENTLY STRIPS '\r'/'\n', so DecodeString would ACCEPT a key with an embedded newline, which
// is the exact config-injection vector this guards against (the key is rendered verbatim into peers'
// root-parsed wg configs). The pattern pins the alphabet + length, rejects any whitespace/newline, and
// mirrors the TS validator's wgPublicKeyPattern byte-for-byte (conformance parity).
var wgPublicKeyPattern = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

// ValidWGPublicKey reports whether s is a well-formed WireGuard public key (32-byte Curve25519, clean
// standard base64 with no surrounding or embedded whitespace). It is the single source of truth for
// "is this key safe to emit", shared by the schema validator and the controller enrollment/manual-node
// ingress.
func ValidWGPublicKey(s string) bool {
	return wgPublicKeyPattern.MatchString(s)
}

// nodeNameCharset constrains the legal character set for a node name (defence-in-depth for D15).
// A node name is derived into a WireGuard interface name and is interpolated into the install
// script that runs as root, so it must exclude quotes, backticks, dollar signs, semicolons and
// other shell metacharacters to foreclose command injection.
// Allowed only: letters, digits, spaces, dots, underscores, and hyphens.
var nodeNameCharset = regexp.MustCompile(`^[A-Za-z0-9 ._-]+$`)

// nodeIDCharset is the character-set half of the portable node-directory contract. The naming
// package additionally enforces length, Windows device names/trailing-dot rules, and root-helper
// reservations. Frontend local compilation executes this same Go validator through WASM.
var nodeIDCharset = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// mimicEgressIfacePattern validates an optional per-node mimic egress-interface override. A Linux
// interface name is <= IFNAMSIZ-1 (15) chars; the charset mirrors the TS validator byte-for-byte.
var mimicEgressIfacePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,15}$`)

// sshFieldCharset constrains the legal character set for the SSH connection fields
// (ssh_host / ssh_alias / ssh_user) (D44).
// These fields are interpolated into the bash and PowerShell deploy scripts that run on the
// operator's own machine, so they must exclude whitespace and every shell metacharacter.
// Allowed only: letters, digits, dots, underscores, colons, @, and hyphens.
var sshFieldCharset = regexp.MustCompile(`^[A-Za-z0-9._:@-]+$`)

// sshKeyPathCharset constrains ssh_key_path. Like ssh_host/alias/user it is
// spliced into the operator's bash + PowerShell deploy scripts (ssh/scp -i
// <path>), so it must exclude every shell metacharacter that could break out of
// quoting. But unlike those connection fields it is a filesystem PATH, so it
// additionally permits the path characters a real key path needs: forward and
// back slashes, a leading ~, a Windows drive colon, and spaces (e.g.
// `C:\Users\John Doe\.ssh\id_ed25519`). Everything dangerous — $ ` " ' ; | & <
// > ( ) etc. — is excluded. This is the validation half of the ssh_key_path
// injection fix; the renderer's bashSingleQuote/powerShellArgQuote escaping is
// the defence-in-depth runtime half.
var sshKeyPathCharset = regexp.MustCompile(`^[A-Za-z0-9._:@/\\~ -]+$`)

// endpointHostCharset constrains edge endpoint_host and node public_endpoints[].host (plan-6).
// These hosts are rendered into the per-peer WireGuard config FILE that root's wg-quick parses
// (the `Endpoint = <host>:<port>` line), so the charset admits exactly what a WireGuard endpoint
// host can be — hostnames, IPv4, and bracketed IPv6 (letters, digits, dot, underscore, colon,
// square brackets, hyphen) — and forbids whitespace and control/metacharacters that would
// corrupt the config or confuse the parser. The host is ALSO shq-escaped (single-quoted) and spliced
// into the root install shell for a mimic edge (install.sh `_mimic_resolve <host>` resolves it to an
// IP for the `remote=` filter), so this charset is shell-injection defense-in-depth on top of the
// quoting — do NOT relax it to admit shell metacharacters.
var endpointHostCharset = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]+$`)

// routerIDMAC48 constrains the MAC-48 form of a Babel router-id (D66): six colon-separated
// hexadecimal pairs, e.g. 02:11:22:33:44:55. babeld also accepts an IPv4-form router-id, so the
// IPv4 form is checked separately by net.ParseIP (see validateNodesSchema); satisfying either
// form is legal.
var routerIDMAC48 = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)

const (
	// mtuMinimum is the practical lower bound for a WireGuard interface MTU: 576 is the minimum
	// reassembly buffer an IPv4 datagram must support; below this value wg-quick rejects the
	// interface (producing an undeployable config). D64.
	mtuMinimum = 576
	// mtuMaximum is the theoretical upper bound for the MTU (a 16-bit unsigned field). D64.
	mtuMaximum = 65535
)

// ValidateSchema runs Pass 1 of validation: the structural schema checks over a topology's
// project, domains, nodes, and edges (required fields, enum values, CIDR/charset/range
// well-formedness). It normalizes a few fields in place (e.g. routing_mode, transport) and
// returns a ValidationResult accumulating every schema error and warning.
//
// Topology size bounds (plan-6 item 6): a DoS guard DISTINCT from the HTTP body-size cap.
// They reject obviously-abusive topologies before the per-entity loops and the O(n²)
// semantic pass (IP-collision/NAT-reachability) ever run on attacker-controlled bulk. The
// ceilings are far above any realistic overlay (hundreds of nodes) — they stop "a million
// nodes", not a power user.
const (
	maxTopologyNodes = 2000
	maxTopologyEdges = 10000
	// maxTopologyDomains bounds the number of domains a topology may carry. Each domain drives a
	// separate allocation pool (its own CIDR scan + reserved-range walk), so an unbounded domain
	// count is the same DoS class as the nodes/edges bound — it is far above any realistic overlay
	// (a handful of domains), so it stops "a million domains", not a power user.
	maxTopologyDomains = 1000
	// maxReservedRangesPerDomain bounds the reserved_ranges entries on a SINGLE domain. Every
	// reserved range is parsed and then Contains-walked for every candidate address during
	// allocation (an O(candidates × reserved) inner loop), so a domain stamped with thousands of
	// reserved ranges is an amplification vector independent of node/edge/domain counts. The
	// ceiling is far above any realistic carve-out list.
	maxReservedRangesPerDomain = 1000
)

// topologyExceedsBounds reports whether a topology must be rejected at the root before any
// further validation: it is too large to process safely (count bound) OR is stamped with an
// allocation-schema version newer than this build understands (forward-compat fail-closed,
// plan-6 item 7 — a newer YAOG may use a pin format we would misread as v1). Both
// ValidateSchema and ValidateSemantic short-circuit on it so neither the per-entity loops
// nor the O(n²) semantic checks touch abusive bulk or a future-format topology.
func topologyExceedsBounds(topo *model.Topology) bool {
	if topo.AllocSchemaVersion > model.CurrentAllocSchemaVersion ||
		len(topo.Nodes) > maxTopologyNodes ||
		len(topo.Edges) > maxTopologyEdges ||
		len(topo.Domains) > maxTopologyDomains {
		return true
	}
	// Reserved ranges are bounded PER domain (the amplification is the per-domain Contains walk),
	// so a single over-cap domain trips the root guard.
	for i := range topo.Domains {
		if len(topo.Domains[i].ReservedRanges) > maxReservedRangesPerDomain {
			return true
		}
	}
	return false
}

func ValidateSchema(topo *model.Topology) *ValidationResult {
	result := &ValidationResult{}

	// Topology-root guards reported HERE (schema is the canonical reporter) and
	// short-circuiting: an oversized or future-format topology is rejected outright rather
	// than merged into a pile of misleading downstream errors, and the expensive passes
	// never run on it. ValidateSemantic guards on the same predicate without re-reporting.
	if topologyExceedsBounds(topo) {
		if topo.AllocSchemaVersion > model.CurrentAllocSchemaVersion {
			result.AddError("alloc_schema_version", CodeTopologySchemaVersionUnsupported,
				P{"version", strconv.Itoa(topo.AllocSchemaVersion)}, P{"max", strconv.Itoa(model.CurrentAllocSchemaVersion)})
		}
		if len(topo.Nodes) > maxTopologyNodes {
			result.AddError("nodes", CodeTopologyTooManyNodes,
				P{"count", strconv.Itoa(len(topo.Nodes))}, P{"max", strconv.Itoa(maxTopologyNodes)})
		}
		if len(topo.Edges) > maxTopologyEdges {
			result.AddError("edges", CodeTopologyTooManyEdges,
				P{"count", strconv.Itoa(len(topo.Edges))}, P{"max", strconv.Itoa(maxTopologyEdges)})
		}
		if len(topo.Domains) > maxTopologyDomains {
			result.AddError("domains", CodeTopologyTooManyDomains,
				P{"count", strconv.Itoa(len(topo.Domains))}, P{"max", strconv.Itoa(maxTopologyDomains)})
		}
		for i := range topo.Domains {
			if n := len(topo.Domains[i].ReservedRanges); n > maxReservedRangesPerDomain {
				result.AddError(fmt.Sprintf("domains[%d].reserved_ranges", i), CodeTopologyTooManyReservedRanges,
					P{"count", strconv.Itoa(n)}, P{"max", strconv.Itoa(maxReservedRangesPerDomain)})
			}
		}
		return result
	}

	// Project
	validateProjectSchema(topo, result)

	// Domains
	validateDomainsSchema(topo, result)

	// Nodes
	validateNodesSchema(topo, result)

	// Edges
	validateEdgesSchema(topo, result)

	return result
}

func validateProjectSchema(topo *model.Topology, result *ValidationResult) {
	if topo.Project.ID == "" {
		result.AddError("project.id", CodeProjectIDRequired)
	}
	if topo.Project.Name == "" {
		result.AddError("project.name", CodeProjectNameRequired)
	}
}

func validateDomainsSchema(topo *model.Topology, result *ValidationResult) {
	if len(topo.Domains) == 0 {
		result.AddError("domains", CodeDomainNoneDefined)
		return
	}

	for i := range topo.Domains {
		// Access via an index-derived pointer so that normalizing write-backs to fields such as
		// RoutingMode persist into the topology object (writes to the copy yielded by range would
		// not take effect; see the round-trip requirement of Spec C).
		domain := &topo.Domains[i]
		prefix := fmt.Sprintf("domains[%d]", i)

		if domain.ID == "" {
			result.AddError(prefix+".id", CodeDomainIDRequired)
		}
		if domain.Name == "" {
			result.AddError(prefix+".name", CodeDomainNameRequired)
		}

		// CIDR format validation
		if domain.CIDR == "" {
			result.AddError(prefix+".cidr", CodeDomainCIDREmpty)
		} else {
			_, ipNet, err := net.ParseCIDR(domain.CIDR)
			if err != nil {
				result.AddError(prefix+".cidr", CodeDomainCIDRInvalid, P{"cidr", domain.CIDR})
			} else if ipNet.IP.To4() == nil {
				// IPv4-only: the allocator only supports IPv4; IPv6 / other address families would crash it
				result.AddError(prefix+".cidr", CodeDomainCIDRNotIPv4, P{"cidr", domain.CIDR})
			} else {
				// CIDR size lower bound: a prefix shorter than /8 is too large to enumerate for allocation
				ones, _ := ipNet.Mask.Size()
				if ones < 8 {
					result.AddError(prefix+".cidr", CodeDomainCIDRTooLarge, P{"cidr", domain.CIDR})
				}
			}
		}

		// AllocationMode
		validAllocModes := map[string]bool{"auto": true, "manual": true}
		if domain.AllocationMode != "" && !validAllocModes[domain.AllocationMode] {
			result.AddError(prefix+".allocation_mode", CodeDomainAllocationModeInvalid, P{"mode", domain.AllocationMode})
		}

		// RoutingMode normalization and validation (D2/D72, Spec C: docs/spec/compiler/routing-modes.md).
		// First normalize the empty value to babel and write it back to the topology object so it can
		// round-trip (both the compile result and the persisted topology explicitly carry babel),
		// eliminating the "empty routing_mode silently disables the routing daemon yet compiles
		// successfully" failure mode. The enum check must run after normalization so the empty value
		// cannot bypass it.
		if domain.RoutingMode == "" {
			domain.RoutingMode = "babel"
		}
		// babel is the only currently implemented routing mode; static and none are reserved values
		// whose routing installers are not yet implemented, so reject them outright rather than render
		// a route-less dead overlay.
		switch domain.RoutingMode {
		case "babel":
			// The only implemented mode; allow it.
		case "static", "none":
			result.AddError(prefix+".routing_mode", CodeDomainRoutingModeUnimplemented, P{"mode", domain.RoutingMode})
		default:
			result.AddError(prefix+".routing_mode", CodeDomainRoutingModeInvalid, P{"mode", domain.RoutingMode})
		}

		// ReservedRanges validation: each entry must be a parseable CIDR or IP, and must be IPv4
		for j, rr := range domain.ReservedRanges {
			rrPrefix := fmt.Sprintf("%s.reserved_ranges[%d]", prefix, j)
			_, rNet, err := net.ParseCIDR(rr)
			if err == nil {
				// Parsed as a CIDR: require the IPv4 address family
				if rNet.IP.To4() == nil {
					result.AddError(rrPrefix, CodeDomainReservedRangeNotIPv4, P{"cidr", rr})
				}
				continue
			}
			// Fall back to a single IP: require it to be parseable and IPv4
			ip := net.ParseIP(rr)
			if ip == nil {
				result.AddError(rrPrefix, CodeDomainReservedRangeInvalid, P{"value", rr})
			} else if ip.To4() == nil {
				result.AddError(rrPrefix, CodeDomainReservedAddressNotIPv4, P{"ip", rr})
			}
		}

		// transit_cidr validation (plan-6): parseable, IPv4-only, and large enough to hold the
		// per-link transit address pairs (each link consumes a pair of transit IPs). Mirrors the
		// domain CIDR's IPv4 + size guards; an empty value falls back in the compiler to the default
		// 10.10.0.0/24 and needs no validation.
		if domain.TransitCIDR != "" {
			_, tNet, err := net.ParseCIDR(domain.TransitCIDR)
			if err != nil {
				result.AddError(prefix+".transit_cidr", CodeDomainTransitCIDRInvalid, P{"cidr", domain.TransitCIDR})
			} else if tNet.IP.To4() == nil {
				result.AddError(prefix+".transit_cidr", CodeDomainTransitCIDRNotIPv4, P{"cidr", domain.TransitCIDR})
			} else {
				ones, _ := tNet.Mask.Size()
				if ones < 8 {
					result.AddError(prefix+".transit_cidr", CodeDomainTransitCIDRTooLarge, P{"cidr", domain.TransitCIDR})
				} else if ones > 30 {
					result.AddError(prefix+".transit_cidr", CodeDomainTransitCIDRTooSmall, P{"cidr", domain.TransitCIDR})
				}
			}
		}
	}
}

func validateNodesSchema(topo *model.Topology, result *ValidationResult) {
	for i, node := range topo.Nodes {
		prefix := fmt.Sprintf("nodes[%d]", i)

		if node.ID == "" {
			result.AddError(prefix+".id", CodeNodeIDRequired)
		} else if node.ID == "." || node.ID == ".." || !nodeIDCharset.MatchString(node.ID) {
			// A node ID reaches path/file/interface-name sinks, so reject spaces, '/', and shell
			// metacharacters at the source.
			result.AddError(prefix+".id", CodeNodeIDIllegalChars, P{"id", fmt.Sprintf("%q", node.ID)})
		} else if !naming.ValidPortableNodeID(node.ID) {
			result.AddError(prefix+".id", CodeNodeIDNotPortable, P{"id", fmt.Sprintf("%q", node.ID)}, P{"max", fmt.Sprint(naming.MaxPortableNodeIDLength)})
		}
		if node.Name == "" {
			result.AddError(prefix+".name", CodeNodeNameRequired)
		} else if node.Name == "." || node.Name == ".." || !nodeNameCharset.MatchString(node.Name) {
			// Node name charset validation (D15 defence-in-depth): the name derives a WireGuard
			// interface name and is interpolated into the install script that runs as root, so quotes,
			// backticks, $, ; and other shell metacharacters are forbidden.
			result.AddError(prefix+".name", CodeNodeNameIllegalChars, P{"name", fmt.Sprintf("%q", node.Name)})
		}
		if node.DomainID == "" {
			result.AddError(prefix+".domain_id", CodeNodeDomainIDRequired)
		}

		// Role
		validRoles := map[string]bool{"peer": true, "router": true, "relay": true, "gateway": true, "client": true}
		if node.Role == "" {
			result.AddError(prefix+".role", CodeNodeRoleEmpty)
		} else if !validRoles[node.Role] {
			result.AddError(prefix+".role", CodeNodeRoleInvalid, P{"role", node.Role})
		}

		// Deployment mode (optional; empty == managed). Orthogonal to role. An invalid value changes
		// custody/admission behavior (manual nodes carry their own pre-known key), so reject a typo
		// rather than silently treating it as managed.
		if node.DeploymentMode != "" && node.DeploymentMode != model.DeploymentManaged && node.DeploymentMode != model.DeploymentManual {
			result.AddError(prefix+".deployment_mode", CodeNodeDeploymentModeInvalid, P{"mode", node.DeploymentMode})
		}

		// Active probes are deployment policy, not an air-gap/manual-node side effect. Validate
		// their complete typed contract here so neither render nor the agent has to interpret an
		// ambiguous destination. Runtime parses the signed copy again as defense in depth.
		if len(node.TelemetryProbes) > 0 {
			if node.IsManual() {
				result.AddError(prefix+".telemetry_probes", CodeNodeTelemetryProbesInvalid, P{"detail", "active probes require an agent-managed source node"})
			} else if err := probepolicy.Validate(node.TelemetryProbes); err != nil {
				result.AddError(prefix+".telemetry_probes", CodeNodeTelemetryProbesInvalid, P{"detail", err.Error()})
			}
		}
		if node.TelemetryDevices != nil {
			if node.IsManual() {
				result.AddError(prefix+".telemetry_devices", CodeNodeTelemetryDevicesInvalid, P{"detail", "automatic device telemetry requires an agent-managed source node"})
			} else if err := probepolicy.ValidateDevicePolicy(&probepolicy.DevicePolicy{
				Mode: probepolicy.DeviceMode(node.TelemetryDevices.Mode),
			}); err != nil {
				result.AddError(prefix+".telemetry_devices", CodeNodeTelemetryDevicesInvalid, P{"detail", err.Error()})
			}
		}

		// Platform (optional; unsupported values are a warning, not an error)
		if node.Platform != "" {
			validPlatforms := map[string]bool{"debian": true, "ubuntu": true}
			if !validPlatforms[strings.ToLower(node.Platform)] {
				result.AddWarning(prefix+".platform", CodeNodePlatformUnsupported, P{"platform", node.Platform})
			}
		}

		// XDPMode: the XDP attach mode for mimic (transport=="tcp"). Only skb/native are legal;
		// empty is equivalent to skb (the default generic XDP). The renderer silently falls back to
		// skb on an illegal value, so reject it explicitly here to avoid spellings like
		// "Native"/"generic" being quietly treated as skb (docs/spec/artifacts/mimic.md).
		if node.XDPMode != "" {
			validXDPModes := map[string]bool{"skb": true, "native": true}
			if !validXDPModes[node.XDPMode] {
				result.AddError(prefix+".xdp_mode", CodeNodeXDPModeInvalid, P{"mode", node.XDPMode})
			}
		}
		// MimicEgressInterface: optional per-node override of the mimic egress NIC. If set, require a
		// plausible interface name (the renderer shq-escapes it, so this is a typo/UX guard, not safety).
		if node.MimicEgressInterface != "" && !mimicEgressIfacePattern.MatchString(node.MimicEgressInterface) {
			result.AddError(prefix+".mimic_egress_interface", CodeNodeMimicEgressInterfaceInvalid, P{"iface", node.MimicEgressInterface})
		}

		// OverlayIP (optional; must be a parseable IP when set)
		if node.OverlayIP != "" {
			if net.ParseIP(node.OverlayIP) == nil {
				result.AddError(prefix+".overlay_ip", CodeNodeOverlayIPInvalid, P{"ip", node.OverlayIP})
			}
		}

		// WireGuardPublicKey (optional here: a MANAGED node's key comes from the enrollment
		// registry, so the topology field is empty — check only when present, e.g. a MANUAL node or
		// an air-gap/local topology that carries derived keys). It is rendered VERBATIM into peers'
		// root-parsed wg configs via a non-escaping template, so a malformed value (bad base64 /
		// wrong length / embedded newline) is rejected at the source.
		if node.WireGuardPublicKey != "" && !ValidWGPublicKey(node.WireGuardPublicKey) {
			result.AddError(prefix+".wireguard_public_key", CodeNodeWGPublicKeyInvalid, P{"key", fmt.Sprintf("%q", node.WireGuardPublicKey)})
		}

		// MTU validation (D64): 0 means use the system default (typically 1420) and is skipped.
		// When non-zero it must fall within [576, 65535] — an MTU below 576 (the minimum IPv4
		// datagram reassembly buffer) or above 65535 is rejected by wg-quick, producing an
		// undeployable WireGuard config.
		if node.MTU != 0 && (node.MTU < mtuMinimum || node.MTU > mtuMaximum) {
			result.AddError(prefix+".mtu", CodeNodeMTUOutOfRange, P{"mtu", strconv.Itoa(node.MTU)}, P{"low", strconv.Itoa(mtuMinimum)}, P{"high", strconv.Itoa(mtuMaximum)})
		}

		// SSHPort validation (D65): 0 means use the default port 22 and is skipped.
		// When non-zero it must fall within 1-65535, otherwise it would be interpolated into an
		// unconnectable SSH deploy command.
		if node.SSHPort != 0 && (node.SSHPort < 1 || node.SSHPort > 65535) {
			result.AddError(prefix+".ssh_port", CodeNodeSSHPortOutOfRange, P{"port", strconv.Itoa(node.SSHPort)})
		}

		// RouterID validation (D66): left empty, the compiler auto-generates it and the check is skipped.
		// When non-empty it must be either the MAC-48 form (six colon-separated hexadecimal pairs,
		// e.g. 02:11:22:33:44:55) or parseable as an IPv4 address — babeld accepts both forms; any
		// other value is rejected by babeld.
		if node.RouterID != "" {
			if !routerIDMAC48.MatchString(node.RouterID) && net.ParseIP(node.RouterID).To4() == nil {
				result.AddError(prefix+".router_id", CodeNodeRouterIDInvalid, P{"id", fmt.Sprintf("%q", node.RouterID)})
			}
		}

		// ExtraPrefixes validation (D67): each entry must be parseable as an IPv4 CIDR (mirroring the
		// IPv4-guard style of reserved_ranges). These prefixes are announced into the Babel routing
		// table; a non-IPv4 or non-CIDR prefix would produce an undeployable babeld config.
		for j, prefixCIDR := range node.ExtraPrefixes {
			epPrefix := fmt.Sprintf("%s.extra_prefixes[%d]", prefix, j)
			_, epNet, err := net.ParseCIDR(prefixCIDR)
			if err != nil {
				result.AddError(epPrefix, CodeNodeExtraPrefixInvalid, P{"prefix", prefixCIDR})
			} else if epNet.IP.To4() == nil {
				result.AddError(epPrefix, CodeNodeExtraPrefixNotIPv4, P{"prefix", prefixCIDR})
			}
		}

		// SSH field charset validation (D44): when non-empty, each field is interpolated into the
		// bash and PowerShell deploy scripts that run on the operator's own machine, so it must
		// exclude whitespace and every shell metacharacter.
		if node.SSHHost != "" && !sshFieldCharset.MatchString(node.SSHHost) {
			result.AddError(prefix+".ssh_host", CodeNodeSSHHostIllegalChars, P{"host", fmt.Sprintf("%q", node.SSHHost)})
		}
		if node.SSHAlias != "" && !sshFieldCharset.MatchString(node.SSHAlias) {
			result.AddError(prefix+".ssh_alias", CodeNodeSSHAliasIllegalChars, P{"alias", fmt.Sprintf("%q", node.SSHAlias)})
		}
		if node.SSHUser != "" && !sshFieldCharset.MatchString(node.SSHUser) {
			result.AddError(prefix+".ssh_user", CodeNodeSSHUserIllegalChars, P{"user", fmt.Sprintf("%q", node.SSHUser)})
		}
		// ssh_key_path is also spliced into the operator's deploy shell command
		// (ssh/scp -i <path>); it permits path characters (/ \ ~ : space) the
		// connection fields don't, but still forbids every shell metacharacter so a
		// hostile path like `/k$(reboot).pem` or `k".pem` cannot inject. See
		// sshKeyPathCharset.
		if node.SSHKeyPath != "" && !sshKeyPathCharset.MatchString(node.SSHKeyPath) {
			result.AddError(prefix+".ssh_key_path", CodeNodeSSHKeyPathIllegalChars, P{"path", fmt.Sprintf("%q", node.SSHKeyPath)})
		}

		// public_endpoints[].host charset validation (plan-6): host is rendered into the per-peer
		// WireGuard config file parsed by root's wg-quick (the `Endpoint =` line), so it must exclude
		// whitespace and control/metacharacters that would corrupt the config or confuse the parser.
		// PublicEndpoint.Port is not validated here: it is only a node-reachability hint that the
		// compiler never renders (the reverse-endpoint fallback uses the allocated listen port, see
		// peers.go), so only host needs guarding.
		for k := range node.PublicEndpoints {
			ep := &node.PublicEndpoints[k]
			if ep.Host != "" && !endpointHostCharset.MatchString(ep.Host) {
				result.AddError(fmt.Sprintf("%s.public_endpoints[%d].host", prefix, k), CodeNodePublicEndpointHostIllegalChars, P{"host", fmt.Sprintf("%q", ep.Host)})
			}
		}
	}
}

func validateEdgesSchema(topo *model.Topology, result *ValidationResult) {
	for i := range topo.Edges {
		// Access via an index-derived pointer so that normalizing write-backs to fields such as
		// Transport persist into the topology object.
		edge := &topo.Edges[i]
		prefix := fmt.Sprintf("edges[%d]", i)

		if edge.ID == "" {
			result.AddError(prefix+".id", CodeEdgeIDRequired)
		}
		if edge.FromNodeID == "" {
			result.AddError(prefix+".from_node_id", CodeEdgeFromNodeIDRequired)
		}
		if edge.ToNodeID == "" {
			result.AddError(prefix+".to_node_id", CodeEdgeToNodeIDRequired)
		}

		// Type
		validTypes := map[string]bool{"direct": true, "public-endpoint": true, "relay-path": true, "candidate": true}
		if edge.Type == "" {
			result.AddError(prefix+".type", CodeEdgeTypeEmpty)
		} else if !validTypes[edge.Type] {
			result.AddError(prefix+".type", CodeEdgeTypeInvalid, P{"type", edge.Type})
		}

		// Transport normalization and validation (D72, Spec C).
		// First normalize the empty value to udp and write it back to the topology object, then run
		// the enum check — the same normalization pattern as routing_mode, so the enum check runs
		// after normalization.
		if edge.Transport == "" {
			edge.Transport = "udp"
		}
		validTransports := map[string]bool{"udp": true, "tcp": true}
		if !validTransports[edge.Transport] {
			result.AddError(prefix+".transport", CodeEdgeTransportInvalid, P{"transport", edge.Transport})
		}
		// tcp is now implemented (mimic eBPF UDP->fake-TCP encapsulation), is a legal value, and no
		// longer warns. The semantic constraint that "both ends must be deployable Linux" is handled
		// by validateMimicTransport in semantic.go.

		// mimic_fallback enum (plan-4): "" (inherit the fleet default) / "udp" / "none". A meaningful
		// tri-state, so "" is NOT normalized; the enum guard rejects typos ("UDP"/"off"/"yes") at the
		// schema stage. Only relevant on a tcp edge (semantic.go advises if set on a udp edge); the
		// resolver in the compiler floors anything unrecognized to "none" defensively, but a stored
		// value must be one of these three.
		validFallback := map[string]bool{"": true, "udp": true, "none": true}
		if !validFallback[edge.MimicFallback] {
			result.AddError(prefix+".mimic_fallback", CodeEdgeMimicFallbackInvalid, P{"policy", edge.MimicFallback})
		}

		// EndpointPort
		if edge.EndpointPort < 0 || edge.EndpointPort > 65535 {
			result.AddError(prefix+".endpoint_port", CodeEdgeEndpointPortInvalid, P{"port", strconv.Itoa(edge.EndpointPort)})
		}

		// A port override REQUIRES an explicit endpoint host (require-explicit-host semantics): a port
		// alone cannot be dialed. Without this the compiler silently DROPS a port-only forward override
		// (its Endpoint derivation requires endpoint_host) — or the reverse direction falls back to the
		// peer's plain public IP — while the panel badge still claims "NAT override active". Reject the
		// inconsistent state loudly here instead of shipping a config that quietly ignores the override.
		if edge.EndpointPort > 0 && edge.EndpointHost == "" {
			result.AddError(prefix+".endpoint_port", CodeEdgeEndpointPortWithoutHost)
		}

		// endpoint_host charset validation (plan-6): when non-empty it is rendered into the per-peer
		// WireGuard config file parsed by root's wg-quick (the `Endpoint =` line), so it must exclude
		// whitespace and control/metacharacters that would corrupt the config or confuse the parser.
		if edge.EndpointHost != "" && !endpointHostCharset.MatchString(edge.EndpointHost) {
			result.AddError(prefix+".endpoint_host", CodeEdgeEndpointHostIllegalChars, P{"host", fmt.Sprintf("%q", edge.EndpointHost)})
		}

		// Role validation (parallel links / failover): only empty, "primary", and "backup" are allowed.
		// Empty and "primary" belong to the same primary class (collapsing to one primary link per node
		// pair), whereas each "backup" edge becomes its own independent backup link. Semantics in
		// docs/spec/compiler/allocation-stability.md (Link identity with parallel edges).
		if edge.Role != "" && edge.Role != model.EdgeRolePrimary && edge.Role != model.EdgeRoleBackup {
			result.AddError(prefix+".role", CodeEdgeRoleInvalid, P{"role", edge.Role})
		}

		// link_direction validation (per-edge dial-direction policy): only empty, "both", and
		// "forward" are allowed; empty is equivalent to "both" (a meaningful default, so "" is NOT
		// normalized — mirrors mimic_fallback). There is deliberately no "reverse" (D11, one
		// spelling): single-linking the other way is expressed by flipping the edge. The compiler
		// defensively floors anything unrecognized to "both", but a stored value must be one of
		// these three; the semantic dial-direction rules (validateLinkDirection) only ever act on
		// recognized values.
		if edge.LinkDirection != "" && edge.LinkDirection != model.EdgeLinkDirectionBoth &&
			edge.LinkDirection != model.EdgeLinkDirectionForward {
			result.AddError(prefix+".link_direction", CodeEdgeLinkDirectionInvalid, P{"direction", edge.LinkDirection})
		}

		// Self-loop: an edge whose endpoints are the same node is invalid.
		if edge.FromNodeID != "" && edge.FromNodeID == edge.ToNodeID {
			result.AddError(prefix, CodeEdgeSelfLoop)
		}
	}
}
