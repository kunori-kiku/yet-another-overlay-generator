// Package telemetryprotocol contains the leaf-level HTTP extension contract shared by the
// agent client and controller API. Reliable-delivery metadata deliberately lives in headers so
// the JSON request body remains byte-for-shape compatible with strict legacy controllers.
package telemetryprotocol

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

	BootIDBytes = 16

	// MaxMetrics and MaxMetricsBytes are shared admission bounds. The agent applies them before
	// queueing so a deterministic controller 400 can never block the replay queue's head.
	MaxMetrics      = 32
	MaxMetricsBytes = 64 << 10

	// MaxIntervalHeaderBytes bounds parsing work while still accommodating every positive int64.
	MaxIntervalHeaderBytes = 20
)
