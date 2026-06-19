// Compiler model — the TypeScript mirror of the Go compile-output types.
//
// This module CONSUMES the frozen wire model (`../types/topology.ts`: Topology / Node / Edge,
// including `router_id?` on Node (F2; mirrors internal/model/topology.go:89), the six `pinned_*`
// fields + `compiled_port` + `alloc_schema_version`) and ADDS the compile-OUTPUT types the Go
// pipeline produces: PeerInfo, ClientPeerInfo, KeyPair, and CompileResult. The output types mirror
// internal/compiler/compiler.go:23-62 (CompileResult), internal/compiler/peers.go:193-259 (PeerInfo),
// and internal/compiler/peers.go:1170-1198 (ClientPeerInfo) field-for-field.
//
// Go is the authoritative oracle: these types name the exact data the conformance harness (plan-5)
// pins byte-for-byte against the Go side. Do NOT re-freeze the wire model here — re-export it so the
// rest of the compiler imports one canonical surface.

import type {
  CompileManifest,
  Domain,
  Edge,
  Node,
  NodeCapabilities,
  Project,
  PublicEndpoint,
  RoutePolicy,
  Topology,
  ValidationError,
} from '../types/topology';

// Re-export the frozen wire model so the compiler library exposes a single canonical model surface
// (consumers import { Topology, Node, ... } from './model' rather than reaching across to ../types).
export type {
  CompileManifest,
  Domain,
  Edge,
  Node,
  NodeCapabilities,
  Project,
  PublicEndpoint,
  RoutePolicy,
  Topology,
  ValidationError,
};

// KeyPair is a WireGuard key pair. Mirrors internal/compiler/peers.go:188-191 (compiler.KeyPair).
export interface KeyPair {
  privateKey: string;
  publicKey: string;
}

// PeerInfo describes the complete configuration of a point-to-point WireGuard interface.
// One WireGuard interface per peer (the per-peer interface architecture). Mirrors
// internal/compiler/peers.go:193-259 (compiler.PeerInfo) field-for-field.
export interface PeerInfo {
  // Remote node ID.
  nodeID: string;
  // Remote node name.
  nodeName: string;
  // Remote node public key.
  publicKey: string;
  // Remote node overlay IP.
  overlayIP: string;
  // AllowedIPs (the per-peer model uses a permissive policy: 0.0.0.0/0, ::/0).
  allowedIPs: string[];
  // Endpoint (remote public address).
  endpoint: string;
  // PersistentKeepalive.
  persistentKeepalive: number;
  // WireGuard interface name (e.g. wg-dmit, capped at 15 chars on Linux).
  interfaceName: string;
  // Dedicated listen port for this interface.
  listenPort: number;
  // Local transit IP (point-to-point link address).
  localTransitIP: string;
  // Remote transit IP.
  remoteTransitIP: string;
  // Local IPv6 link-local address (required by Babel).
  localLinkLocal: string;
  // Remote IPv6 link-local address.
  remoteLinkLocal: string;
  // Whether this is the router-side interface connecting to a client.
  isClientPeer: boolean;
  // Client overlay IP (set only when isClientPeer=true, used for PostUp route injection).
  clientOverlayIP: string;
  // This link's Babel rxcost override, derived from the corresponding edge (D63).
  // 0 means adopt the role preset's default cost (decided by the Babel renderer).
  linkCost: number;
  // Whether this link has mimic (tcp shaping transport) enabled: equivalent to
  // link.primaryEdge.Transport=="tcp".
  mimic: boolean;
  // The effective WireGuard MTU this interface emits.
  // non-mimic: keep node.mtu as is (0 => renderer omits the MTU line).
  // mimic: ((node.mtu>0 ? node.mtu : 1420) − 12).
  mtu: number;
}

// ClientPeerInfo describes the information needed for a client node's wg0 configuration.
// Mirrors internal/compiler/peers.go:1170-1198 (compiler.ClientPeerInfo) field-for-field.
export interface ClientPeerInfo {
  // Client node information.
  nodeID: string;
  nodeName: string;
  overlayIP: string;
  // The effective MTU of the wg0 interface (same mimic formula as PeerInfo.mtu).
  mtu: number;
  // Whether the client's single outbound edge has mimic enabled (transport=="tcp").
  mimic: boolean;
  // The client's WireGuard private key.
  privateKey: string;
  // Router-side information.
  routerPublicKey: string;
  routerEndpoint: string; // host:port
  // List of domain CIDRs (used as AllowedIPs).
  domainCIDRs: string[];
  // The client's listen port.
  listenPort: number;
}

// CompileResult holds the output of a full compilation: the resolved topology, per-node peer maps,
// all rendered configs and scripts, and the manifest. Mirrors internal/compiler/compiler.go:25-67
// (compiler.CompileResult). The Go map[string]X fields become Record<string, X> here.
export interface CompileResult {
  // The compiled topology (with allocated IPs).
  topology: Topology;
  // Maps each node ID to its derived peer entries.
  peerMap: Record<string, PeerInfo[]>;
  // The rendered WireGuard config per node.
  wireGuardConfigs: Record<string, string>;
  // The rendered Babel config per node.
  babelConfigs: Record<string, string>;
  // The rendered sysctl settings per node.
  sysctlConfigs: Record<string, string>;
  // The rendered install script per node.
  installScripts: Record<string, string>;
  // Per-node, controller-signed artifacts.json content (nodeID -> JSON), carrying the mimic
  // GitHub-.deb pins. EMPTY in local mode (no catalog configured), so export omits the file — the
  // air-gap bundle stays byte-identical.
  artifactsJSON: Record<string, string>;
  // The auto-generated deploy script per node.
  deployScripts: Record<string, string>;
  // The wg0 config info for client-role nodes.
  clientConfigs: Record<string, ClientPeerInfo>;
  // Non-fatal warnings produced by the schema and semantic stages, surfaced after a successful compile.
  warnings: ValidationError[];
  // The compile manifest summarizing this build.
  manifest: CompileManifest;
}
