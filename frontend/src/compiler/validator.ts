// Validator — the TypeScript mirror of internal/validator (the AUTHORITATIVE Go oracle). This module
// reproduces ValidateSchema (internal/validator/schema.go) byte-for-behavior: it emits the EXACT same
// finding-code SET as the Go validator for any topology, pinned by the plan-5 conformance harness's
// verdict.validator channel.
//
// Codes are STRING LITERALS equal to the Go validator.Code values (internal/validator/code.go). There is
// NO codes.ts (plan-5 superseded it); the canonical code strings live in the Go source and the FE i18n
// 'error.<code>' catalog, and the catalog-sync guard keeps them in lockstep. The enum below is a typed
// convenience surface whose VALUES are those literals.
//
// THE MUTATIONS (round-trip semantics): ValidateSchema MUTATES the topology in place before validating —
// it normalizes an empty domain.routing_mode to "babel" (schema.go:220-222) and an empty edge.transport
// to "udp" (schema.go:433-435), then runs the enum check on the normalized value. The mutation is
// observable downstream (a subsequent compile sees the normalized topology), so this port mutates the
// SAME topology object the caller passes and replicates the normalize-then-validate ORDER exactly.
//
// This module ports BOTH halves: the SCHEMA pass (ValidateSchema, internal/validator/schema.go) and the
// SEMANTIC pass (ValidateSemantic, internal/validator/semantic.go + nat.go, ~20 sub-validators). The
// top-level validate() runs schema-THEN-semantic in the order /api/validate uses (HandleValidate runs
// both passes and concatenates their findings) and returns the combined {errors, warnings}. The semantic
// pass shares the Phase-1 leaves (linkid, naming, cidr, allocconst) so its grouping/dedup/cost math is
// byte-identical to the Go oracle.

import type { Domain, Edge, Node, Topology } from '../types/topology';
import { canonicalIP, contains, parseCIDR, parseCIDRFamily, parseIPFamily } from './cidr';
import { isBackup, linkKey, pinKey } from './linkid';
import { safeInstallerFileName, wgInterfaceName, wgInterfaceNameForEdge } from './naming';
import { BackupDefaultLinkCost, DefaultTransitCIDR, MinPinnedPort } from './allocconst';

// Code is the typed set of validation-finding identifiers. Each value is the exact validator.Code
// string literal from internal/validator/code.go (the join key to the FE 'error.<code>' catalog). Only
// the codes the SCHEMA pass emits are listed here; the semantic pass adds the rest.
export const Code = {
  ProjectIDRequired: 'validation_project_id_required',
  ProjectNameRequired: 'validation_project_name_required',
  DomainNoneDefined: 'validation_domain_none_defined',
  DomainIDRequired: 'validation_domain_id_required',
  DomainNameRequired: 'validation_domain_name_required',
  DomainCIDREmpty: 'validation_domain_cidr_empty',
  DomainCIDRInvalid: 'validation_domain_cidr_invalid',
  DomainCIDRNotIPv4: 'validation_domain_cidr_not_ipv4',
  DomainCIDRTooLarge: 'validation_domain_cidr_too_large',
  DomainTransitCIDRInvalid: 'validation_domain_transit_cidr_invalid',
  DomainTransitCIDRNotIPv4: 'validation_domain_transit_cidr_not_ipv4',
  DomainTransitCIDRTooLarge: 'validation_domain_transit_cidr_too_large',
  DomainTransitCIDRTooSmall: 'validation_domain_transit_cidr_too_small',
  DomainAllocationModeInvalid: 'validation_domain_allocation_mode_invalid',
  DomainRoutingModeUnimplemented: 'validation_domain_routing_mode_unimplemented',
  DomainRoutingModeInvalid: 'validation_domain_routing_mode_invalid',
  DomainReservedRangeNotIPv4: 'validation_domain_reserved_range_not_ipv4',
  DomainReservedRangeInvalid: 'validation_domain_reserved_range_invalid',
  DomainReservedAddressNotIPv4: 'validation_domain_reserved_address_not_ipv4',
  NodeIDRequired: 'validation_node_id_required',
  NodeNameRequired: 'validation_node_name_required',
  NodeNameIllegalChars: 'validation_node_name_illegal_chars',
  NodeDomainIDRequired: 'validation_node_domain_id_required',
  NodeRoleEmpty: 'validation_node_role_empty',
  NodeRoleInvalid: 'validation_node_role_invalid',
  NodeDeploymentModeInvalid: 'validation_node_deployment_mode_invalid',
  NodePlatformUnsupported: 'validation_node_platform_unsupported',
  NodeXDPModeInvalid: 'validation_node_xdp_mode_invalid',
  NodeOverlayIPInvalid: 'validation_node_overlay_ip_invalid',
  NodeWGPublicKeyInvalid: 'validation_node_wg_public_key_invalid',
  NodeMTUOutOfRange: 'validation_node_mtu_out_of_range',
  NodeSSHPortOutOfRange: 'validation_node_ssh_port_out_of_range',
  NodeRouterIDInvalid: 'validation_node_router_id_invalid',
  NodeExtraPrefixInvalid: 'validation_node_extra_prefix_invalid',
  NodeExtraPrefixNotIPv4: 'validation_node_extra_prefix_not_ipv4',
  NodeSSHHostIllegalChars: 'validation_node_ssh_host_illegal_chars',
  NodeSSHAliasIllegalChars: 'validation_node_ssh_alias_illegal_chars',
  NodeSSHUserIllegalChars: 'validation_node_ssh_user_illegal_chars',
  NodeSSHKeyPathIllegalChars: 'validation_node_ssh_key_path_illegal_chars',
  NodePublicEndpointHostIllegalChars: 'validation_node_public_endpoint_host_illegal_chars',
  EdgeIDRequired: 'validation_edge_id_required',
  EdgeFromNodeIDRequired: 'validation_edge_from_node_id_required',
  EdgeToNodeIDRequired: 'validation_edge_to_node_id_required',
  EdgeTypeEmpty: 'validation_edge_type_empty',
  EdgeTypeInvalid: 'validation_edge_type_invalid',
  EdgeTransportInvalid: 'validation_edge_transport_invalid',
  EdgeEndpointHostIllegalChars: 'validation_edge_endpoint_host_illegal_chars',
  EdgeEndpointPortInvalid: 'validation_edge_endpoint_port_invalid',
  EdgeRoleInvalid: 'validation_edge_role_invalid',
  EdgeSelfLoop: 'validation_edge_self_loop',
  TopologyTooManyNodes: 'validation_topology_too_many_nodes',
  TopologyTooManyEdges: 'validation_topology_too_many_edges',
  TopologyTooManyDomains: 'validation_topology_too_many_domains',
  TopologyTooManyReservedRanges: 'validation_topology_too_many_reserved_ranges',
  TopologySchemaVersionUnsupported: 'validation_topology_schema_version_unsupported',
  // --- semantic-pass codes (internal/validator/semantic.go + nat.go) ---
  RoutePolicyReserved: 'validation_routepolicy_reserved',
  NodeDomainRefMissing: 'validation_node_domain_ref_missing',
  EdgeNodeRefMissing: 'validation_edge_node_ref_missing',
  NodeOverlayIPOutOfCIDR: 'validation_node_overlay_ip_out_of_cidr',
  NodeOverlayIPConflict: 'validation_node_overlay_ip_conflict',
  DomainIDDuplicate: 'validation_domain_id_duplicate',
  NodeIDDuplicate: 'validation_node_id_duplicate',
  EdgeIDDuplicate: 'validation_edge_id_duplicate',
  NodeNameDuplicate: 'validation_node_name_duplicate',
  NodeNameInstallerCollision: 'validation_node_name_installer_collision',
  NodeNameInterfaceCollision: 'validation_node_name_interface_collision',
  NodeEffectivePortRangeOverflow: 'validation_node_effective_port_range_overflow',
  NodeIsolated: 'validation_node_isolated',
  ClientInboundRejected: 'validation_client_inbound_rejected',
  ClientTargetPeer: 'validation_client_target_peer',
  ClientTargetClient: 'validation_client_target_client',
  ClientEndpointHostRequired: 'validation_client_endpoint_host_required',
  ClientNoOutboundEdge: 'validation_client_no_outbound_edge',
  ClientMultipleOutboundEdges: 'validation_client_multiple_outbound_edges',
  ClientRouterIDMeaningless: 'validation_client_router_id_meaningless',
  ClientExtraPrefixesMeaningless: 'validation_client_extra_prefixes_meaningless',
  EdgeMimicPlatformUnsupported: 'validation_edge_mimic_platform_unsupported',
  PinClientPortPin: 'validation_pin_client_port_pin',
  PinClientAllocationIgnored: 'validation_pin_client_allocation_ignored',
  PinPortIncomplete: 'validation_pin_port_incomplete',
  PinTransitIPIncomplete: 'validation_pin_transit_ip_incomplete',
  PinLinkLocalIncomplete: 'validation_pin_link_local_incomplete',
  PinPortOutOfRange: 'validation_pin_port_out_of_range',
  PinTransitIPInvalid: 'validation_pin_transit_ip_invalid',
  PinTransitIPOutOfCIDR: 'validation_pin_transit_ip_out_of_cidr',
  PinPortDuplicateCrossLink: 'validation_pin_port_duplicate_cross_link',
  PinTransitIPDuplicateCrossLink: 'validation_pin_transit_ip_duplicate_cross_link',
  PinLinkLocalDuplicateCrossLink: 'validation_pin_link_local_duplicate_cross_link',
  EdgeEndpointNoMatch: 'validation_edge_endpoint_no_match',
  EdgeDuplicateEnabledSameDirection: 'validation_edge_duplicate_enabled_same_direction',
  NodeInterfaceNameCollision: 'validation_node_interface_name_collision',
  EdgeMultipleExplicitPrimary: 'validation_edge_multiple_explicit_primary',
  EdgeBackupTouchesClient: 'validation_edge_backup_touches_client',
  LinkNoPrimary: 'validation_link_no_primary',
  LinkEqualCost: 'validation_link_equal_cost',
  NATTargetUnreachable: 'validation_nat_target_unreachable',
  NATDeadLink: 'validation_nat_dead_link',
  NATDoubleNATNoEndpoint: 'validation_nat_double_nat_no_endpoint',
  NATNoOutboundToPublic: 'validation_nat_no_outbound_to_public',
} as const;

export type CodeValue = (typeof Code)[keyof typeof Code];

// ValidationError is one finding, mirroring internal/validator/code.go's ValidationError field-for-field
// (and the FE wire type frontend/src/types/topology.ts:ValidationError). Field is the dotted path, code
// is the stable join key, params drives client localization, message is the server-rendered English
// default, level is "error" | "warning".
export interface ValidationError {
  field: string;
  code: string;
  params?: Record<string, string>;
  message: string;
  level: 'error' | 'warning';
}

// ValidationResult collects findings from a validation pass, mirroring validator.ValidationResult.
export interface ValidationResult {
  errors: ValidationError[];
  warnings: ValidationError[];
}

// P is one keyword-style template parameter (mirrors validator.P), so call sites cannot transpose
// positional slots.
type P = { k: string; v: string };

// addError / addWarning code a finding at the source: the caller passes a Code + keyword params, never
// English prose, so the rendered message can never drift from the code. Mirrors
// ValidationResult.AddError / AddWarning + newFinding (code.go:256-283).
function addError(result: ValidationResult, field: string, code: string, ...params: P[]): void {
  result.errors.push(newFinding(field, code, 'error', params));
}

function addWarning(result: ValidationResult, field: string, code: string, ...params: P[]): void {
  result.warnings.push(newFinding(field, code, 'warning', params));
}

function newFinding(field: string, code: string, level: 'error' | 'warning', params: P[]): ValidationError {
  let m: Record<string, string> | undefined;
  if (params.length > 0) {
    m = {};
    for (const p of params) {
      m[p.k] = p.v;
    }
  }
  const tmpl = registry[code];
  // The Go newFinding panics on an unregistered code (a programming error). Here every code the schema
  // pass emits is in the registry; fall back to the bare code string for the message rather than throw,
  // since the conformance gate compares the CODE, not the message.
  const message = tmpl !== undefined ? interpolate(tmpl, m) : code;
  return { field, code, params: m, message, level };
}

// interpolate replaces {name} with params[name]; an unknown placeholder is left intact (a visible
// "{name}", never a throw). Single-pass scan mirroring validator.interpolate (code.go:288-308)
// byte-for-byte so substitution matches on every side.
function interpolate(tmpl: string, params: Record<string, string> | undefined): string {
  if (!params || Object.keys(params).length === 0 || tmpl.indexOf('{') < 0) {
    return tmpl;
  }
  let b = '';
  for (let i = 0; i < tmpl.length; ) {
    if (tmpl[i] === '{') {
      const rel = tmpl.indexOf('}', i);
      // Go requires rel > 1 relative to i (a non-empty name between the braces); rel - i > 1 here.
      if (rel >= 0 && rel - i > 1) {
        const name = tmpl.slice(i + 1, rel);
        if (Object.prototype.hasOwnProperty.call(params, name)) {
          b += params[name];
          i = rel + 1;
          continue;
        }
      }
    }
    b += tmpl[i];
    i++;
  }
  return b;
}

// goQuote reproduces Go's fmt.Sprintf("%q", s): a Go-syntax double-quoted string literal. Printable
// Unicode passes through; the standard C escapes apply to the well-known control chars; '"' and '\'
// are backslash-escaped; other control / non-printable ASCII bytes render as \xHH. This drives the
// {name}/{host}/... params the schema pass quotes (node name, ssh fields, router_id, endpoint hosts).
// It is display-only — the conformance verdict compares the CODE, not the message — but a faithful
// rendering keeps the params byte-equal to Go for the realistic (printable) inputs.
function goQuote(s: string): string {
  let out = '"';
  for (const ch of s) {
    const cp = ch.codePointAt(0)!;
    switch (ch) {
      case '"':
        out += '\\"';
        continue;
      case '\\':
        out += '\\\\';
        continue;
      case '\x07':
        out += '\\a';
        continue;
      case '\b':
        out += '\\b';
        continue;
      case '\f':
        out += '\\f';
        continue;
      case '\n':
        out += '\\n';
        continue;
      case '\r':
        out += '\\r';
        continue;
      case '\t':
        out += '\\t';
        continue;
      case '\v':
        out += '\\v';
        continue;
    }
    // Printable ASCII passes through verbatim.
    if (cp >= 0x20 && cp < 0x7f) {
      out += ch;
      continue;
    }
    // Other ASCII control / DEL → \xHH (two hex digits, lowercase, matching Go).
    if (cp < 0x80) {
      out += '\\x' + cp.toString(16).padStart(2, '0');
      continue;
    }
    // Non-ASCII: Go renders a printable rune as itself. The common validator inputs are printable
    // Unicode (e.g. 中文, emoji), so emit the character verbatim — matching Go for all printable runes.
    out += ch;
  }
  return out + '"';
}

// --- charset regexes (schema.go:18-52). Mirrored exactly; JS RegExp shares Go's RE2-compatible charset
// syntax for these patterns (no backtracking constructs). ---

// nodeNameCharset (schema.go:18): letters, digits, spaces, dots, underscores, hyphens.
const nodeNameCharset = /^[A-Za-z0-9 ._-]+$/;
// sshFieldCharset (schema.go:25): letters, digits, dots, underscores, colons, @, hyphens.
const sshFieldCharset = /^[A-Za-z0-9._:@-]+$/;
// sshKeyPathCharset (schema.go:37): adds path characters (/ \ ~ space) to the ssh-field set.
const sshKeyPathCharset = /^[A-Za-z0-9._:@/\\~ -]+$/;
// endpointHostCharset (schema.go:46): letters, digits, dot, underscore, colon, square brackets, hyphen.
const endpointHostCharset = /^[A-Za-z0-9._:[\]-]+$/;
// routerIDMAC48 (schema.go:52): six colon-separated hex pairs.
const routerIDMAC48 = /^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$/;

// --- numeric bounds (schema.go) ---
const mtuMinimum = 576; // schema.go:58
const mtuMaximum = 65535; // schema.go:60
const maxTopologyNodes = 2000; // schema.go:74
const maxTopologyEdges = 10000; // schema.go:75
const maxTopologyDomains = 1000; // schema.go:80
const maxReservedRangesPerDomain = 1000; // schema.go:86
// CurrentAllocSchemaVersion (internal/model/topology.go:11): the highest pin-schema version this build
// understands; the validator fails closed on any input stamped HIGHER (forward-compat guard).
const currentAllocSchemaVersion = 1;

// EdgeRolePrimary / EdgeRoleBackup (internal/model/topology.go:134-136). An empty role is equivalent to
// primary (both in the primary class).
const edgeRolePrimary = 'primary';
const edgeRoleBackup = 'backup';

// topologyExceedsBounds mirrors schema.go:95-110: a topology is rejected at the root before any further
// validation when it is too large (count bound) OR stamped with a future alloc-schema version.
function topologyExceedsBounds(topo: Topology): boolean {
  const allocVer = topo.alloc_schema_version ?? 0;
  if (
    allocVer > currentAllocSchemaVersion ||
    topo.nodes.length > maxTopologyNodes ||
    topo.edges.length > maxTopologyEdges ||
    topo.domains.length > maxTopologyDomains
  ) {
    return true;
  }
  for (const d of topo.domains) {
    if ((d.reserved_ranges?.length ?? 0) > maxReservedRangesPerDomain) {
      return true;
    }
  }
  return false;
}

// validate runs the FULL validation in the order /api/validate uses (HandleValidate): the schema pass
// (Pass 1) THEN the semantic pass (Pass 2), concatenating their findings into ONE combined result. It
// mirrors the product flow: ValidateSchema first (which normalizes routing_mode/transport in place — the
// semantic pass then sees the normalized topology), then ValidateSemantic. The topology object is mutated
// by the schema normalizations (the mutations round-trip downstream); a subsequent compile sees the same
// normalized topology.
export function validate(topo: Topology): ValidationResult {
  const schema = validateSchema(topo);
  const semantic = validateSemantic(topo);
  return {
    errors: [...schema.errors, ...semantic.errors],
    warnings: [...schema.warnings, ...semantic.warnings],
  };
}

// validateSchema runs Pass 1 of validation: the structural schema checks over a topology's project,
// domains, nodes, and edges. It NORMALIZES a few fields in place (routing_mode, transport) and returns a
// ValidationResult accumulating every schema error and warning. Mirrors validator.ValidateSchema
// (schema.go:112-158). The topology object is mutated (the normalizations round-trip), matching Go's
// pointer-receiver mutation.
export function validateSchema(topo: Topology): ValidationResult {
  const result: ValidationResult = { errors: [], warnings: [] };

  // Topology-root guards reported HERE (schema is the canonical reporter) and short-circuiting: an
  // oversized or future-format topology is rejected outright (schema.go:119-143).
  if (topologyExceedsBounds(topo)) {
    const allocVer = topo.alloc_schema_version ?? 0;
    if (allocVer > currentAllocSchemaVersion) {
      addError(result, 'alloc_schema_version', Code.TopologySchemaVersionUnsupported,
        { k: 'version', v: String(allocVer) }, { k: 'max', v: String(currentAllocSchemaVersion) });
    }
    if (topo.nodes.length > maxTopologyNodes) {
      addError(result, 'nodes', Code.TopologyTooManyNodes,
        { k: 'count', v: String(topo.nodes.length) }, { k: 'max', v: String(maxTopologyNodes) });
    }
    if (topo.edges.length > maxTopologyEdges) {
      addError(result, 'edges', Code.TopologyTooManyEdges,
        { k: 'count', v: String(topo.edges.length) }, { k: 'max', v: String(maxTopologyEdges) });
    }
    if (topo.domains.length > maxTopologyDomains) {
      addError(result, 'domains', Code.TopologyTooManyDomains,
        { k: 'count', v: String(topo.domains.length) }, { k: 'max', v: String(maxTopologyDomains) });
    }
    for (let i = 0; i < topo.domains.length; i++) {
      const n = topo.domains[i].reserved_ranges?.length ?? 0;
      if (n > maxReservedRangesPerDomain) {
        addError(result, `domains[${i}].reserved_ranges`, Code.TopologyTooManyReservedRanges,
          { k: 'count', v: String(n) }, { k: 'max', v: String(maxReservedRangesPerDomain) });
      }
    }
    return result;
  }

  validateProjectSchema(topo, result);
  validateDomainsSchema(topo, result);
  validateNodesSchema(topo, result);
  validateEdgesSchema(topo, result);

  return result;
}

// validateProjectSchema mirrors schema.go:160-167.
function validateProjectSchema(topo: Topology, result: ValidationResult): void {
  if (topo.project.id === '') {
    addError(result, 'project.id', Code.ProjectIDRequired);
  }
  if (topo.project.name === '') {
    addError(result, 'project.name', Code.ProjectNameRequired);
  }
}

// validateDomainsSchema mirrors schema.go:169-275. It NORMALIZES domain.routing_mode in place (empty →
// "babel", schema.go:220-222) before the enum check, so the mutation round-trips.
function validateDomainsSchema(topo: Topology, result: ValidationResult): void {
  if (topo.domains.length === 0) {
    addError(result, 'domains', Code.DomainNoneDefined);
    return;
  }

  for (let i = 0; i < topo.domains.length; i++) {
    // Mutate the actual domain object so the routing_mode normalization persists (round-trip).
    const domain = topo.domains[i];
    const prefix = `domains[${i}]`;

    if (domain.id === '') {
      addError(result, prefix + '.id', Code.DomainIDRequired);
    }
    if (domain.name === '') {
      addError(result, prefix + '.name', Code.DomainNameRequired);
    }

    // CIDR format validation (schema.go:190-206).
    if (domain.cidr === '') {
      addError(result, prefix + '.cidr', Code.DomainCIDREmpty);
    } else {
      const info = parseCIDRFamily(domain.cidr);
      if (info === null) {
        addError(result, prefix + '.cidr', Code.DomainCIDRInvalid, { k: 'cidr', v: domain.cidr });
      } else if (!info.isIPv4) {
        addError(result, prefix + '.cidr', Code.DomainCIDRNotIPv4, { k: 'cidr', v: domain.cidr });
      } else if (info.ones < 8) {
        addError(result, prefix + '.cidr', Code.DomainCIDRTooLarge, { k: 'cidr', v: domain.cidr });
      }
    }

    // AllocationMode (schema.go:208-212). The wire value is a plain string (an empty value is legal and
    // skips the enum check), so read it as a string — the frozen TS literal-union is narrower than the
    // deserialized reality the Go validator sees.
    const allocationMode = domain.allocation_mode as string;
    if (allocationMode !== '' && allocationMode !== 'auto' && allocationMode !== 'manual') {
      addError(result, prefix + '.allocation_mode', Code.DomainAllocationModeInvalid, { k: 'mode', v: allocationMode });
    }

    // RoutingMode normalization + validation (schema.go:214-233). Normalize empty → "babel" and write it
    // back so it round-trips, THEN run the enum check (the empty value cannot bypass it). Read as a plain
    // string for the same reason as allocation_mode.
    if ((domain.routing_mode as string) === '') {
      domain.routing_mode = 'babel';
    }
    switch (domain.routing_mode as string) {
      case 'babel':
        // The only implemented mode; allow it.
        break;
      case 'static':
      case 'none':
        addError(result, prefix + '.routing_mode', Code.DomainRoutingModeUnimplemented, { k: 'mode', v: domain.routing_mode });
        break;
      default:
        addError(result, prefix + '.routing_mode', Code.DomainRoutingModeInvalid, { k: 'mode', v: domain.routing_mode });
        break;
    }

    // ReservedRanges validation (schema.go:235-253): each entry must be a parseable CIDR or IP, IPv4.
    const reserved = domain.reserved_ranges ?? [];
    for (let j = 0; j < reserved.length; j++) {
      const rr = reserved[j];
      const rrPrefix = `${prefix}.reserved_ranges[${j}]`;
      const cidr = parseCIDRFamily(rr);
      if (cidr !== null) {
        // Parsed as a CIDR: require the IPv4 family.
        if (!cidr.isIPv4) {
          addError(result, rrPrefix, Code.DomainReservedRangeNotIPv4, { k: 'cidr', v: rr });
        }
        continue;
      }
      // Fall back to a single IP: require it parseable + IPv4.
      const ip = parseIPFamily(rr);
      if (ip === null) {
        addError(result, rrPrefix, Code.DomainReservedRangeInvalid, { k: 'value', v: rr });
      } else if (!ip.isIPv4) {
        addError(result, rrPrefix, Code.DomainReservedAddressNotIPv4, { k: 'ip', v: rr });
      }
    }

    // transit_cidr validation (schema.go:255-273): parseable, IPv4-only, large enough to hold a transit
    // pair (prefix /30 or shorter, i.e. ones <= 30) and not shorter than /8. Empty falls back to the
    // default 10.10.0.0/24 in the compiler and needs no validation.
    if (domain.transit_cidr !== undefined && domain.transit_cidr !== '') {
      const tInfo = parseCIDRFamily(domain.transit_cidr);
      if (tInfo === null) {
        addError(result, prefix + '.transit_cidr', Code.DomainTransitCIDRInvalid, { k: 'cidr', v: domain.transit_cidr });
      } else if (!tInfo.isIPv4) {
        addError(result, prefix + '.transit_cidr', Code.DomainTransitCIDRNotIPv4, { k: 'cidr', v: domain.transit_cidr });
      } else if (tInfo.ones < 8) {
        addError(result, prefix + '.transit_cidr', Code.DomainTransitCIDRTooLarge, { k: 'cidr', v: domain.transit_cidr });
      } else if (tInfo.ones > 30) {
        addError(result, prefix + '.transit_cidr', Code.DomainTransitCIDRTooSmall, { k: 'cidr', v: domain.transit_cidr });
      }
    }
  }
}

// validateNodesSchema mirrors schema.go:277-402.
// validWGPublicKey mirrors validator.ValidWGPublicKey (schema.go): a WireGuard public key is 32 bytes
// of standard base64 = exactly 43 base64 chars + one '=' pad. This regex is DELIBERATELY STRICTER than
// base64-decoding: Go's base64 decoder (and JS atob) silently strip CR/LF and surrounding whitespace, so
// a decode-and-check-length would ACCEPT a key with an embedded newline — the exact config-injection
// vector this guards against. Do NOT "simplify" it back to a decoder. Byte-identical to the Go regex.
const wgPublicKeyPattern = /^[A-Za-z0-9+/]{43}=$/;
function validWGPublicKey(s: string): boolean {
  return wgPublicKeyPattern.test(s);
}

function validateNodesSchema(topo: Topology, result: ValidationResult): void {
  for (let i = 0; i < topo.nodes.length; i++) {
    const node = topo.nodes[i];
    const prefix = `nodes[${i}]`;

    if (node.id === '') {
      addError(result, prefix + '.id', Code.NodeIDRequired);
    }
    if (node.name === '') {
      addError(result, prefix + '.name', Code.NodeNameRequired);
    } else if (!nodeNameCharset.test(node.name)) {
      addError(result, prefix + '.name', Code.NodeNameIllegalChars, { k: 'name', v: goQuote(node.name) });
    }
    if (node.domain_id === '') {
      addError(result, prefix + '.domain_id', Code.NodeDomainIDRequired);
    }

    // Role (schema.go:296-302). string(node.role) so an out-of-enum value passes through to the check.
    const role = node.role as string;
    if (role === '') {
      addError(result, prefix + '.role', Code.NodeRoleEmpty);
    } else if (role !== 'peer' && role !== 'router' && role !== 'relay' && role !== 'gateway' && role !== 'client') {
      addError(result, prefix + '.role', Code.NodeRoleInvalid, { k: 'role', v: role });
    }

    // Deployment mode (optional; empty == managed) (schema.go). Reject a typo rather than silently
    // treating it as managed, since it changes custody/admission behavior.
    const deploymentMode = (node.deployment_mode ?? '') as string;
    if (deploymentMode !== '' && deploymentMode !== 'managed' && deploymentMode !== 'manual') {
      addError(result, prefix + '.deployment_mode', Code.NodeDeploymentModeInvalid, { k: 'mode', v: deploymentMode });
    }

    // Platform (optional; unsupported → warning) (schema.go:304-310). Lowercased for the membership test.
    const platform = (node.platform ?? '') as string;
    if (platform !== '') {
      const lowered = platform.toLowerCase();
      if (lowered !== 'debian' && lowered !== 'ubuntu') {
        addWarning(result, prefix + '.platform', Code.NodePlatformUnsupported, { k: 'platform', v: platform });
      }
    }

    // XDPMode (schema.go:312-321): only skb/native legal; empty equivalent to skb.
    const xdp = (node.xdp_mode ?? '') as string;
    if (xdp !== '') {
      if (xdp !== 'skb' && xdp !== 'native') {
        addError(result, prefix + '.xdp_mode', Code.NodeXDPModeInvalid, { k: 'mode', v: xdp });
      }
    }

    // OverlayIP (optional; parseable IP when set) (schema.go:323-328). net.ParseIP accepts both families.
    const overlayIP = node.overlay_ip ?? '';
    if (overlayIP !== '') {
      if (parseIPFamily(overlayIP) === null) {
        addError(result, prefix + '.overlay_ip', Code.NodeOverlayIPInvalid, { k: 'ip', v: overlayIP });
      }
    }

    // WireGuardPublicKey (optional here; check only when present) (schema.go). It is rendered verbatim
    // into peers' root-parsed wg configs, so a malformed value is rejected. Mirrors
    // validator.ValidWGPublicKey (base64.StdEncoding + 32 bytes): a 32-byte Curve25519 key is exactly
    // 43 standard-base64 chars + one '=' pad.
    const wgPub = node.wireguard_public_key ?? '';
    if (wgPub !== '' && !validWGPublicKey(wgPub)) {
      addError(result, prefix + '.wireguard_public_key', Code.NodeWGPublicKeyInvalid, { k: 'key', v: goQuote(wgPub) });
    }

    // MTU (schema.go:330-336): 0 means system default; non-zero must be in [576, 65535].
    const mtu = node.mtu ?? 0;
    if (mtu !== 0 && (mtu < mtuMinimum || mtu > mtuMaximum)) {
      addError(result, prefix + '.mtu', Code.NodeMTUOutOfRange,
        { k: 'mtu', v: String(mtu) }, { k: 'low', v: String(mtuMinimum) }, { k: 'high', v: String(mtuMaximum) });
    }

    // SSHPort (schema.go:338-343): 0 means default 22; non-zero must be in [1, 65535].
    const sshPort = node.ssh_port ?? 0;
    if (sshPort !== 0 && (sshPort < 1 || sshPort > 65535)) {
      addError(result, prefix + '.ssh_port', Code.NodeSSHPortOutOfRange, { k: 'port', v: String(sshPort) });
    }

    // RouterID (schema.go:345-353): empty → auto-generated (skipped). When set must be MAC-48 OR IPv4.
    const routerID = node.router_id ?? '';
    if (routerID !== '') {
      const ipFam = parseIPFamily(routerID);
      const isIPv4 = ipFam !== null && ipFam.isIPv4;
      if (!routerIDMAC48.test(routerID) && !isIPv4) {
        addError(result, prefix + '.router_id', Code.NodeRouterIDInvalid, { k: 'id', v: goQuote(routerID) });
      }
    }

    // ExtraPrefixes (schema.go:355-366): each must be a parseable IPv4 CIDR.
    const extraPrefixes = node.extra_prefixes ?? [];
    for (let j = 0; j < extraPrefixes.length; j++) {
      const prefixCIDR = extraPrefixes[j];
      const epPrefix = `${prefix}.extra_prefixes[${j}]`;
      const info = parseCIDRFamily(prefixCIDR);
      if (info === null) {
        addError(result, epPrefix, Code.NodeExtraPrefixInvalid, { k: 'prefix', v: prefixCIDR });
      } else if (!info.isIPv4) {
        addError(result, epPrefix, Code.NodeExtraPrefixNotIPv4, { k: 'prefix', v: prefixCIDR });
      }
    }

    // SSH field charset validation (schema.go:368-387).
    const sshHost = node.ssh_host ?? '';
    if (sshHost !== '' && !sshFieldCharset.test(sshHost)) {
      addError(result, prefix + '.ssh_host', Code.NodeSSHHostIllegalChars, { k: 'host', v: goQuote(sshHost) });
    }
    const sshAlias = node.ssh_alias ?? '';
    if (sshAlias !== '' && !sshFieldCharset.test(sshAlias)) {
      addError(result, prefix + '.ssh_alias', Code.NodeSSHAliasIllegalChars, { k: 'alias', v: goQuote(sshAlias) });
    }
    const sshUser = node.ssh_user ?? '';
    if (sshUser !== '' && !sshFieldCharset.test(sshUser)) {
      addError(result, prefix + '.ssh_user', Code.NodeSSHUserIllegalChars, { k: 'user', v: goQuote(sshUser) });
    }
    const sshKeyPath = node.ssh_key_path ?? '';
    if (sshKeyPath !== '' && !sshKeyPathCharset.test(sshKeyPath)) {
      addError(result, prefix + '.ssh_key_path', Code.NodeSSHKeyPathIllegalChars, { k: 'path', v: goQuote(sshKeyPath) });
    }

    // public_endpoints[].host charset validation (schema.go:389-400).
    const publicEndpoints = node.public_endpoints ?? [];
    for (let k = 0; k < publicEndpoints.length; k++) {
      const ep = publicEndpoints[k];
      if (ep.host !== '' && !endpointHostCharset.test(ep.host)) {
        addError(result, `${prefix}.public_endpoints[${k}].host`, Code.NodePublicEndpointHostIllegalChars, { k: 'host', v: goQuote(ep.host) });
      }
    }
  }
}

// validateEdgesSchema mirrors schema.go:404-469. It NORMALIZES edge.transport in place (empty → "udp",
// schema.go:433-435) before the enum check, so the mutation round-trips.
function validateEdgesSchema(topo: Topology, result: ValidationResult): void {
  for (let i = 0; i < topo.edges.length; i++) {
    // Mutate the actual edge object so the transport normalization persists (round-trip).
    const edge = topo.edges[i];
    const prefix = `edges[${i}]`;

    if (edge.id === '') {
      addError(result, prefix + '.id', Code.EdgeIDRequired);
    }
    if (edge.from_node_id === '') {
      addError(result, prefix + '.from_node_id', Code.EdgeFromNodeIDRequired);
    }
    if (edge.to_node_id === '') {
      addError(result, prefix + '.to_node_id', Code.EdgeToNodeIDRequired);
    }

    // Type (schema.go:421-427).
    const type = edge.type as string;
    if (type === '') {
      addError(result, prefix + '.type', Code.EdgeTypeEmpty);
    } else if (type !== 'direct' && type !== 'public-endpoint' && type !== 'relay-path' && type !== 'candidate') {
      addError(result, prefix + '.type', Code.EdgeTypeInvalid, { k: 'type', v: type });
    }

    // Transport normalization + validation (schema.go:429-442). Normalize empty → "udp" and write it
    // back, THEN run the enum check. The wire value is a plain string (empty is the un-set case), read
    // as such — the frozen TS literal-union is narrower than the deserialized reality.
    if (edge.transport === undefined || (edge.transport as string) === '') {
      edge.transport = 'udp';
    }
    const transport = edge.transport as string;
    if (transport !== 'udp' && transport !== 'tcp') {
      addError(result, prefix + '.transport', Code.EdgeTransportInvalid, { k: 'transport', v: transport });
    }

    // EndpointPort (schema.go:444-447).
    const endpointPort = edge.endpoint_port ?? 0;
    if (endpointPort < 0 || endpointPort > 65535) {
      addError(result, prefix + '.endpoint_port', Code.EdgeEndpointPortInvalid, { k: 'port', v: String(endpointPort) });
    }

    // endpoint_host charset (schema.go:449-454).
    const endpointHost = edge.endpoint_host ?? '';
    if (endpointHost !== '' && !endpointHostCharset.test(endpointHost)) {
      addError(result, prefix + '.endpoint_host', Code.EdgeEndpointHostIllegalChars, { k: 'host', v: goQuote(endpointHost) });
    }

    // Role validation (schema.go:456-462): only empty, "primary", "backup" are allowed.
    const edgeRole = (edge.role ?? '') as string;
    if (edgeRole !== '' && edgeRole !== edgeRolePrimary && edgeRole !== edgeRoleBackup) {
      addError(result, prefix + '.role', Code.EdgeRoleInvalid, { k: 'role', v: edgeRole });
    }

    // Self-loop (schema.go:464-467).
    if (edge.from_node_id !== '' && edge.from_node_id === edge.to_node_id) {
      addError(result, prefix, Code.EdgeSelfLoop);
    }
  }
}

// ========================================================================================
// SEMANTIC PASS — mirror of internal/validator/semantic.go + nat.go (ValidateSemantic, ~20
// sub-validators). Cross-reference integrity, IP collisions, name collisions across 3 normalized forms,
// effective-port-range overflow, isolated nodes, NAT reachability, client-edge rules, mimic platform,
// endpoint consistency, duplicate-direction warnings, interface-name uniqueness, single-primary,
// backup-touches-client, parallel-link equal-cost / no-primary warnings, allocation-pin validation, and
// route_policies-reserved. Emits the exact validator.Code literals; shares the Phase-1 leaves so grouping
// + dedup + cost math are byte-identical to Go.
// ========================================================================================

// roleOf reads a node's role as a plain string (the wire value, possibly out of the TS literal-union).
function roleOf(n: Node | undefined): string {
  return n ? (n.role as string) : '';
}

// validateSemantic runs the semantic validation pass (Pass 2). Mirrors validator.ValidateSemantic
// (semantic.go:17-90), including the DoS short-circuit BEFORE the O(n²) collision/NAT passes (the schema
// pass is the canonical reporter of the root bounds; this guard only protects the expensive work).
export function validateSemantic(topo: Topology): ValidationResult {
  const result: ValidationResult = { errors: [], warnings: [] };

  // DoS / forward-compat guard (semantic.go:25-27): short-circuit (return clean) before the expensive
  // passes when the topology is oversized or future-format. Reuses the same predicate as the schema pass.
  if (topologyExceedsBounds(topo)) {
    return result;
  }

  // Build lookup maps (semantic.go:30-31).
  const domainMap = buildDomainMap(topo);
  const nodeMap = buildNodeMap(topo);

  validateNodeDomainRefs(topo, domainMap, result);
  validateEdgeNodeRefs(topo, nodeMap, result);
  validateIPSemantics(topo, domainMap, result);
  validateIDUniqueness(topo, result);
  validateNodeNameCollisions(topo, result);
  validateEffectivePortRanges(topo, result);
  detectIsolatedNodes(topo, result);
  validateNATReachability(topo, nodeMap, result);
  validateClientEdges(topo, nodeMap, result);
  validateMimicTransport(topo, nodeMap, result);
  validateEdgeEndpointConsistency(topo, nodeMap, result);
  detectDuplicateEnabledEdges(topo, result);
  validateInterfaceNameUniqueness(topo, nodeMap, result);
  validateSinglePrimaryPerPair(topo, nodeMap, result);
  validateBackupClientEdges(topo, nodeMap, result);
  validateParallelLinkCosts(topo, nodeMap, result);
  validateAllocationPins(topo, domainMap, nodeMap, result);
  validateRoutePoliciesReserved(topo, result);

  return result;
}

// buildDomainMap / buildNodeMap mirror semantic.go:107-121 (ID → entity).
function buildDomainMap(topo: Topology): Map<string, Domain> {
  const m = new Map<string, Domain>();
  for (const d of topo.domains) {
    m.set(d.id, d);
  }
  return m;
}

function buildNodeMap(topo: Topology): Map<string, Node> {
  const m = new Map<string, Node>();
  for (const n of topo.nodes) {
    m.set(n.id, n);
  }
  return m;
}

// validateNodeDomainRefs mirrors semantic.go:123-131. Note: Go's map lookup of a key the entity DID set
// vs DID-NOT differs from "" — a node with domain_id "" skips the check; a non-empty domain_id absent
// from the map errors.
function validateNodeDomainRefs(topo: Topology, domainMap: Map<string, Domain>, result: ValidationResult): void {
  for (let i = 0; i < topo.nodes.length; i++) {
    const node = topo.nodes[i];
    if (node.domain_id !== '') {
      if (!domainMap.has(node.domain_id)) {
        addError(result, `nodes[${i}].domain_id`, Code.NodeDomainRefMissing, { k: 'node', v: node.name }, { k: 'id', v: node.domain_id });
      }
    }
  }
}

// validateEdgeNodeRefs mirrors semantic.go:133-147.
function validateEdgeNodeRefs(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    const prefix = `edges[${i}]`;
    if (edge.from_node_id !== '') {
      if (!nodeMap.has(edge.from_node_id)) {
        addError(result, prefix + '.from_node_id', Code.EdgeNodeRefMissing, { k: 'id', v: edge.id }, { k: 'other', v: edge.from_node_id });
      }
    }
    if (edge.to_node_id !== '') {
      if (!nodeMap.has(edge.to_node_id)) {
        addError(result, prefix + '.to_node_id', Code.EdgeNodeRefMissing, { k: 'id', v: edge.id }, { k: 'other', v: edge.to_node_id });
      }
    }
  }
}

// validateIPSemantics mirrors semantic.go:149-180: overlay IP within its domain CIDR + duplicate overlay
// IP detection. A malformed overlay IP is reported by the schema pass and skipped here.
function validateIPSemantics(topo: Topology, domainMap: Map<string, Domain>, result: ValidationResult): void {
  const ipUsage = new Map<string, string>(); // overlay IP -> first node name

  for (let i = 0; i < topo.nodes.length; i++) {
    const node = topo.nodes[i];
    const overlayIP = node.overlay_ip ?? '';
    if (overlayIP === '') {
      continue;
    }
    const prefix = `nodes[${i}].overlay_ip`;

    // net.ParseIP(node.OverlayIP) == nil → skip (schema pass owns malformed IPs). Both families are
    // accepted by ParseIP, so parseIPFamily(null) is the skip predicate.
    if (parseIPFamily(overlayIP) === null) {
      continue;
    }

    // Overlay IP within the domain CIDR (semantic.go:165-171). Go uses net.ParseCIDR + cidrNet.Contains,
    // which accept BOTH families; the overlay-out-of-CIDR check fires only when ParseCIDR succeeds. The
    // domain CIDR is IPv4-validated upstream, and contains() is the IPv4 membership test; an IPv6 overlay
    // IP would not be Contains'd by an IPv4 net, mirroring Go (a family mismatch is "not within").
    const domain = domainMap.get(node.domain_id);
    if (domain && domain.cidr !== '') {
      const info = parseCIDR(domain.cidr);
      if (info !== null && !contains(info, overlayIP)) {
        addError(result, prefix, Code.NodeOverlayIPOutOfCIDR, { k: 'node', v: node.name }, { k: 'cidr', v: overlayIP }, { k: 'name', v: domain.name }, { k: 'prefix', v: domain.cidr });
      }
    }

    // Duplicate overlay IP (semantic.go:173-178). Keyed by the RAW string (Go keys ipUsage by the raw
    // OverlayIP, not a canonical form).
    const existing = ipUsage.get(overlayIP);
    if (existing !== undefined) {
      addError(result, prefix, Code.NodeOverlayIPConflict, { k: 'cidr', v: overlayIP }, { k: 'other', v: existing }, { k: 'node', v: node.name });
    } else {
      ipUsage.set(overlayIP, node.name);
    }
  }
}

// validateIDUniqueness mirrors semantic.go:182-209: duplicate domain / node / edge IDs.
function validateIDUniqueness(topo: Topology, result: ValidationResult): void {
  const domainIDs = new Set<string>();
  for (let i = 0; i < topo.domains.length; i++) {
    const d = topo.domains[i];
    if (domainIDs.has(d.id)) {
      addError(result, `domains[${i}].id`, Code.DomainIDDuplicate, { k: 'id', v: d.id });
    }
    domainIDs.add(d.id);
  }

  const nodeIDs = new Set<string>();
  for (let i = 0; i < topo.nodes.length; i++) {
    const n = topo.nodes[i];
    if (nodeIDs.has(n.id)) {
      addError(result, `nodes[${i}].id`, Code.NodeIDDuplicate, { k: 'id', v: n.id });
    }
    nodeIDs.add(n.id);
  }

  const edgeIDs = new Set<string>();
  for (let i = 0; i < topo.edges.length; i++) {
    const e = topo.edges[i];
    if (edgeIDs.has(e.id)) {
      addError(result, `edges[${i}].id`, Code.EdgeIDDuplicate, { k: 'id', v: e.id });
    }
    edgeIDs.add(e.id);
  }
}

// validateNodeNameCollisions mirrors semantic.go:223-263: collisions across three normalized forms (raw
// name, installer filename, WireGuard interface name). The installer/interface checks ONLY error when the
// first occupant's raw name DIFFERS (a raw-name dup is already reported by the N1 check).
function validateNodeNameCollisions(topo: Topology, result: ValidationResult): void {
  const rawNames = new Map<string, string>(); // raw name -> first node name
  const installerNames = new Map<string, string>(); // installer filename -> first node name
  const interfaceNames = new Map<string, string>(); // interface name -> first node name

  for (let i = 0; i < topo.nodes.length; i++) {
    const node = topo.nodes[i];
    if (node.name === '') {
      continue; // schema pass owns empty names
    }
    const prefix = `nodes[${i}].name`;

    // N1: raw-name collision.
    const firstRaw = rawNames.get(node.name);
    if (firstRaw !== undefined) {
      addError(result, prefix, Code.NodeNameDuplicate, { k: 'other', v: firstRaw }, { k: 'node', v: node.name }, { k: 'name', v: goQuote(node.name) });
    } else {
      rawNames.set(node.name, node.name);
    }

    // N2: installer-filename collision.
    const installerName = safeInstallerFileName(node.name);
    const firstInstaller = installerNames.get(installerName);
    if (firstInstaller !== undefined) {
      if (firstInstaller !== node.name) {
        addError(result, prefix, Code.NodeNameInstallerCollision, { k: 'other', v: firstInstaller }, { k: 'node', v: node.name }, { k: 'name', v: goQuote(installerName) });
      }
    } else {
      installerNames.set(installerName, node.name);
    }

    // N3: WireGuard interface-name collision.
    const interfaceName = wgInterfaceName(node.name);
    const firstIface = interfaceNames.get(interfaceName);
    if (firstIface !== undefined) {
      if (firstIface !== node.name) {
        addError(result, prefix, Code.NodeNameInterfaceCollision, { k: 'other', v: firstIface }, { k: 'node', v: node.name }, { k: 'name', v: goQuote(interfaceName) });
      }
    } else {
      interfaceNames.set(interfaceName, node.name);
    }
  }
}

// defaultListenPort — the single base port for per-peer interface allocation (semantic.go:268).
const defaultListenPort = 51820;

// validateEffectivePortRanges mirrors semantic.go:312-378. Deduplicate enabled edges by linkKey, count
// interfaces per non-client endpoint, then error when base+count-1 > 65535.
function validateEffectivePortRanges(topo: Topology, result: ValidationResult): void {
  const nodeMap = new Map<string, Node>();
  const nodeIndex = new Map<string, number>();
  for (let i = 0; i < topo.nodes.length; i++) {
    nodeMap.set(topo.nodes[i].id, topo.nodes[i]);
    nodeIndex.set(topo.nodes[i].id, i);
  }

  const seenLinks = new Set<string>();
  const interfaceCount = new Map<string, number>(); // nodeID -> interface count

  for (const edge of topo.edges) {
    if (!edge.is_enabled) {
      continue;
    }
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);
    if (!fromNode || !toNode) {
      continue;
    }
    const lk = linkKey(edge);
    if (seenLinks.has(lk)) {
      continue;
    }
    seenLinks.add(lk);

    if (roleOf(fromNode) !== 'client') {
      interfaceCount.set(fromNode.id, (interfaceCount.get(fromNode.id) ?? 0) + 1);
    }
    if (roleOf(toNode) !== 'client') {
      interfaceCount.set(toNode.id, (interfaceCount.get(toNode.id) ?? 0) + 1);
    }
  }

  for (const node of topo.nodes) {
    const count = interfaceCount.get(node.id) ?? 0;
    if (count === 0) {
      continue;
    }
    const base = defaultListenPort;
    const high = base + count - 1;
    if (high > 65535) {
      const idx = nodeIndex.get(node.id) ?? 0;
      addError(result, `nodes[${idx}]`, Code.NodeEffectivePortRangeOverflow,
        { k: 'node', v: node.name }, { k: 'low', v: String(base) }, { k: 'high', v: String(high) }, { k: 'base', v: String(base) }, { k: 'count', v: String(count) });
    }
  }
}

// detectIsolatedNodes mirrors semantic.go:380-400: warn for any node (when >1 nodes) with no enabled edge.
function detectIsolatedNodes(topo: Topology, result: ValidationResult): void {
  if (topo.nodes.length <= 1) {
    return;
  }
  const connectedNodes = new Set<string>();
  for (const edge of topo.edges) {
    if (edge.is_enabled) {
      connectedNodes.add(edge.from_node_id);
      connectedNodes.add(edge.to_node_id);
    }
  }
  for (const node of topo.nodes) {
    if (!connectedNodes.has(node.id)) {
      addWarning(result, 'topology', Code.NodeIsolated, { k: 'node', v: node.name }, { k: 'id', v: node.id });
    }
  }
}

// --- NAT reachability (internal/validator/nat.go) ---

// capHasPublicIP / capCanAcceptInbound read a node's RAW declared capability flags. The validator runs
// BEFORE InferCapabilitiesFromRole (capability inference is a later compile pass), so it sees only the
// explicit wire flags. An absent capabilities object / unset flag is falsy (== Go's zero-value false).
function capHasPublicIP(node: Node): boolean {
  return node.capabilities?.has_public_ip === true;
}

function capCanAcceptInbound(node: Node): boolean {
  return node.capabilities?.can_accept_inbound === true;
}

// canBeDialed mirrors nat.go:18-20: dialable without an endpoint when it has a public IP, accepts
// inbound, or is a relay (a relay is guaranteed CanAcceptInbound after inference).
function canBeDialed(node: Node): boolean {
  return capHasPublicIP(node) || capCanAcceptInbound(node) || roleOf(node) === 'relay';
}

// hasEnabledEndpointEdge mirrors nat.go:26-36: an enabled from->to edge carrying endpoint_host exists.
function hasEnabledEndpointEdge(topo: Topology, fromID: string, toID: string): boolean {
  for (const edge of topo.edges) {
    if (!edge.is_enabled) {
      continue;
    }
    if (edge.from_node_id === fromID && edge.to_node_id === toID && (edge.endpoint_host ?? '') !== '') {
      return true;
    }
  }
  return false;
}

// validateNATReachability mirrors nat.go:42-109.
function validateNATReachability(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled) {
      continue;
    }
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);
    if (!fromNode || !toNode) {
      continue;
    }
    const prefix = `edges[${i}]`;

    // Target unreachable (nat.go:57-59): target has no public IP, no inbound, not a relay.
    if (!capHasPublicIP(toNode) && !capCanAcceptInbound(toNode) && roleOf(toNode) !== 'relay') {
      addWarning(result, prefix, Code.NATTargetUnreachable, { k: 'edge', v: edge.id }, { k: 'node', v: toNode.name });
    }

    // Double-ended NAT (nat.go:62-82).
    if (!capHasPublicIP(fromNode) && !capHasPublicIP(toNode)) {
      if (edge.type === 'direct' && (edge.endpoint_host ?? '') === '') {
        const reverseHasEndpoint = hasEnabledEndpointEdge(topo, toNode.id, fromNode.id);
        const neitherCanBeDialed = !canBeDialed(fromNode) && !canBeDialed(toNode);
        if (!reverseHasEndpoint && neitherCanBeDialed) {
          addError(result, prefix, Code.NATDeadLink, { k: 'edge', v: edge.id }, { k: 'from', v: fromNode.name }, { k: 'to', v: toNode.name });
        } else {
          addWarning(result, prefix, Code.NATDoubleNATNoEndpoint, { k: 'edge', v: edge.id }, { k: 'from', v: fromNode.name }, { k: 'to', v: toNode.name });
        }
      }
    }
  }

  // Per-node: a NAT'd node (no public IP, no inbound) needs an outbound edge to a publicly reachable peer
  // (nat.go:88-108).
  for (const node of topo.nodes) {
    if (capHasPublicIP(node) || capCanAcceptInbound(node)) {
      continue;
    }
    let hasOutboundToPublic = false;
    for (const edge of topo.edges) {
      if (!edge.is_enabled || edge.from_node_id !== node.id) {
        continue;
      }
      const target = nodeMap.get(edge.to_node_id);
      if (target && (capHasPublicIP(target) || capCanAcceptInbound(target) || roleOf(target) === 'relay')) {
        hasOutboundToPublic = true;
        break;
      }
    }
    if (!hasOutboundToPublic && topo.edges.length > 0) {
      addWarning(result, 'nat_reachability', Code.NATNoOutboundToPublic, { k: 'name', v: node.name }, { k: 'id', v: node.id });
    }
  }
}

// validateClientEdges mirrors semantic.go:403-463.
function validateClientEdges(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  const clientOutbound = new Map<string, number>(); // nodeID -> count of enabled outbound edges

  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled) {
      continue;
    }
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);

    // Reject inbound edges targeting a client.
    if (toNode && roleOf(toNode) === 'client') {
      addError(result, `edges[${i}]`, Code.ClientInboundRejected, { k: 'node', v: toNode.name });
    }

    if (fromNode && roleOf(fromNode) === 'client') {
      clientOutbound.set(fromNode.id, (clientOutbound.get(fromNode.id) ?? 0) + 1);

      if (toNode) {
        if (roleOf(toNode) === 'peer') {
          addError(result, `edges[${i}]`, Code.ClientTargetPeer, { k: 'node', v: fromNode.name }, { k: 'other', v: toNode.name });
        }
        if (roleOf(toNode) === 'client') {
          addError(result, `edges[${i}]`, Code.ClientTargetClient, { k: 'node', v: fromNode.name }, { k: 'other', v: toNode.name });
        }
      }

      if ((edge.endpoint_host ?? '') === '') {
        addError(result, `edges[${i}].endpoint_host`, Code.ClientEndpointHostRequired, { k: 'node', v: fromNode.name });
      }
    }
  }

  for (const node of topo.nodes) {
    if (roleOf(node) !== 'client') {
      continue;
    }
    const count = clientOutbound.get(node.id) ?? 0;
    if (count === 0) {
      addError(result, 'topology', Code.ClientNoOutboundEdge, { k: 'node', v: node.name });
    } else if (count > 1) {
      addError(result, 'topology', Code.ClientMultipleOutboundEdges, { k: 'node', v: node.name }, { k: 'count', v: String(count) });
    }

    if ((node.router_id ?? '') !== '') {
      addWarning(result, `node.${node.id}.router_id`, Code.ClientRouterIDMeaningless, { k: 'node', v: node.name });
    }
    if ((node.extra_prefixes ?? []).length > 0) {
      addWarning(result, `node.${node.id}.extra_prefixes`, Code.ClientExtraPrefixesMeaningless, { k: 'node', v: node.name });
    }
  }
}

// mimicLinuxDeployable mirrors semantic.go:470-484: nil / empty platform → allowed; only debian/ubuntu
// (case-insensitive) are deployable Linux.
function mimicLinuxDeployable(node: Node | undefined): boolean {
  if (!node) {
    return true; // missing node reported elsewhere
  }
  const platform = (node.platform ?? '') as string;
  if (platform === '') {
    return true;
  }
  const lowered = platform.toLowerCase();
  return lowered === 'debian' || lowered === 'ubuntu';
}

// validateMimicTransport mirrors semantic.go:498-518: both ends of a tcp edge must be deployable Linux.
function validateMimicTransport(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled) {
      continue;
    }
    if (edge.transport !== 'tcp') {
      continue;
    }
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);

    if (!mimicLinuxDeployable(fromNode)) {
      addError(result, `edges[${i}].transport`, Code.EdgeMimicPlatformUnsupported, { k: 'id', v: edge.id }, { k: 'node', v: fromNode ? fromNode.name : '' }, { k: 'platform', v: goQuote((fromNode?.platform ?? '') as string) });
    }
    if (!mimicLinuxDeployable(toNode)) {
      addError(result, `edges[${i}].transport`, Code.EdgeMimicPlatformUnsupported, { k: 'id', v: edge.id }, { k: 'node', v: toNode ? toNode.name : '' }, { k: 'platform', v: goQuote((toNode?.platform ?? '') as string) });
    }
  }
}

// edgeTransitCIDR mirrors semantic.go:523-532: the from node's domain transit_cidr, falling back to the
// default 10.10.0.0/24.
function edgeTransitCIDR(edge: Edge, domainMap: Map<string, Domain>, nodeMap: Map<string, Node>): string {
  const fromNode = nodeMap.get(edge.from_node_id);
  if (!fromNode) {
    return DefaultTransitCIDR;
  }
  const domain = domainMap.get(fromNode.domain_id);
  if (domain && (domain.transit_cidr ?? '') !== '') {
    return domain.transit_cidr as string;
  }
  return DefaultTransitCIDR;
}

// nodePortPin / pinOwner mirror semantic.go:545-557.
interface NodePortPin {
  port: number;
  linkID: string;
  edge: string;
}
interface PinOwner {
  linkID: string;
  edge: string;
}

// validateAllocationPins mirrors semantic.go:573-645.
function validateAllocationPins(topo: Topology, domainMap: Map<string, Domain>, nodeMap: Map<string, Node>, result: ValidationResult): void {
  const portsByNode = new Map<string, NodePortPin[]>();
  const transitByValue = new Map<string, PinOwner>();
  const linkLocalByValue = new Map<string, PinOwner>();

  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled) {
      continue;
    }
    const prefix = `edges[${i}]`;
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);
    if (!fromNode || !toNode) {
      continue;
    }

    const link = linkKey(edge);

    const pinnedFromPort = edge.pinned_from_port ?? 0;
    const pinnedToPort = edge.pinned_to_port ?? 0;
    const pinnedFromTransit = edge.pinned_from_transit_ip ?? '';
    const pinnedToTransit = edge.pinned_to_transit_ip ?? '';
    const pinnedFromLL = edge.pinned_from_link_local ?? '';
    const pinnedToLL = edge.pinned_to_link_local ?? '';

    // Client edge carries pins (semantic.go:609-619).
    const clientTouched = roleOf(fromNode) === 'client' || roleOf(toNode) === 'client';
    if (clientTouched) {
      if (pinnedFromPort !== 0 || pinnedToPort !== 0) {
        addError(result, prefix, Code.PinClientPortPin, { k: 'id', v: edge.id });
      }
      if (pinnedFromTransit !== '' || pinnedToTransit !== '' || pinnedFromLL !== '' || pinnedToLL !== '') {
        addWarning(result, prefix, Code.PinClientAllocationIgnored, { k: 'id', v: edge.id });
      }
      continue;
    }

    // Partial pin (semantic.go:622, 650-660).
    if ((pinnedFromPort !== 0) !== (pinnedToPort !== 0)) {
      addError(result, prefix, Code.PinPortIncomplete, { k: 'id', v: edge.id });
    }
    if ((pinnedFromTransit !== '') !== (pinnedToTransit !== '')) {
      addError(result, prefix, Code.PinTransitIPIncomplete, { k: 'id', v: edge.id });
    }
    if ((pinnedFromLL !== '') !== (pinnedToLL !== '')) {
      addError(result, prefix, Code.PinLinkLocalIncomplete, { k: 'id', v: edge.id });
    }

    // Port out of range (semantic.go:626-627, 665-672).
    validatePinnedPortRange(prefix, 'pinned_from_port', pinnedFromPort, fromNode, result);
    validatePinnedPortRange(prefix, 'pinned_to_port', pinnedToPort, toNode, result);

    // Transit IP out of pool (semantic.go:630-632, 677-695).
    const transitCIDR = edgeTransitCIDR(edge, domainMap, nodeMap);
    validatePinnedTransitInCIDR(prefix, 'pinned_from_transit_ip', pinnedFromTransit, transitCIDR, result);
    validatePinnedTransitInCIDR(prefix, 'pinned_to_transit_ip', pinnedToTransit, transitCIDR, result);

    // Cross-link dedup (semantic.go:634-643).
    checkDuplicatePortOnNode(prefix, edge.from_node_id, pinnedFromPort, link, edge.id, portsByNode, result);
    checkDuplicatePortOnNode(prefix, edge.to_node_id, pinnedToPort, link, edge.id, portsByNode, result);

    checkDuplicateTransitIP(prefix, pinnedFromTransit, link, edge.id, transitByValue, result);
    checkDuplicateTransitIP(prefix, pinnedToTransit, link, edge.id, transitByValue, result);
    checkDuplicateLinkLocal(prefix, pinnedFromLL, link, edge.id, linkLocalByValue, result);
    checkDuplicateLinkLocal(prefix, pinnedToLL, link, edge.id, linkLocalByValue, result);
  }
}

// validatePinnedPortRange mirrors semantic.go:665-672: >= MinPinnedPort (1024) and <= 65535; 0 = unpinned.
function validatePinnedPortRange(prefix: string, field: string, port: number, node: Node, result: ValidationResult): void {
  if (port === 0) {
    return;
  }
  if (port < MinPinnedPort || port > 65535) {
    addError(result, prefix + '.' + field, Code.PinPortOutOfRange, { k: 'node', v: node.name }, { k: 'port', v: String(port) }, { k: 'base', v: String(MinPinnedPort) });
  }
}

// validatePinnedTransitInCIDR mirrors semantic.go:677-695: unparseable → invalid; parseable but out of
// the resolved transit pool → out-of-CIDR. An unparseable transit CIDR itself is reported elsewhere.
function validatePinnedTransitInCIDR(prefix: string, field: string, value: string, transitCIDR: string, result: ValidationResult): void {
  if (value === '') {
    return;
  }
  if (parseIPFamily(value) === null) {
    addError(result, prefix + '.' + field, Code.PinTransitIPInvalid, { k: 'cidr', v: goQuote(value) });
    return;
  }
  const info = parseCIDR(transitCIDR);
  if (info === null) {
    return; // illegal transit CIDR reported elsewhere
  }
  if (!contains(info, value)) {
    addError(result, prefix + '.' + field, Code.PinTransitIPOutOfCIDR, { k: 'cidr', v: value }, { k: 'prefix', v: transitCIDR });
  }
}

// checkDuplicatePortOnNode mirrors semantic.go:700-716.
function checkDuplicatePortOnNode(prefix: string, nodeID: string, port: number, link: string, edgeID: string, portsByNode: Map<string, NodePortPin[]>, result: ValidationResult): void {
  if (port === 0) {
    return;
  }
  const existing = portsByNode.get(nodeID) ?? [];
  for (const e of existing) {
    if (e.port !== port) {
      continue;
    }
    if (e.linkID === link) {
      return; // same link (forward/reverse)
    }
    addError(result, prefix, Code.PinPortDuplicateCrossLink, { k: 'port', v: String(port) }, { k: 'other', v: e.edge }, { k: 'id', v: edgeID });
    return;
  }
  existing.push({ port, linkID: link, edge: edgeID });
  portsByNode.set(nodeID, existing);
}

// checkDuplicateTransitIP mirrors semantic.go:722-735 (canonicalized address comparison).
function checkDuplicateTransitIP(prefix: string, value: string, link: string, edgeID: string, transitByValue: Map<string, PinOwner>, result: ValidationResult): void {
  if (value === '') {
    return;
  }
  const key = canonicalIP(value);
  const owner = transitByValue.get(key);
  if (owner !== undefined) {
    if (owner.linkID === link) {
      return;
    }
    addError(result, prefix, Code.PinTransitIPDuplicateCrossLink, { k: 'cidr', v: value }, { k: 'other', v: owner.edge }, { k: 'id', v: edgeID });
    return;
  }
  transitByValue.set(key, { linkID: link, edge: edgeID });
}

// checkDuplicateLinkLocal mirrors semantic.go:740-753.
function checkDuplicateLinkLocal(prefix: string, value: string, link: string, edgeID: string, linkLocalByValue: Map<string, PinOwner>, result: ValidationResult): void {
  if (value === '') {
    return;
  }
  const key = canonicalIP(value);
  const owner = linkLocalByValue.get(key);
  if (owner !== undefined) {
    if (owner.linkID === link) {
      return;
    }
    addError(result, prefix, Code.PinLinkLocalDuplicateCrossLink, { k: 'cidr', v: value }, { k: 'other', v: owner.edge }, { k: 'id', v: edgeID });
    return;
  }
  linkLocalByValue.set(key, { linkID: link, edge: edgeID });
}

// validateEdgeEndpointConsistency mirrors semantic.go:772-795: warn when an enabled edge's endpoint_host
// matches none of the target node's public_endpoints[].host (and the target declares any).
function validateEdgeEndpointConsistency(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled || (edge.endpoint_host ?? '') === '') {
      continue;
    }
    const toNode = nodeMap.get(edge.to_node_id);
    const publicEndpoints = toNode?.public_endpoints ?? [];
    if (!toNode || publicEndpoints.length === 0) {
      continue;
    }
    let matched = false;
    for (const ep of publicEndpoints) {
      if (ep.host === edge.endpoint_host) {
        matched = true;
        break;
      }
    }
    if (!matched) {
      addWarning(result, `edges[${i}].endpoint_host`, Code.EdgeEndpointNoMatch, { k: 'id', v: edge.id }, { k: 'other', v: edge.endpoint_host as string }, { k: 'node', v: toNode.name });
    }
  }
}

// detectDuplicateEnabledEdges mirrors semantic.go:807-825: same direction, primary class, second+ edge
// warns. Backup edges are independent links and skip.
function detectDuplicateEnabledEdges(topo: Topology, result: ValidationResult): void {
  const firstEdgeByDirection = new Map<string, string>();
  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled) {
      continue;
    }
    if (isBackup(edge)) {
      continue;
    }
    const direction = edge.from_node_id + '->' + edge.to_node_id;
    const firstID = firstEdgeByDirection.get(direction);
    if (firstID !== undefined) {
      addWarning(result, `edges[${i}]`, Code.EdgeDuplicateEnabledSameDirection, { k: 'id', v: edge.id }, { k: 'other', v: firstID });
      continue;
    }
    firstEdgeByDirection.set(direction, edge.id);
  }
}

// babeldWiredDefaultCost (semantic.go:832): babeld's built-in default rxcost for wired/tunnel interfaces.
const babeldWiredDefaultCost = 96;

// effectiveLinkCost mirrors semantic.go:845-859: priority > weight > backup-default(384) > 0.
function effectiveLinkCost(rep: Edge | undefined): number {
  if (!rep) {
    return 0;
  }
  if ((rep.priority ?? 0) > 0) {
    return rep.priority as number;
  }
  if ((rep.weight ?? 0) > 0) {
    return rep.weight as number;
  }
  if (isBackup(rep)) {
    return BackupDefaultLinkCost;
  }
  return 0;
}

// comparableCost mirrors semantic.go:863-868: 0 (unset) → babeld's built-in default 96.
function comparableCost(cost: number): number {
  return cost === 0 ? babeldWiredDefaultCost : cost;
}

// linkDescription mirrors semantic.go:968-973: a language-neutral locator for a colliding link.
function linkDescription(edge: Edge, remoteName: string, backup: boolean): string {
  if (backup) {
    return `backup→${remoteName} (${edge.id})`;
  }
  return `primary→${remoteName}`;
}

// validateInterfaceNameUniqueness mirrors semantic.go:886-957 (invariant N4).
function validateInterfaceNameUniqueness(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  const ifaceByNode = new Map<string, Map<string, string>>();

  const register = (nodeIdx: number, nodeID: string, ifaceName: string, linkDesc: string): void => {
    let byName = ifaceByNode.get(nodeID);
    if (!byName) {
      byName = new Map<string, string>();
      ifaceByNode.set(nodeID, byName);
    }
    const first = byName.get(ifaceName);
    if (first !== undefined) {
      const node = nodeMap.get(nodeID);
      const nodeName = node ? node.name : nodeID;
      addError(result, `nodes[${nodeIdx}]`, Code.NodeInterfaceNameCollision, { k: 'node', v: nodeName }, { k: 'name', v: goQuote(ifaceName) }, { k: 'prefix', v: first }, { k: 'other', v: linkDesc });
      return;
    }
    byName.set(ifaceName, linkDesc);
  };

  const nodeIndex = new Map<string, number>();
  for (let i = 0; i < topo.nodes.length; i++) {
    nodeIndex.set(topo.nodes[i].id, i);
  }

  const seenLinks = new Set<string>();
  for (const edge of topo.edges) {
    if (!edge.is_enabled) {
      continue;
    }
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);
    if (!fromNode || !toNode) {
      continue;
    }
    const lk = linkKey(edge);
    if (seenLinks.has(lk)) {
      continue;
    }
    seenLinks.add(lk);

    const backup = isBackup(edge);

    if (roleOf(fromNode) !== 'client') {
      const ifaceName = backup ? wgInterfaceNameForEdge(toNode.name, edge.id, true) : wgInterfaceName(toNode.name);
      register(nodeIndex.get(fromNode.id) ?? 0, fromNode.id, ifaceName, linkDescription(edge, toNode.name, backup));
    }
    if (roleOf(toNode) !== 'client') {
      const ifaceName = backup ? wgInterfaceNameForEdge(fromNode.name, edge.id, true) : wgInterfaceName(fromNode.name);
      register(nodeIndex.get(toNode.id) ?? 0, toNode.id, ifaceName, linkDescription(edge, fromNode.name, backup));
    }
  }
}

// validateSinglePrimaryPerPair mirrors semantic.go:983-1004: at most one EXPLICIT role=="primary" per
// pair (an empty role does NOT count — it is warned by detectDuplicateEnabledEdges).
function validateSinglePrimaryPerPair(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  const firstPrimary = new Map<string, string>();
  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled) {
      continue;
    }
    if ((edge.role ?? '') !== edgeRolePrimary) {
      continue;
    }
    if (!nodeMap.has(edge.from_node_id) || !nodeMap.has(edge.to_node_id)) {
      continue;
    }
    const pk = pinKey(edge.from_node_id, edge.to_node_id);
    const firstID = firstPrimary.get(pk);
    if (firstID !== undefined) {
      addError(result, `edges[${i}].role`, Code.EdgeMultipleExplicitPrimary, { k: 'id', v: edge.id }, { k: 'other', v: firstID });
      continue;
    }
    firstPrimary.set(pk, edge.id);
  }
}

// validateBackupClientEdges mirrors semantic.go:1010-1025: a backup edge must not touch a client node.
function validateBackupClientEdges(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled) {
      continue;
    }
    if (!isBackup(edge)) {
      continue;
    }
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);
    if ((fromNode && roleOf(fromNode) === 'client') || (toNode && roleOf(toNode) === 'client')) {
      addError(result, `edges[${i}].role`, Code.EdgeBackupTouchesClient, { k: 'id', v: edge.id });
    }
  }
}

// pairLinkSummary mirrors semantic.go:1028-1034.
interface PairLinkSummary {
  edgeIndex: number;
  hasPrimary: boolean;
  costs: number[];
  fromName: string;
  toName: string;
}

// validateParallelLinkCosts mirrors semantic.go:1048-1118: equal-cost + no-primary warnings for
// multi-link pairs. Order preserved by first-appearance (a Map preserves insertion order in JS, matching
// the Go `order` slice).
function validateParallelLinkCosts(topo: Topology, nodeMap: Map<string, Node>, result: ValidationResult): void {
  const summaries = new Map<string, PairLinkSummary>();
  const order: string[] = [];
  const primaryCounted = new Set<string>();

  for (let i = 0; i < topo.edges.length; i++) {
    const edge = topo.edges[i];
    if (!edge.is_enabled) {
      continue;
    }
    const fromNode = nodeMap.get(edge.from_node_id);
    const toNode = nodeMap.get(edge.to_node_id);
    if (!fromNode || !toNode) {
      continue;
    }
    const pk = pinKey(edge.from_node_id, edge.to_node_id);
    let s = summaries.get(pk);
    if (!s) {
      s = { edgeIndex: i, hasPrimary: false, costs: [], fromName: fromNode.name, toName: toNode.name };
      summaries.set(pk, s);
      order.push(pk);
    }

    if (isBackup(edge)) {
      s.costs.push(comparableCost(effectiveLinkCost(edge)));
      continue;
    }

    s.hasPrimary = true;
    if (!primaryCounted.has(pk)) {
      primaryCounted.add(pk);
      s.costs.push(comparableCost(effectiveLinkCost(edge)));
    }
  }

  for (const pk of order) {
    const s = summaries.get(pk);
    if (!s) {
      continue;
    }

    if (s.costs.length > 0 && !s.hasPrimary) {
      addWarning(result, `edges[${s.edgeIndex}]`, Code.LinkNoPrimary, { k: 'node', v: s.fromName }, { k: 'other', v: s.toName });
    }

    if (s.costs.length >= 2) {
      let allEqual = true;
      for (let i = 1; i < s.costs.length; i++) {
        if (s.costs[i] !== s.costs[0]) {
          allEqual = false;
          break;
        }
      }
      if (allEqual) {
        addWarning(result, `edges[${s.edgeIndex}]`, Code.LinkEqualCost, { k: 'node', v: s.fromName }, { k: 'other', v: s.toName }, { k: 'count', v: String(s.costs.length) }, { k: 'low', v: String(s.costs[0]) });
      }
    }
  }
}

// validateRoutePoliciesReserved mirrors semantic.go:101-105: route_policies must be empty (reserved).
function validateRoutePoliciesReserved(topo: Topology, result: ValidationResult): void {
  const count = topo.route_policies?.length ?? 0;
  if (count > 0) {
    addError(result, 'route_policies', Code.RoutePolicyReserved, { k: 'count', v: String(count) });
  }
}

// registry maps each schema-pass Code to its English message template, mirroring the subset of
// validator.registry (code.go:128-226) the schema pass emits. The English message is the single source
// of the CLI/curl message AND the i18n English fallback. {name} placeholders map 1:1 to the params
// passed at the call site.
const registry: Record<string, string> = {
  [Code.ProjectIDRequired]: 'Project ID is required.',
  [Code.ProjectNameRequired]: 'Project name is required.',
  [Code.DomainNoneDefined]: 'At least one domain must be defined.',
  [Code.DomainIDRequired]: 'Domain ID is required.',
  [Code.DomainNameRequired]: 'Domain name is required.',
  [Code.DomainCIDREmpty]: 'CIDR must not be empty.',
  [Code.DomainCIDRInvalid]: 'Invalid CIDR format: {cidr}.',
  [Code.DomainCIDRNotIPv4]: 'CIDR must be an IPv4 network: {cidr} (IPv6 and other address families are not supported yet).',
  [Code.DomainCIDRTooLarge]: 'CIDR {cidr} is too large; the prefix length must not be shorter than /8 (it cannot be enumerated for allocation).',
  [Code.DomainTransitCIDRInvalid]: 'Invalid transit_cidr format: {cidr}.',
  [Code.DomainTransitCIDRNotIPv4]: 'transit_cidr must be an IPv4 network: {cidr} (the transit-pair allocator is IPv4-only).',
  [Code.DomainTransitCIDRTooLarge]: 'transit_cidr {cidr} is too large; the prefix length must not be shorter than /8 (it cannot be enumerated for allocation).',
  [Code.DomainTransitCIDRTooSmall]: 'transit_cidr {cidr} is too small; the prefix must be /30 or shorter so it can hold at least one per-link transit IP pair.',
  [Code.DomainAllocationModeInvalid]: 'Invalid allocation mode: {mode}. Allowed values: auto, manual.',
  [Code.DomainRoutingModeUnimplemented]: 'Routing mode {mode} is not implemented yet; only babel is currently supported (the only implemented routing mode).',
  [Code.DomainRoutingModeInvalid]: 'Invalid routing mode: {mode}; only babel is currently supported (the only implemented routing mode).',
  [Code.DomainReservedRangeNotIPv4]: 'Reserved range must be IPv4: {cidr} (IPv6 and other address families are not supported yet).',
  [Code.DomainReservedRangeInvalid]: 'Invalid reserved range format: {value}.',
  [Code.DomainReservedAddressNotIPv4]: 'Reserved address must be IPv4: {ip} (IPv6 and other address families are not supported yet).',
  [Code.NodeIDRequired]: 'Node ID is required.',
  [Code.NodeNameRequired]: 'Node name is required.',
  [Code.NodeNameIllegalChars]: 'Node name {name} contains illegal characters: only letters, digits, spaces, dot (.), underscore (_), and hyphen (-) are allowed; shell metacharacters such as quotes, backticks, $, and ; are forbidden.',
  [Code.NodeDomainIDRequired]: 'Node must reference a Domain.',
  [Code.NodeRoleEmpty]: 'Node role must not be empty.',
  [Code.NodeRoleInvalid]: 'Invalid role: {role}. Allowed values: peer, router, relay, gateway, client.',
  [Code.NodeDeploymentModeInvalid]: 'Invalid deployment_mode: {mode}. Allowed values: managed, manual (or empty for managed).',
  [Code.NodePlatformUnsupported]: 'Unsupported platform: {platform}. Allowed values: debian, ubuntu.',
  [Code.NodeXDPModeInvalid]: 'Invalid XDP mode: {mode}. Allowed values: skb, native (empty is equivalent to skb).',
  [Code.NodeOverlayIPInvalid]: 'Invalid overlay IP address: {ip}.',
  [Code.NodeWGPublicKeyInvalid]: 'wireguard_public_key {key} is not a valid Curve25519 public key: it must be 32 bytes encoded as standard base64 (44 characters). It is written verbatim into the WireGuard configuration deployed on peer nodes, so a malformed value is rejected here.',
  [Code.NodeMTUOutOfRange]: 'MTU {mtu} is out of range: it must be between {low} and {high} (576 is the IPv4 datagram minimum; an out-of-range MTU is rejected by wg-quick).',
  [Code.NodeSSHPortOutOfRange]: 'ssh_port {port} is out of range: it must be between 1 and 65535.',
  [Code.NodeRouterIDInvalid]: 'Invalid router_id format: {id}. It must be in MAC-48 form (six colon-separated hex pairs, e.g. 02:11:22:33:44:55) or an IPv4 address; otherwise babeld will reject it.',
  [Code.NodeExtraPrefixInvalid]: 'Invalid extra route prefix format: {prefix} (it must be in CIDR form, e.g. 192.168.0.0/24).',
  [Code.NodeExtraPrefixNotIPv4]: 'Extra route prefix must be IPv4: {prefix} (IPv6 and other address families are not supported yet).',
  [Code.NodeSSHHostIllegalChars]: 'ssh_host {host} contains illegal characters: only letters, digits, dot (.), underscore (_), colon (:), @, and hyphen (-) are allowed; whitespace and shell metacharacters are forbidden.',
  [Code.NodeSSHAliasIllegalChars]: 'ssh_alias {alias} contains illegal characters: only letters, digits, dot (.), underscore (_), colon (:), @, and hyphen (-) are allowed; whitespace and shell metacharacters are forbidden.',
  [Code.NodeSSHUserIllegalChars]: 'ssh_user {user} contains illegal characters: only letters, digits, dot (.), underscore (_), colon (:), @, and hyphen (-) are allowed; whitespace and shell metacharacters are forbidden.',
  [Code.NodeSSHKeyPathIllegalChars]: 'ssh_key_path {path} contains illegal characters: only letters, digits, and path characters (. _ : @ / \\ ~ space and -) are allowed; shell metacharacters ($ ` " \' ; | & < > ( ) etc.) are forbidden because the path is spliced into the operator\'s deploy shell command.',
  [Code.NodePublicEndpointHostIllegalChars]: 'public_endpoints host {host} contains illegal characters: only letters, digits, dot (.), underscore (_), colon (:), brackets ([ ]), and hyphen (-) are allowed; whitespace and metacharacters are forbidden because the host is written into the WireGuard configuration deployed on the node.',
  [Code.EdgeIDRequired]: 'Edge ID is required.',
  [Code.EdgeFromNodeIDRequired]: 'Edge source node ID is required.',
  [Code.EdgeToNodeIDRequired]: 'Edge target node ID is required.',
  [Code.EdgeTypeEmpty]: 'Edge type must not be empty.',
  [Code.EdgeTypeInvalid]: 'Invalid edge type: {type}. Allowed values: direct, public-endpoint, relay-path, candidate.',
  [Code.EdgeTransportInvalid]: 'Invalid transport protocol: {transport}. Allowed values: udp, tcp.',
  [Code.EdgeEndpointHostIllegalChars]: 'endpoint_host {host} contains illegal characters: only letters, digits, dot (.), underscore (_), colon (:), brackets ([ ]), and hyphen (-) are allowed; whitespace and metacharacters are forbidden because the host is written into the WireGuard configuration deployed on the node.',
  [Code.EdgeEndpointPortInvalid]: 'Invalid endpoint port: {port}.',
  [Code.EdgeRoleInvalid]: 'Invalid link role: {role}. Allowed values: primary, backup (empty is equivalent to primary).',
  [Code.EdgeSelfLoop]: 'Edge source and target nodes must not be the same (self-loop).',
  [Code.TopologyTooManyNodes]: 'Topology has too many nodes: {count} exceeds the maximum of {max}. Split the deployment into separate topologies.',
  [Code.TopologyTooManyEdges]: 'Topology has too many edges: {count} exceeds the maximum of {max}. Split the deployment into separate topologies.',
  [Code.TopologyTooManyDomains]: 'Topology has too many domains: {count} exceeds the maximum of {max}. Split the deployment into separate topologies.',
  [Code.TopologyTooManyReservedRanges]: 'A domain has too many reserved ranges: {count} exceeds the maximum of {max}. Consolidate the reserved ranges or split the domain.',
  [Code.TopologySchemaVersionUnsupported]: 'Topology allocation-schema version {version} is newer than this build supports (max {max}); it was created by a newer version of YAOG. Upgrade YAOG to open it.',
  // --- semantic-pass templates (mirror of validator.registry, code.go) ---
  [Code.RoutePolicyReserved]: 'route_policies is a reserved feature that is not yet implemented: no renderer consumes it, the compiler only passes it through verbatim, so it must be empty (detected {count} policies; please clear route_policies; for LAN bridging / route injection use extra_prefixes instead)',
  [Code.NodeDomainRefMissing]: 'Node {node} references a non-existent Domain {id}',
  [Code.EdgeNodeRefMissing]: 'Edge {id} references a non-existent node {other}',
  [Code.NodeOverlayIPOutOfCIDR]: 'Overlay IP {cidr} of node {node} is not within the CIDR {prefix} of Domain {name}',
  [Code.NodeOverlayIPConflict]: 'Overlay IP {cidr} conflicts: already used by node {other}, also assigned to node {node}',
  [Code.DomainIDDuplicate]: 'Duplicate Domain ID: {id}',
  [Code.NodeIDDuplicate]: 'Duplicate Node ID: {id}',
  [Code.EdgeIDDuplicate]: 'Duplicate Edge ID: {id}',
  [Code.NodeNameDuplicate]: 'Duplicate node name: node {other} and node {node} use the same name {name}',
  [Code.NodeNameInstallerCollision]: 'Node names produce the same installer script filename: node {other} and node {node} both normalize to {name}, which will cause silent skips or identity mismatches during deployment',
  [Code.NodeNameInterfaceCollision]: 'Node names produce the same WireGuard interface name: node {other} and node {node} both normalize to {name}, which will cause one interface configuration to overwrite the other',
  [Code.NodeEffectivePortRangeOverflow]: 'Node {node} has an effective listen port range of {low}-{high} (base port {base} + {count} peer interfaces); the highest port {high} exceeds 65535 and will produce an undeployable WireGuard configuration',
  [Code.NodeIsolated]: 'Node {node} ({id}) is isolated and not connected to any enabled edge',
  [Code.ClientInboundRejected]: 'Client node {node} cannot accept inbound connections',
  [Code.ClientTargetPeer]: 'Client {node} cannot connect to peer {other} (peers do not forward traffic)',
  [Code.ClientTargetClient]: 'Client {node} cannot connect to another client {other}',
  [Code.ClientEndpointHostRequired]: 'Client {node} requires endpoint_host to reach the router',
  [Code.ClientNoOutboundEdge]: 'Client {node} must have exactly one enabled outbound edge',
  [Code.ClientMultipleOutboundEdges]: 'Client {node} has {count} outbound edges but must have exactly one (single wg0 interface)',
  [Code.ClientRouterIDMeaningless]: 'Client {node} has router_id set but clients do not run Babel',
  [Code.ClientExtraPrefixesMeaningless]: 'Client {node} has extra_prefixes set but clients do not announce routes',
  [Code.EdgeMimicPlatformUnsupported]: 'Edge {id} uses tcp transport (mimic), but endpoint node {node} has platform {platform} which is not a deployable Linux: mimic is an eBPF/kernel feature, so both ends of a tcp edge must be Linux (debian / ubuntu)',
  [Code.PinClientPortPin]: 'Edge {id} touches a client node but sets a port pin: clients use a single wg0 with no per-peer listen ports; please clear pinned_from_port / pinned_to_port on this edge',
  [Code.PinClientAllocationIgnored]: 'Edge {id} touches a client node; its allocation pins will be ignored: clients use a single wg0 and do not participate in per-peer transit/link-local allocation',
  [Code.PinPortIncomplete]: 'Edge {id} has an incomplete listen port pin (only one end is pinned): pins must be set in pairs; please complete both pinned_from_port and pinned_to_port, or clear both',
  [Code.PinTransitIPIncomplete]: 'Edge {id} has an incomplete transit IP pin (only one end is pinned): pins must be set in pairs; please complete both pinned_from_transit_ip and pinned_to_transit_ip, or clear both',
  [Code.PinLinkLocalIncomplete]: 'Edge {id} has an incomplete link-local pin (only one end is pinned): pins must be set in pairs; please complete both pinned_from_link_local and pinned_to_link_local, or clear both',
  [Code.PinPortOutOfRange]: 'Port pin {port} for node {node} is out of range: it must be between {base} and 65535 (clear this pin if renumbering is needed)',
  [Code.PinTransitIPInvalid]: 'transit IP pin {cidr} is not a valid IP address',
  [Code.PinTransitIPOutOfCIDR]: 'transit IP pin {cidr} is not within the edge transit address pool {prefix} (the pool may have been narrowed; clear this pin to renumber)',
  [Code.PinPortDuplicateCrossLink]: 'Port pin {port} is occupied by two different links on the node: edge {other} and edge {id} pin the same listen port on the same node',
  [Code.PinTransitIPDuplicateCrossLink]: 'transit IP pin {cidr} is occupied by two different links: edge {other} and edge {id} pin the same transit address',
  [Code.PinLinkLocalDuplicateCrossLink]: 'link-local pin {cidr} is occupied by two different links: edge {other} and edge {id} pin the same link-local address',
  [Code.EdgeEndpointNoMatch]: 'Edge {id} dials {other} but target {node} has no matching public endpoint (the endpoint snapshot may be stale after a node edit)',
  [Code.EdgeDuplicateEnabledSameDirection]: 'Edge {id} and edge {other} connect the same pair of nodes (same direction) and both belong to the primary class; only the first takes effect at compile time and this edge endpoint settings will be ignored; please delete or disable the redundant edge — if redundant backup was intended, set this edge role to backup so it becomes an independent backup link',
  [Code.NodeInterfaceNameCollision]: 'Node {node} has two links generating the same WireGuard interface name {name}: {prefix} collides with {other}, one interface configuration will overwrite the other; please rename one of the colliding nodes to eliminate the 4-digit hash collision',
  [Code.EdgeMultipleExplicitPrimary]: 'Edge {id} and edge {other} connect the same pair of nodes and are both explicitly marked role primary: each pair of nodes may have at most one primary link, the compiler folds the primary class and ignores the rest; please change one to backup or clear its role',
  [Code.EdgeBackupTouchesClient]: 'Edge {id} touches a client node but is marked as backup: clients use a single wg0, do not run Babel, and have no per-peer interfaces or cost-based failover, so backup links are meaningless for them; please clear this edge role or delete the edge',
  [Code.LinkNoPrimary]: 'All links between node {node} and {other} are backup with no primary link: Babel will forward across the backup links with no primary/backup distinction (a role flip may have left out the primary); please change one to primary or clear its role',
  [Code.LinkEqualCost]: 'There are {count} links between node {node} and {other} but all resolved costs are identical ({low}): Babel cannot prefer any one of them and the configuration cannot express failover; please set distinct costs per link via role backup or priority/weight',
  [Code.NATTargetUnreachable]: 'Edge {edge}: target node {node} has no public IP and does not accept inbound connections; the peer will not be able to initiate a connection to it',
  [Code.NATDeadLink]: 'Edge {edge}: nodes {from} and {to} are both behind NAT, neither direction provides an endpoint host address, and neither end accepts inbound connections; the direct tunnel cannot be established (confirmed dead link). Configure a public endpoint on one end, or route through a relay instead',
  [Code.NATDoubleNATNoEndpoint]: 'Edge {edge}: nodes {from} and {to} are both behind NAT and provide no endpoint host address; the direct tunnel cannot be established (a relay or public relay is required)',
  [Code.NATNoOutboundToPublic]: 'Node {name} ({id}) is behind NAT and has no outbound connection to any public, inbound-capable, or relay node; it will not be able to join the overlay',
};
