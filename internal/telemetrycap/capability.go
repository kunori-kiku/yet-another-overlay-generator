// Package telemetrycap defines the pure, closed compatibility contract shared by the compiler,
// rendered installers, and agent runtime. It deliberately imports no stateful runtime package so
// the stateless compile pipeline can depend on it without crossing the architecture boundary.
package telemetrycap

import "sort"

const (
	InstallerPolicyV1Env = "YAOG_AGENT_CAP_TELEMETRY_POLICY_V1"

	PolicyV1 = "telemetry-policy-v1"
	PolicyV2 = "telemetry-policy-v2"
	URLV1    = "url-probes-v1"
	DeviceV1 = "device-telemetry-v1"

	// ControllerTopologyPolicyV2 is advertised by authenticated login/session responses when the
	// controller preserves successor-only topology fields. Its absence is the safe old-controller
	// signal: the panel must not write a successor-bearing draft.
	ControllerTopologyPolicyV2 = "telemetry-policy-v2-topology"

	InstallerPolicyV2Env = "YAOG_AGENT_CAP_TELEMETRY_POLICY_V2"
	InstallerURLV1Env    = "YAOG_AGENT_CAP_URL_PROBES_V1"
	InstallerDeviceV1Env = "YAOG_AGENT_CAP_DEVICE_TELEMETRY_V1"
)

// Definition is the single launcher contract for an agent telemetry capability. Keeping the marker
// and refusal text beside the wire token makes a newly required capability impossible to render as
// an accidentally ungated installer.
type Definition struct {
	Token                string
	InstallerEnvironment string
	InstallerError       string
}

var definitions = map[string]Definition{
	PolicyV1: {
		Token: PolicyV1, InstallerEnvironment: InstallerPolicyV1Env,
		InstallerError: "ERROR: this bundle contains signed active telemetry policy and requires yaog-agent v2.0.0-rc.9 or later; upgrade the agent before applying",
	},
	PolicyV2: {
		Token: PolicyV2, InstallerEnvironment: InstallerPolicyV2Env,
		InstallerError: "ERROR: this bundle contains successor signed telemetry policy and requires a telemetry-policy-v2 capable yaog-agent; upgrade the agent before applying",
	},
	URLV1: {
		Token: URLV1, InstallerEnvironment: InstallerURLV1Env,
		InstallerError: "ERROR: this bundle contains signed URL probes and requires a url-probes-v1 capable yaog-agent; upgrade the agent before applying",
	},
	DeviceV1: {
		Token: DeviceV1, InstallerEnvironment: InstallerDeviceV1Env,
		InstallerError: "ERROR: this bundle contains signed device telemetry policy and requires a device-telemetry-v1 capable yaog-agent; upgrade the agent before applying",
	},
}

// Lookup returns launcher metadata only for a member of the closed capability contract.
func Lookup(token string) (Definition, bool) {
	definition, ok := definitions[token]
	return definition, ok
}

// InstallerEnvironments returns every marker owned by this closed contract in deterministic order.
// The launcher strips this complete set before adding only the capabilities its binary implements.
func InstallerEnvironments() []string {
	environments := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		environments = append(environments, definition.InstallerEnvironment)
	}
	sort.Strings(environments)
	return environments
}
