// Package allocconst is the single source of truth for the shared allocation
// constants used across the compiler, the semantic validator, and the script
// renderer. It is a pure leaf package: it imports NOTHING (not even
// internal/model), so any internal package may depend on it without risk of an
// import cycle. The dependency direction is strictly downward —
// compiler/validator/renderer -> allocconst — and allocconst has no back-edge.
//
// Why a dedicated leaf (not internal/model, not internal/linkid):
//   - internal/model carries value types whose layout churns; the allocation
//     constants are decoupled from that churn by living here.
//   - internal/renderer does NOT import internal/linkid, so homing these in
//     linkid would force a spurious renderer->linkid edge.
//
// These three values were previously duplicated as private consts in
// compiler/peers.go, validator/semantic.go, and renderer/script.go, with
// comment-only "must match the same-named constant" sync notes. Single-sourcing
// them here replaces that comment-only contract with a compile-time
// single-definition guarantee, and is the canonical source the TypeScript
// local-mode compiler port mirrors.
package allocconst

// DefaultTransitCIDR is the fallback transit address pool used when a domain
// does not explicitly configure transit_cidr. The compiler resolves empty
// transit_cidr values to this pool at the allocation point, the semantic
// validator judges out-of-pool pins against it, and the script renderer
// parameterizes the SNAT source-address fix rule with it — all three must agree
// on the same pool, otherwise a pin could pass validation and then be rejected
// by the compiler (or vice versa).
const DefaultTransitCIDR = "10.10.0.0/24"

// BackupDefaultLinkCost is the preset Babel rxcost a backup link adopts when it
// has no explicit Priority/Weight: 384 = 4x babeld's wired default cost (96).
// Babel never prefers a backup while the primary link is alive, yet multi-hop
// alternative paths still participate in cost comparison normally. See
// docs/spec/artifacts/babel.md (Link cost resolution).
const BackupDefaultLinkCost = 384

// WGListenPortBase is the fixed base port from which per-peer WireGuard listen ports are
// auto-allocated (the lowest candidate; the allocator skips used values upward). Per-node
// listen_port has been removed, so this base is uniform across the fleet. It is the single
// value the compiler's port allocator and the validator's pin/port checks MUST agree on —
// hoisted here (framework-refactor plan-1) so the two can no longer drift a hardcoded 51820.
const WGListenPortBase = 51820

// MinPinnedPort is the lower bound for an OPERATOR-CHOSEN pinned listen port
// (PR7). Auto-allocation still starts at the default listen port (WGListenPortBase, 51820), but a
// port-restricted NAT VPS often only forwards a fixed range below 51820 (e.g.
// 30000-30100), and the internal listen port must fall inside that range for
// the external->internal forward to work — so a manual pin may legitimately go
// lower than the auto base. 1024 keeps pins out of the privileged-port range
// (the agent runs as root and could bind lower, but 1-1023 risks clashing with
// system services) while admitting every realistic NAT-VPS range.
const MinPinnedPort = 1024
