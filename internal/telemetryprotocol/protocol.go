// Package telemetryprotocol contains the leaf-level HTTP extension contract shared by the
// agent client and controller API. Reliable-delivery metadata deliberately lives in headers so
// the JSON request body remains byte-for-shape compatible with strict legacy controllers.
package telemetryprotocol

import "strings"

const (
	Version = "2"

	HeaderProtocol       = "X-YAOG-Telemetry-Protocol"
	HeaderBootID         = "X-YAOG-Telemetry-Boot-ID"
	HeaderSequence       = "X-YAOG-Telemetry-Sequence"
	HeaderSampledAt      = "X-YAOG-Telemetry-Sampled-At"
	HeaderIntervalMillis = "X-YAOG-Telemetry-Interval-Millis"
	HeaderAckSequence    = "X-YAOG-Telemetry-Ack-Sequence"
	HeaderReceivedAt     = "X-YAOG-Telemetry-Received-At"
	HeaderDuplicate      = "X-YAOG-Telemetry-Duplicate"
	HeaderCapabilities   = "X-YAOG-Telemetry-Capabilities"

	// CapabilityProbeSamplesV1 is an explicit controller receipt capability. Agents must not add the
	// recent-attempt probe_samples metric, or shorten a deliberately slow heartbeat cadence to flush
	// it, until a successful receipt advertises this token. A later successful receipt without the
	// token disables both behaviors again, which keeps controller rollback safe.
	CapabilityProbeSamplesV1 = "probe-samples-v1"

	BootIDBytes = 16

	// MaxMetrics and MaxMetricsBytes are shared admission bounds. The agent applies them before
	// queueing so a deterministic controller 400 can never block the replay queue's head.
	MaxMetrics      = 32
	MaxMetricsBytes = 64 << 10

	// MaxIntervalHeaderBytes bounds parsing work while still accommodating every positive int64.
	MaxIntervalHeaderBytes = 20

	// MaxCapabilitiesHeaderBytes bounds optional receipt parsing. The currently defined response is
	// one short token, but leaving a small extension budget avoids coupling future capabilities to an
	// unbounded proxy-controlled string.
	MaxCapabilitiesHeaderBytes = 1024
)

// HasCapability reports whether a comma-separated receipt capability header contains the exact
// requested token. Oversized or empty headers safely mean "unsupported".
func HasCapability(header, capability string) bool {
	if header == "" || capability == "" || len(header) > MaxCapabilitiesHeaderBytes {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		if strings.TrimSpace(candidate) == capability {
			return true
		}
	}
	return false
}
