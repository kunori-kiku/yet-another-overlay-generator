package validator

import (
	"fmt"
	"strings"
)

// code.go — validation-finding codes + English templates (plan-3.5a). Design locked via the
// 2-Opus debate (wf_f060ce0d): a VALIDATOR-LOCAL code system, deliberately separate from
// internal/apierr. A validation finding rides a 200 ValidateResponse (errors[]/warnings[]),
// it is NOT an HTTP transport failure — so unlike apierr.Code a validator Code carries no HTTP
// status, and being a distinct Go type it CANNOT leak through the apierr HTTP envelope (compile-
// time channel separation). The frontend still localizes both via ONE 'error.<code>' catalog.
//
// "Errors coded at the source, localized at the edge" (PRINCIPLES.md): every user-facing
// validator message is a Code + params; the English template here is the single source of both
// the CLI/curl message and the panel's English i18n fallback. Chinese now lives only in the
// frontend zh catalog — an English-locale operator never sees it on a validation failure.

// Code is a stable validation-finding identifier: validation_<area>_<reason> (snake_case). It
// is the join key to the frontend 'error.<code>' catalog.
type Code string

const (
	CodeProjectIDRequired                 Code = "validation_project_id_required"
	CodeProjectNameRequired               Code = "validation_project_name_required"
	CodeDomainNoneDefined                 Code = "validation_domain_none_defined"
	CodeDomainIDRequired                  Code = "validation_domain_id_required"
	CodeDomainNameRequired                Code = "validation_domain_name_required"
	CodeDomainCIDREmpty                   Code = "validation_domain_cidr_empty"
	CodeDomainCIDRInvalid                 Code = "validation_domain_cidr_invalid"
	CodeDomainCIDRNotIPv4                 Code = "validation_domain_cidr_not_ipv4"
	CodeDomainCIDRTooLarge                Code = "validation_domain_cidr_too_large"
	CodeDomainAllocationModeInvalid       Code = "validation_domain_allocation_mode_invalid"
	CodeDomainRoutingModeUnimplemented    Code = "validation_domain_routing_mode_unimplemented"
	CodeDomainRoutingModeInvalid          Code = "validation_domain_routing_mode_invalid"
	CodeDomainReservedRangeNotIPv4        Code = "validation_domain_reserved_range_not_ipv4"
	CodeDomainReservedRangeInvalid        Code = "validation_domain_reserved_range_invalid"
	CodeDomainReservedAddressNotIPv4      Code = "validation_domain_reserved_address_not_ipv4"
	CodeNodeIDRequired                    Code = "validation_node_id_required"
	CodeNodeNameRequired                  Code = "validation_node_name_required"
	CodeNodeNameIllegalChars              Code = "validation_node_name_illegal_chars"
	CodeNodeDomainIDRequired              Code = "validation_node_domain_id_required"
	CodeNodeRoleEmpty                     Code = "validation_node_role_empty"
	CodeNodeRoleInvalid                   Code = "validation_node_role_invalid"
	CodeNodePlatformUnsupported           Code = "validation_node_platform_unsupported"
	CodeNodeXDPModeInvalid                Code = "validation_node_xdp_mode_invalid"
	CodeNodeOverlayIPInvalid              Code = "validation_node_overlay_ip_invalid"
	CodeNodeListenPortInvalid             Code = "validation_node_listen_port_invalid"
	CodeNodeMTUOutOfRange                 Code = "validation_node_mtu_out_of_range"
	CodeNodeSSHPortOutOfRange             Code = "validation_node_ssh_port_out_of_range"
	CodeNodeRouterIDInvalid               Code = "validation_node_router_id_invalid"
	CodeNodeExtraPrefixInvalid            Code = "validation_node_extra_prefix_invalid"
	CodeNodeExtraPrefixNotIPv4            Code = "validation_node_extra_prefix_not_ipv4"
	CodeNodeSSHHostIllegalChars           Code = "validation_node_ssh_host_illegal_chars"
	CodeNodeSSHAliasIllegalChars          Code = "validation_node_ssh_alias_illegal_chars"
	CodeNodeSSHUserIllegalChars           Code = "validation_node_ssh_user_illegal_chars"
	CodeNodeSSHKeyPathIllegalChars        Code = "validation_node_ssh_key_path_illegal_chars"
	CodeEdgeIDRequired                    Code = "validation_edge_id_required"
	CodeEdgeFromNodeIDRequired            Code = "validation_edge_from_node_id_required"
	CodeEdgeToNodeIDRequired              Code = "validation_edge_to_node_id_required"
	CodeEdgeTypeEmpty                     Code = "validation_edge_type_empty"
	CodeEdgeTypeInvalid                   Code = "validation_edge_type_invalid"
	CodeEdgeTransportInvalid              Code = "validation_edge_transport_invalid"
	CodeEdgeEndpointPortInvalid           Code = "validation_edge_endpoint_port_invalid"
	CodeEdgeRoleInvalid                   Code = "validation_edge_role_invalid"
	CodeEdgeSelfLoop                      Code = "validation_edge_self_loop"
	CodeRoutePolicyReserved               Code = "validation_routepolicy_reserved"
	CodeNodeDomainRefMissing              Code = "validation_node_domain_ref_missing"
	CodeEdgeNodeRefMissing                Code = "validation_edge_node_ref_missing"
	CodeNodeOverlayIPOutOfCIDR            Code = "validation_node_overlay_ip_out_of_cidr"
	CodeNodeOverlayIPConflict             Code = "validation_node_overlay_ip_conflict"
	CodeDomainIDDuplicate                 Code = "validation_domain_id_duplicate"
	CodeNodeIDDuplicate                   Code = "validation_node_id_duplicate"
	CodeEdgeIDDuplicate                   Code = "validation_edge_id_duplicate"
	CodeNodeNameDuplicate                 Code = "validation_node_name_duplicate"
	CodeNodeNameInstallerCollision        Code = "validation_node_name_installer_collision"
	CodeNodeNameInterfaceCollision        Code = "validation_node_name_interface_collision"
	CodeNodeListenPortHostConflict        Code = "validation_node_listen_port_host_conflict"
	CodeNodeEffectivePortRangeOverflow    Code = "validation_node_effective_port_range_overflow"
	CodeNodeEffectivePortRangeOverlap     Code = "validation_node_effective_port_range_overlap"
	CodeNodeIsolated                      Code = "validation_node_isolated"
	CodeClientInboundRejected             Code = "validation_client_inbound_rejected"
	CodeClientTargetPeer                  Code = "validation_client_target_peer"
	CodeClientTargetClient                Code = "validation_client_target_client"
	CodeClientEndpointHostRequired        Code = "validation_client_endpoint_host_required"
	CodeClientNoOutboundEdge              Code = "validation_client_no_outbound_edge"
	CodeClientMultipleOutboundEdges       Code = "validation_client_multiple_outbound_edges"
	CodeClientRouterIDMeaningless         Code = "validation_client_router_id_meaningless"
	CodeClientExtraPrefixesMeaningless    Code = "validation_client_extra_prefixes_meaningless"
	CodeEdgeMimicPlatformUnsupported      Code = "validation_edge_mimic_platform_unsupported"
	CodePinClientPortPin                  Code = "validation_pin_client_port_pin"
	CodePinClientAllocationIgnored        Code = "validation_pin_client_allocation_ignored"
	CodePinPortIncomplete                 Code = "validation_pin_port_incomplete"
	CodePinTransitIPIncomplete            Code = "validation_pin_transit_ip_incomplete"
	CodePinLinkLocalIncomplete            Code = "validation_pin_link_local_incomplete"
	CodePinPortOutOfRange                 Code = "validation_pin_port_out_of_range"
	CodePinTransitIPInvalid               Code = "validation_pin_transit_ip_invalid"
	CodePinTransitIPOutOfCIDR             Code = "validation_pin_transit_ip_out_of_cidr"
	CodePinPortDuplicateCrossLink         Code = "validation_pin_port_duplicate_cross_link"
	CodePinTransitIPDuplicateCrossLink    Code = "validation_pin_transit_ip_duplicate_cross_link"
	CodePinLinkLocalDuplicateCrossLink    Code = "validation_pin_link_local_duplicate_cross_link"
	CodeEdgeEndpointNoMatch               Code = "validation_edge_endpoint_no_match"
	CodeEdgeDuplicateEnabledSameDirection Code = "validation_edge_duplicate_enabled_same_direction"
	CodeNodeInterfaceNameCollision        Code = "validation_node_interface_name_collision"
	CodeEdgeMultipleExplicitPrimary       Code = "validation_edge_multiple_explicit_primary"
	CodeEdgeBackupTouchesClient           Code = "validation_edge_backup_touches_client"
	CodeLinkNoPrimary                     Code = "validation_link_no_primary"
	CodeLinkEqualCost                     Code = "validation_link_equal_cost"
	CodeNATTargetUnreachable              Code = "validation_nat_target_unreachable"
	CodeNATDeadLink                       Code = "validation_nat_dead_link"
	CodeNATDoubleNATNoEndpoint            Code = "validation_nat_double_nat_no_endpoint"
	CodeNATNoOutboundToPublic             Code = "validation_nat_no_outbound_to_public"
)

// registry maps each Code to its English message TEMPLATE. {role} placeholders map 1:1 to the
// params passed at the call site. Single source of the CLI/curl message AND the i18n English
// fallback. No status field — a finding has none. A bijection test (code_test.go) pins every
// const to a registry entry and vice versa.
var registry = map[Code]string{
	CodeProjectIDRequired:                 "Project ID is required.",
	CodeProjectNameRequired:               "Project name is required.",
	CodeDomainNoneDefined:                 "At least one domain must be defined.",
	CodeDomainIDRequired:                  "Domain ID is required.",
	CodeDomainNameRequired:                "Domain name is required.",
	CodeDomainCIDREmpty:                   "CIDR must not be empty.",
	CodeDomainCIDRInvalid:                 "Invalid CIDR format: {cidr}.",
	CodeDomainCIDRNotIPv4:                 "CIDR must be an IPv4 network: {cidr} (IPv6 and other address families are not supported yet).",
	CodeDomainCIDRTooLarge:                "CIDR {cidr} is too large; the prefix length must not be shorter than /8 (it cannot be enumerated for allocation).",
	CodeDomainAllocationModeInvalid:       "Invalid allocation mode: {mode}. Allowed values: auto, manual.",
	CodeDomainRoutingModeUnimplemented:    "Routing mode {mode} is not implemented yet; only babel is currently supported (the only implemented routing mode).",
	CodeDomainRoutingModeInvalid:          "Invalid routing mode: {mode}; only babel is currently supported (the only implemented routing mode).",
	CodeDomainReservedRangeNotIPv4:        "Reserved range must be IPv4: {cidr} (IPv6 and other address families are not supported yet).",
	CodeDomainReservedRangeInvalid:        "Invalid reserved range format: {value}.",
	CodeDomainReservedAddressNotIPv4:      "Reserved address must be IPv4: {ip} (IPv6 and other address families are not supported yet).",
	CodeNodeIDRequired:                    "Node ID is required.",
	CodeNodeNameRequired:                  "Node name is required.",
	CodeNodeNameIllegalChars:              "Node name {name} contains illegal characters: only letters, digits, spaces, dot (.), underscore (_), and hyphen (-) are allowed; shell metacharacters such as quotes, backticks, $, and ; are forbidden.",
	CodeNodeDomainIDRequired:              "Node must reference a Domain.",
	CodeNodeRoleEmpty:                     "Node role must not be empty.",
	CodeNodeRoleInvalid:                   "Invalid role: {role}. Allowed values: peer, router, relay, gateway, client.",
	CodeNodePlatformUnsupported:           "Unsupported platform: {platform}. Allowed values: debian, ubuntu.",
	CodeNodeXDPModeInvalid:                "Invalid XDP mode: {mode}. Allowed values: skb, native (empty is equivalent to skb).",
	CodeNodeOverlayIPInvalid:              "Invalid overlay IP address: {ip}.",
	CodeNodeListenPortInvalid:             "Invalid listen port: {port}.",
	CodeNodeMTUOutOfRange:                 "MTU {mtu} is out of range: it must be between {low} and {high} (576 is the IPv4 datagram minimum; an out-of-range MTU is rejected by wg-quick).",
	CodeNodeSSHPortOutOfRange:             "ssh_port {port} is out of range: it must be between 1 and 65535.",
	CodeNodeRouterIDInvalid:               "Invalid router_id format: {id}. It must be in MAC-48 form (six colon-separated hex pairs, e.g. 02:11:22:33:44:55) or an IPv4 address; otherwise babeld will reject it.",
	CodeNodeExtraPrefixInvalid:            "Invalid extra route prefix format: {prefix} (it must be in CIDR form, e.g. 192.168.0.0/24).",
	CodeNodeExtraPrefixNotIPv4:            "Extra route prefix must be IPv4: {prefix} (IPv6 and other address families are not supported yet).",
	CodeNodeSSHHostIllegalChars:           "ssh_host {host} contains illegal characters: only letters, digits, dot (.), underscore (_), colon (:), @, and hyphen (-) are allowed; whitespace and shell metacharacters are forbidden.",
	CodeNodeSSHAliasIllegalChars:          "ssh_alias {alias} contains illegal characters: only letters, digits, dot (.), underscore (_), colon (:), @, and hyphen (-) are allowed; whitespace and shell metacharacters are forbidden.",
	CodeNodeSSHUserIllegalChars:           "ssh_user {user} contains illegal characters: only letters, digits, dot (.), underscore (_), colon (:), @, and hyphen (-) are allowed; whitespace and shell metacharacters are forbidden.",
	CodeNodeSSHKeyPathIllegalChars:        "ssh_key_path {path} contains illegal characters: only letters, digits, and path characters (. _ : @ / \\ ~ space and -) are allowed; shell metacharacters ($ ` \" ' ; | & < > ( ) etc.) are forbidden because the path is spliced into the operator's deploy shell command.",
	CodeEdgeIDRequired:                    "Edge ID is required.",
	CodeEdgeFromNodeIDRequired:            "Edge source node ID is required.",
	CodeEdgeToNodeIDRequired:              "Edge target node ID is required.",
	CodeEdgeTypeEmpty:                     "Edge type must not be empty.",
	CodeEdgeTypeInvalid:                   "Invalid edge type: {type}. Allowed values: direct, public-endpoint, relay-path, candidate.",
	CodeEdgeTransportInvalid:              "Invalid transport protocol: {transport}. Allowed values: udp, tcp.",
	CodeEdgeEndpointPortInvalid:           "Invalid endpoint port: {port}.",
	CodeEdgeRoleInvalid:                   "Invalid link role: {role}. Allowed values: primary, backup (empty is equivalent to primary).",
	CodeEdgeSelfLoop:                      "Edge source and target nodes must not be the same (self-loop).",
	CodeRoutePolicyReserved:               "route_policies is a reserved feature that is not yet implemented: no renderer consumes it, the compiler only passes it through verbatim, so it must be empty (detected {count} policies; please clear route_policies; for LAN bridging / route injection use extra_prefixes instead)",
	CodeNodeDomainRefMissing:              "Node {node} references a non-existent Domain {id}",
	CodeEdgeNodeRefMissing:                "Edge {id} references a non-existent node {other}",
	CodeNodeOverlayIPOutOfCIDR:            "Overlay IP {cidr} of node {node} is not within the CIDR {prefix} of Domain {name}",
	CodeNodeOverlayIPConflict:             "Overlay IP {cidr} conflicts: already used by node {other}, also assigned to node {node}",
	CodeDomainIDDuplicate:                 "Duplicate Domain ID: {id}",
	CodeNodeIDDuplicate:                   "Duplicate Node ID: {id}",
	CodeEdgeIDDuplicate:                   "Duplicate Edge ID: {id}",
	CodeNodeNameDuplicate:                 "Duplicate node name: node {other} and node {node} use the same name {name}",
	CodeNodeNameInstallerCollision:        "Node names produce the same installer script filename: node {other} and node {node} both normalize to {name}, which will cause silent skips or identity mismatches during deployment",
	CodeNodeNameInterfaceCollision:        "Node names produce the same WireGuard interface name: node {other} and node {node} both normalize to {name}, which will cause one interface configuration to overwrite the other",
	CodeNodeListenPortHostConflict:        "Node {node} and node {other} share the same listen port {port} on host {name}",
	CodeNodeEffectivePortRangeOverflow:    "Node {node} has an effective listen port range of {low}-{high} (base port {base} + {count} peer interfaces); the highest port {high} exceeds 65535 and will produce an undeployable WireGuard configuration",
	CodeNodeEffectivePortRangeOverlap:     "Node {node} (ports {low}-{high}) and node {other} (ports {other_low}-{other_high}) share host {name} and have overlapping effective listen port ranges; WireGuard interfaces on the same host will contend for the same ports",
	CodeNodeIsolated:                      "Node {node} ({id}) is isolated and not connected to any enabled edge",
	CodeClientInboundRejected:             "Client node {node} cannot accept inbound connections",
	CodeClientTargetPeer:                  "Client {node} cannot connect to peer {other} (peers do not forward traffic)",
	CodeClientTargetClient:                "Client {node} cannot connect to another client {other}",
	CodeClientEndpointHostRequired:        "Client {node} requires endpoint_host to reach the router",
	CodeClientNoOutboundEdge:              "Client {node} must have exactly one enabled outbound edge",
	CodeClientMultipleOutboundEdges:       "Client {node} has {count} outbound edges but must have exactly one (single wg0 interface)",
	CodeClientRouterIDMeaningless:         "Client {node} has router_id set but clients do not run Babel",
	CodeClientExtraPrefixesMeaningless:    "Client {node} has extra_prefixes set but clients do not announce routes",
	CodeEdgeMimicPlatformUnsupported:      "Edge {id} uses tcp transport (mimic), but endpoint node {node} has platform {platform} which is not a deployable Linux: mimic is an eBPF/kernel feature, so both ends of a tcp edge must be Linux (debian / ubuntu)",
	CodePinClientPortPin:                  "Edge {id} touches a client node but sets a port pin: clients use a single wg0 with no per-peer listen ports; please clear pinned_from_port / pinned_to_port on this edge",
	CodePinClientAllocationIgnored:        "Edge {id} touches a client node; its allocation pins will be ignored: clients use a single wg0 and do not participate in per-peer transit/link-local allocation",
	CodePinPortIncomplete:                 "Edge {id} has an incomplete listen port pin (only one end is pinned): pins must be set in pairs; please complete both pinned_from_port and pinned_to_port, or clear both",
	CodePinTransitIPIncomplete:            "Edge {id} has an incomplete transit IP pin (only one end is pinned): pins must be set in pairs; please complete both pinned_from_transit_ip and pinned_to_transit_ip, or clear both",
	CodePinLinkLocalIncomplete:            "Edge {id} has an incomplete link-local pin (only one end is pinned): pins must be set in pairs; please complete both pinned_from_link_local and pinned_to_link_local, or clear both",
	CodePinPortOutOfRange:                 "Port pin {port} for node {node} is out of range: it must be no lower than the node base listen port {base} and no higher than 65535 (clear this pin if renumbering is needed)",
	CodePinTransitIPInvalid:               "transit IP pin {cidr} is not a valid IP address",
	CodePinTransitIPOutOfCIDR:             "transit IP pin {cidr} is not within the edge transit address pool {prefix} (the pool may have been narrowed; clear this pin to renumber)",
	CodePinPortDuplicateCrossLink:         "Port pin {port} is occupied by two different links on the node: edge {other} and edge {id} pin the same listen port on the same node",
	CodePinTransitIPDuplicateCrossLink:    "transit IP pin {cidr} is occupied by two different links: edge {other} and edge {id} pin the same transit address",
	CodePinLinkLocalDuplicateCrossLink:    "link-local pin {cidr} is occupied by two different links: edge {other} and edge {id} pin the same link-local address",
	CodeEdgeEndpointNoMatch:               "Edge {id} dials {other} but target {node} has no matching public endpoint (the endpoint snapshot may be stale after a node edit)",
	CodeEdgeDuplicateEnabledSameDirection: "Edge {id} and edge {other} connect the same pair of nodes (same direction) and both belong to the primary class; only the first takes effect at compile time and this edge endpoint settings will be ignored; please delete or disable the redundant edge — if redundant backup was intended, set this edge role to backup so it becomes an independent backup link",
	CodeNodeInterfaceNameCollision:        "Node {node} has two links generating the same WireGuard interface name {name}: {prefix} collides with {other}, one interface configuration will overwrite the other; please rename one of the colliding nodes to eliminate the 4-digit hash collision",
	CodeEdgeMultipleExplicitPrimary:       "Edge {id} and edge {other} connect the same pair of nodes and are both explicitly marked role primary: each pair of nodes may have at most one primary link, the compiler folds the primary class and ignores the rest; please change one to backup or clear its role",
	CodeEdgeBackupTouchesClient:           "Edge {id} touches a client node but is marked as backup: clients use a single wg0, do not run Babel, and have no per-peer interfaces or cost-based failover, so backup links are meaningless for them; please clear this edge role or delete the edge",
	CodeLinkNoPrimary:                     "All links between node {node} and {other} are backup with no primary link: Babel will forward across the backup links with no primary/backup distinction (a role flip may have left out the primary); please change one to primary or clear its role",
	CodeLinkEqualCost:                     "There are {count} links between node {node} and {other} but all resolved costs are identical ({low}): Babel cannot prefer any one of them and the configuration cannot express failover; please set distinct costs per link via role backup or priority/weight",
	CodeNATTargetUnreachable:              "Edge {edge}: target node {node} has no public IP and does not accept inbound connections; the peer will not be able to initiate a connection to it",
	CodeNATDeadLink:                       "Edge {edge}: nodes {from} and {to} are both behind NAT, neither direction provides an endpoint host address, and neither end accepts inbound connections; the direct tunnel cannot be established (confirmed dead link). Configure a public endpoint on one end, or route through a relay instead",
	CodeNATDoubleNATNoEndpoint:            "Edge {edge}: nodes {from} and {to} are both behind NAT and provide no endpoint host address; the direct tunnel cannot be established (a relay or public relay is required)",
	CodeNATNoOutboundToPublic:             "Node {name} ({id}) is behind NAT and has no outbound connection to any public, inbound-capable, or relay node; it will not be able to join the overlay",
}

// P is one template parameter, keyword-style (P{"cidr", v}) so the ~91 call sites cannot
// transpose positional argument slots — a misuse-resistance refinement adopted from the debate.
type P struct{ K, V string }

// ValidationError is one finding. Code+Params drive client localization (the panel's
// 'error.<code>' catalog + {role} interpolation); Message is the server-rendered English default
// (CLI/curl + the i18n English fallback so an English operator never sees another language). Field
// is the dotted path to the offending field; Level is "error" | "warning".
type ValidationError struct {
	Field   string            `json:"field"`
	Code    string            `json:"code"`
	Params  map[string]string `json:"params,omitempty"`
	Message string            `json:"message"`
	Level   string            `json:"level"`
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.Level, e.Field, e.Message)
}

// ValidationResult collects findings from a validation pass.
type ValidationResult struct {
	Errors   []ValidationError `json:"errors"`
	Warnings []ValidationError `json:"warnings"`
}

// AddError / AddWarning code a finding at the source: callers pass a Code + keyword params,
// never English prose, so the rendered message can never drift from the code.
func (r *ValidationResult) AddError(field string, code Code, params ...P) {
	r.Errors = append(r.Errors, newFinding(field, code, "error", params))
}

func (r *ValidationResult) AddWarning(field string, code Code, params ...P) {
	r.Warnings = append(r.Warnings, newFinding(field, code, "warning", params))
}

func (r *ValidationResult) IsValid() bool { return len(r.Errors) == 0 }

// newFinding renders the English Message from the registry template + params AT THE SOURCE, so
// the message can never drift from the code. Panics on an unregistered code — a programming
// error caught at first use (mirrors apierr.New) and a backstop the validator test suite trips
// for any path that emits an uncoded finding.
func newFinding(field string, code Code, level string, params []P) ValidationError {
	tmpl, ok := registry[code]
	if !ok {
		panic("validator: unregistered code " + string(code) + " — add it to the const block AND registry in code.go")
	}
	var m map[string]string
	if len(params) > 0 {
		m = make(map[string]string, len(params))
		for _, p := range params {
			m[p.K] = p.V
		}
	}
	return ValidationError{Field: field, Code: string(code), Params: m, Message: interpolate(tmpl, m), Level: level}
}

// interpolate replaces {name} with params[name]; an unknown placeholder is left intact (a
// visible "{name}", never a panic). Single-pass scan mirroring apierr.interpolate and the
// frontend t() engine byte-for-byte, so substitution matches on every side.
func interpolate(tmpl string, params map[string]string) string {
	if len(params) == 0 || !strings.ContainsRune(tmpl, '{') {
		return tmpl
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		if tmpl[i] == '{' {
			if rel := strings.IndexByte(tmpl[i:], '}'); rel > 1 {
				name := tmpl[i+1 : i+rel]
				if v, ok := params[name]; ok {
					b.WriteString(v)
					i += rel + 1
					continue
				}
			}
		}
		b.WriteByte(tmpl[i])
		i++
	}
	return b.String()
}
