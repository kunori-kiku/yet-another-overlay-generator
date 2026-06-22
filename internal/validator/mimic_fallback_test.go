package validator

import "testing"

// TestValidate_MimicFallbackEnum pins the plan-4 schema enum: "" (inherit) / "udp" / "none" are
// accepted; a typo ("UDP"/"off"/"yes") is rejected with CodeEdgeMimicFallbackInvalid.
func TestValidate_MimicFallbackEnum(t *testing.T) {
	accept := []string{"", "udp", "none"}
	for _, v := range accept {
		topo := mimicTransportTopology("tcp", "debian", "ubuntu")
		topo.Edges[0].MimicFallback = v
		if hasCode(ValidateSchema(topo), CodeEdgeMimicFallbackInvalid) {
			t.Errorf("mimic_fallback=%q should be accepted, got CodeEdgeMimicFallbackInvalid", v)
		}
	}

	reject := []string{"UDP", "off", "yes", "tcp"}
	for _, v := range reject {
		topo := mimicTransportTopology("tcp", "debian", "ubuntu")
		topo.Edges[0].MimicFallback = v
		if !hasCode(ValidateSchema(topo), CodeEdgeMimicFallbackInvalid) {
			t.Errorf("mimic_fallback=%q should be rejected with CodeEdgeMimicFallbackInvalid", v)
		}
	}
}

// TestValidate_MimicFallbackOnUdpEdge_NoError confirms the field is harmless on a udp edge: a valid
// enum value set on a transport=="udp" edge produces no mimic_fallback error (the policy is inert
// there; the resolver floors a udp peer to "none" regardless).
func TestValidate_MimicFallbackOnUdpEdge_NoError(t *testing.T) {
	topo := mimicTransportTopology("udp", "debian", "ubuntu")
	topo.Edges[0].MimicFallback = "udp"
	if hasCode(ValidateSchema(topo), CodeEdgeMimicFallbackInvalid) {
		t.Fatalf("a valid mimic_fallback on a udp edge must not error")
	}
}
