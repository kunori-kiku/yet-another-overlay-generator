// Package runtimecontract holds the stateful agent<->controller runtime-report types — the
// wire contract by which a live agent reports facts about itself to the controller. These are
// NOT topology-schema types (those live in internal/model, the pure leaf); they are runtime
// state, so they are homed here, outside the pure compile core.
package runtimecontract

// Condition is one structured, curated feedback fact an agent reports about itself, in the
// Kubernetes-conditions shape. It REPLACES brittle free-form-health string-matching (the panel
// greps State.Health today). It is additive and backward-compatible: an old agent sends none, an
// old controller ignores the field. The full set per node is a small slice keyed by Type (one
// active condition per Type; a later report for the same Type supersedes the prior).
//
// Curation invariant (HIGH for the agent-feedback subject): Reason is a CLOSED enum (CamelCase
// code) per Type, and Message is a SINGLE length-capped human line produced by a classify() mapping
// — NEVER the raw stderr / LastError dump. Message is for an operator tooltip, not a log sink.
//
// Type and Status are plain strings (matching the model-package idiom — Edge.Transport,
// Node.XDPMode are likewise plain strings); the exported constants below are the closed value sets
// every consumer references so there is one source of truth.
type Condition struct {
	// Type is the condition kind ("configapply", "selfupdate", "wireguard", "mimic"). Stable,
	// machine-readable; the panel may render a richer chip for known types and a generic strip for
	// the rest.
	Type string `json:"type"`
	// Status is the coarse state for color-coding: "ok" | "warn" | "error" | "unknown".
	Status string `json:"status"`
	// Reason is a closed, CamelCase enum code scoped to Type (e.g. "Applied",
	// "DegradedKeepingLastGood"). Stable across releases so the panel/tests can switch on it.
	Reason string `json:"reason"`
	// Message is a single, length-capped human line (curated by classify(), never raw stderr).
	Message string `json:"message,omitempty"`
	// Since is the AGENT-side wall-clock time the condition last changed, RFC3339. Advisory only —
	// the controller server-stamps an authoritative ObservedAt on receipt (a node clock cannot be
	// trusted for ordering). Empty when the agent did not set it.
	Since string `json:"since,omitempty"`
}

// Condition status constants — the closed status enum for color-coding.
const (
	ConditionStatusOK      = "ok"
	ConditionStatusWarn    = "warn"
	ConditionStatusError   = "error"
	ConditionStatusUnknown = "unknown"
)

// Condition type constants — the closed type enum. plan-1 wires only ConditionTypeConfigApply;
// plan-3 adds SelfUpdate + WireGuard, plan-5 adds Mimic. Declared here so every consumer references
// one source of truth.
const (
	ConditionTypeConfigApply = "configapply"
	ConditionTypeSelfUpdate  = "selfupdate"
	ConditionTypeWireGuard   = "wireguard"
	ConditionTypeMimic       = "mimic"
)

// ConditionMessageMax is the hard cap on a curated Message (runes). classify() helpers MUST
// truncate to this. Caps the tooltip to one line and forbids a multi-line stderr dump leaking in.
const ConditionMessageMax = 160
