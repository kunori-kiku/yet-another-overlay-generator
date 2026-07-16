package runtimecontract

// InstallerCapabilityTelemetryPolicyV1Env is the execution capability marker supplied by a
// telemetry-policy-aware yaog-agent when it launches a verified install.sh. Probe-bearing,
// AgentHeld bundles require this marker before host mutation so an older agent cannot apply the
// network generation, ignore telemetry.json, and falsely report the generation as fully applied.
//
// This is deliberately a capability contract rather than a version-string comparison: custom and
// development builds can advertise the behavior they actually implement, while future policy
// versions can add a new marker without weakening this one.
const InstallerCapabilityTelemetryPolicyV1Env = "YAOG_AGENT_CAP_TELEMETRY_POLICY_V1"
