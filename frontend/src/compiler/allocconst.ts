// Allocation constants — the TypeScript mirror of internal/allocconst (the single source of truth the
// Go compiler, semantic validator, and script renderer all share). These values MUST agree with the Go
// constants byte-for-byte: a transit-CIDR or link-cost mismatch would let a pin pass TS validation and
// be rejected by the Go controller (or vice versa). plan-8 single-sourced these into Go's
// internal/allocconst; this is the canonical TS mirror the local-mode compiler uses.

// DefaultTransitCIDR is the fallback transit address pool used when a domain does not configure
// transit_cidr. Mirrors allocconst.DefaultTransitCIDR (internal/allocconst/allocconst.go:29).
// It must equal the literal at every Go pinned site (peers.go, semantic.go, script.go).
export const DefaultTransitCIDR = '10.10.0.0/24';

// BackupDefaultLinkCost is the preset Babel rxcost a backup link adopts when it has no explicit
// Priority/Weight: 384 = 4x babeld's wired default cost (96). Mirrors
// allocconst.BackupDefaultLinkCost (internal/allocconst/allocconst.go:36).
export const BackupDefaultLinkCost = 384;

// MinPinnedPort is the lower bound for an operator-chosen pinned listen port (auto-allocation still
// starts at 51820, but a NAT-VPS may forward a fixed lower range). Mirrors allocconst.MinPinnedPort
// (internal/allocconst/allocconst.go:46).
export const MinPinnedPort = 1024;
