// Role semantics + capability inference — the TypeScript mirror of internal/compiler/roles.go.
//
// This is the COMPILER-side authority (operates on a full Node, deriving caps from the node's existing
// caps + its public endpoints), distinct from the lighter editor-side ../lib/roleCapabilities.ts
// (deriveCapabilitiesFromRole(role, hasPublicIP), used by NodeForm/RightPanel). They must not diverge:
// this module re-exports the editor helper so there is one import surface and one place each rule
// lives. inferCapabilitiesFromRole mirrors roles.go:108-144 (the compiler's normalize step);
// deriveRoleSemantics mirrors roles.go:42-104; deriveAllowedIPsForPeer mirrors roles.go:148-182.

import type { Domain, Node, NodeCapabilities } from '../types/topology';
export { deriveCapabilitiesFromRole } from '../lib/roleCapabilities';
export type { NodeRole } from '../lib/roleCapabilities';

// hasPublicEndpoints reports whether a node has at least one configured public endpoint. Mirrors Go's
// len(node.PublicEndpoints) > 0 (a missing/undefined array counts as zero).
function hasPublicEndpoints(node: Node): boolean {
  return (node.public_endpoints?.length ?? 0) > 0;
}

// hasExtraPrefixes mirrors Go's len(node.ExtraPrefixes) > 0.
function hasExtraPrefixes(node: Node): boolean {
  return (node.extra_prefixes?.length ?? 0) > 0;
}

// BabelAnnouncePolicy describes which prefixes a node announces over Babel. Mirrors
// compiler.BabelAnnouncePolicy (roles.go:27-39). Optional bools are emitted true only when set in Go.
export interface BabelAnnouncePolicy {
  announceSelf: boolean;
  announceDomainCIDR: boolean;
  announceExtraPrefixes: boolean;
  announceDefault: boolean;
}

// RoleSemantics captures the behavioral semantics a node's role implies. Mirrors
// compiler.RoleSemantics (roles.go:9-24).
export interface RoleSemantics {
  enableForwarding: boolean;
  acceptAllInbound: boolean;
  runBabel: boolean;
  babelAnnounce: BabelAnnouncePolicy;
  allowedIPsMode: string; // "point-to-point" | "relay-all" | "gateway" | "client"
}

// emptyAnnounce is the all-false BabelAnnouncePolicy zero value (Go's BabelAnnouncePolicy{}).
function emptyAnnounce(): BabelAnnouncePolicy {
  return {
    announceSelf: false,
    announceDomainCIDR: false,
    announceExtraPrefixes: false,
    announceDefault: false,
  };
}

// deriveRoleSemantics returns the RoleSemantics for the given node based on its role. Mirrors
// compiler.DeriveRoleSemantics (roles.go:42-104) branch-for-branch, including the public-IP gating of
// acceptAllInbound for router/gateway and the extra-prefixes announce flag.
export function deriveRoleSemantics(node: Node): RoleSemantics {
  switch (node.role) {
    case 'router':
      return {
        enableForwarding: true,
        acceptAllInbound: node.capabilities.has_public_ip,
        runBabel: true,
        babelAnnounce: {
          announceSelf: true,
          announceDomainCIDR: true,
          announceExtraPrefixes: hasExtraPrefixes(node),
          announceDefault: false,
        },
        allowedIPsMode: 'point-to-point',
      };

    case 'relay':
      return {
        enableForwarding: true,
        acceptAllInbound: true,
        runBabel: true,
        babelAnnounce: {
          announceSelf: true,
          announceDomainCIDR: true,
          announceExtraPrefixes: hasExtraPrefixes(node),
          announceDefault: false,
        },
        allowedIPsMode: 'relay-all',
      };

    case 'gateway':
      return {
        enableForwarding: true,
        acceptAllInbound: node.capabilities.has_public_ip,
        runBabel: true,
        babelAnnounce: {
          announceSelf: true,
          announceDomainCIDR: true,
          announceExtraPrefixes: true,
          announceDefault: true,
        },
        allowedIPsMode: 'gateway',
      };

    case 'client':
      return {
        enableForwarding: false,
        acceptAllInbound: false,
        runBabel: false,
        babelAnnounce: emptyAnnounce(),
        allowedIPsMode: 'client',
      };

    default: // "peer"
      return {
        enableForwarding: false,
        acceptAllInbound: false,
        runBabel: true,
        babelAnnounce: {
          announceSelf: true,
          announceDomainCIDR: false,
          announceExtraPrefixes: false,
          announceDefault: false,
        },
        allowedIPsMode: 'point-to-point',
      };
  }
}

// inferCapabilitiesFromRole derives a node's capabilities from its role, starting from the node's
// existing capabilities and overlaying the role-implied defaults. Mirrors
// compiler.InferCapabilitiesFromRole (roles.go:108-144):
//   - hasPub = node.capabilities.has_public_ip || hasPublicEndpoints(node); has_public_ip is normalized
//     UP only (an explicit true is never stripped) and feeds the accept-inbound branches;
//   - router/gateway accept inbound when publicly reachable (preserving an explicit true);
//   - relay always forwards + relays + accepts inbound;
//   - peer keeps its existing caps; client zeroes forward/relay/accept-inbound.
export function inferCapabilitiesFromRole(node: Node): NodeCapabilities {
  const caps: NodeCapabilities = { ...node.capabilities };

  const hasPub = node.capabilities.has_public_ip || hasPublicEndpoints(node);
  caps.has_public_ip = hasPub;

  switch (node.role) {
    case 'router':
      caps.can_forward = true;
      caps.can_accept_inbound = caps.can_accept_inbound || hasPub;
      break;
    case 'relay':
      caps.can_forward = true;
      caps.can_relay = true;
      caps.can_accept_inbound = true;
      break;
    case 'gateway':
      caps.can_forward = true;
      caps.can_accept_inbound = caps.can_accept_inbound || hasPub;
      break;
    case 'peer':
      // peer: no capability overrides; keep the node's existing capabilities.
      break;
    case 'client':
      caps.can_forward = false;
      caps.can_relay = false;
      caps.can_accept_inbound = false;
      break;
  }

  return caps;
}

// deriveAllowedIPsForPeer derives the WireGuard AllowedIPs entries for a peer pointing at remoteNode,
// based on remoteNode's role semantics and domain. Mirrors compiler.DeriveAllowedIPsForPeer
// (roles.go:148-182). domain may be null/undefined (Go passes a possibly-nil *Domain).
export function deriveAllowedIPsForPeer(
  remoteNode: Node,
  domain: Domain | null | undefined,
): string[] {
  const semantics = deriveRoleSemantics(remoteNode);
  const ips: string[] = [];

  switch (semantics.allowedIPsMode) {
    case 'relay-all':
      if (domain && domain.cidr !== '') {
        ips.push(domain.cidr);
      }
      if (remoteNode.extra_prefixes) {
        ips.push(...remoteNode.extra_prefixes);
      }
      if (ips.length === 0 && remoteNode.overlay_ip && remoteNode.overlay_ip !== '') {
        ips.push(remoteNode.overlay_ip + '/32');
      }
      break;

    case 'gateway':
      if (domain && domain.cidr !== '') {
        ips.push(domain.cidr);
      }
      if (remoteNode.extra_prefixes) {
        ips.push(...remoteNode.extra_prefixes);
      }
      if (semantics.babelAnnounce.announceDefault) {
        ips.push('0.0.0.0/0');
      }
      break;

    default: // "point-to-point" (and "client", which Go also routes here via default)
      if (remoteNode.overlay_ip && remoteNode.overlay_ip !== '') {
        ips.push(remoteNode.overlay_ip + '/32');
      }
      break;
  }

  return ips;
}
