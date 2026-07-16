package telemetryprotocol

import (
	"strings"
	"testing"
)

func TestHasCapability(t *testing.T) {
	for _, header := range []string{
		CapabilityProbeSamplesV1,
		"future-v1, " + CapabilityProbeSamplesV1,
		"  " + CapabilityProbeSamplesV1 + "  ,future-v1",
	} {
		if !HasCapability(header, CapabilityProbeSamplesV1) {
			t.Fatalf("HasCapability(%q) = false", header)
		}
	}
	for _, header := range []string{
		"",
		"probe-samples",
		CapabilityProbeSamplesV1 + "-suffix",
		strings.Repeat("x", MaxCapabilitiesHeaderBytes+1),
	} {
		if HasCapability(header, CapabilityProbeSamplesV1) {
			t.Fatalf("HasCapability(%q) = true", header)
		}
	}
}
