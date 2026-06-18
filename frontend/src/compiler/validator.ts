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
// This module ports the SCHEMA half (ValidateSchema) only; the semantic half (ValidateSemantic) lands
// in the next substep and appends to the same ValidationResult model.

import type { Topology } from '../types/topology';
import { parseCIDRFamily, parseIPFamily } from './cidr';

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
  NodePlatformUnsupported: 'validation_node_platform_unsupported',
  NodeXDPModeInvalid: 'validation_node_xdp_mode_invalid',
  NodeOverlayIPInvalid: 'validation_node_overlay_ip_invalid',
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
  [Code.NodePlatformUnsupported]: 'Unsupported platform: {platform}. Allowed values: debian, ubuntu.',
  [Code.NodeXDPModeInvalid]: 'Invalid XDP mode: {mode}. Allowed values: skb, native (empty is equivalent to skb).',
  [Code.NodeOverlayIPInvalid]: 'Invalid overlay IP address: {ip}.',
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
};
