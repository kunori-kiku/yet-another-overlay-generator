package allocator

import (
	"context"
	"net"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestAllocateIPs_AddressFamilyAndSizeBounds verifies the allocator's
// defense-in-depth behavior around address family and CIDR size.
//
// Schema validation is the first line of defense that rejects IPv6 (in the
// validator package); this test drives the allocator directly to prove that
// even if a non-IPv4 CIDR bypasses the schema and reaches the allocator, the
// result is only a clean error rather than a panic -- this is the regression
// gate against the historical ip[12:16] out-of-bounds slice panic.
//
// S1 (plan-8 Phase 2) intentionally changed one case here: a /8 IPv4 CIDR is no
// longer allocated — it is ~16.7M host candidates, the DoS scan-vector the new
// per-node scan budget exists to stop, so it is now rejected FAST with a clean
// coded error (CodeOverlayScanBudgetExceeded) rather than entering a multi-million
// iteration linear scan. The "large CIDR math must not overflow / panic" regression
// intent is preserved by the /12 case below (the largest network still within the
// scan budget), which both exercises the large-host-count math and allocates.
func TestAllocateIPs_AddressFamilyAndSizeBounds(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		wantErr bool
		wantIP  string // checked only when wantErr is false
	}{
		{
			// Regression gate: an IPv6 domain CIDR must return a clean error, never panic.
			// fd00::/64 has 64 host bits (>=32), so it is caught at the host-bit overflow guard.
			name:    "IPv6 domain CIDR returns a clean error and does not panic",
			cidr:    "fd00::/64",
			wantErr: true,
		},
		{
			// Even if a longer IPv6 prefix (host bits < 32) bypasses the overflow guard,
			// the To4() guard in ipToUint32 still returns an error rather than panicking.
			name:    "long-prefix IPv6 CIDR also returns a clean error",
			cidr:    "fd00::/120",
			wantErr: true,
		},
		{
			// S1: a /8 IPv4 CIDR (~16.7M candidates) now exceeds the per-node scan budget and is
			// rejected up front with a clean coded error — the DoS guard, not an overflow/panic.
			name:    "/8 IPv4 CIDR exceeds the scan budget and is rejected (S1 DoS guard)",
			cidr:    "10.0.0.0/8",
			wantErr: true,
		},
		{
			// A /12 IPv4 CIDR (~1.05M candidates) is the largest network still within the scan
			// budget; it exercises the large-host-count allocator math (the overflow regression
			// gate) AND allocates successfully, proving the math is sound right up to the cap.
			name:    "/12 IPv4 CIDR allocates (large-but-in-budget; overflow regression gate)",
			cidr:    "10.0.0.0/12",
			wantErr: false,
			wantIP:  "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topo := &model.Topology{
				Project: model.Project{ID: "test", Name: "Test"},
				Domains: []model.Domain{{
					ID:             "domain-1",
					Name:           "test",
					CIDR:           tt.cidr,
					AllocationMode: "auto",
					RoutingMode:    "babel",
				}},
				Nodes: []model.Node{
					{ID: "n1", Name: "node-1", Role: "router", DomainID: "domain-1"},
				},
				Edges: []model.Edge{},
			}

			alloc := NewIPAllocator()
			nodes, err := alloc.AllocateIPs(context.Background(), topo)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("CIDR %s should return an error, but allocation succeeded", tt.cidr)
				}
				return
			}

			if err != nil {
				t.Fatalf("CIDR %s should allocate successfully, but returned an error: %v", tt.cidr, err)
			}
			if len(nodes) != 1 {
				t.Fatalf("should return 1 node, actually returned %d", len(nodes))
			}
			if nodes[0].OverlayIP != tt.wantIP {
				t.Errorf("CIDR %s allocation result should be %s, actually %s", tt.cidr, tt.wantIP, nodes[0].OverlayIP)
			}
		})
	}
}

// TestIPToUint32_Errors verifies that ipToUint32 returns an error for nil and for 16-byte non-v4-mappable input.
func TestIPToUint32_Errors(t *testing.T) {
	tests := []struct {
		name    string
		ip      net.IP
		wantErr bool
	}{
		{
			name:    "nil address returns an error",
			ip:      nil,
			wantErr: true,
		},
		{
			// A 16-byte pure IPv6 address (non-v4-mappable); To4() returns nil.
			name:    "16-byte IPv6 address returns an error",
			ip:      net.ParseIP("fd00::1"),
			wantErr: true,
		},
		{
			// A normal IPv4 case as a control: must succeed and convert correctly.
			name:    "IPv4 address converts normally",
			ip:      net.ParseIP("10.0.0.1"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ipToUint32(tt.ip)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("address %v should return an error, but converted successfully to %d", tt.ip, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("address %v should convert successfully, but returned an error: %v", tt.ip, err)
			}
			// 10.0.0.1 = 0x0A000001
			const want = uint32(0x0A000001)
			if got != want {
				t.Errorf("address %v conversion result should be %d, actually %d", tt.ip, want, got)
			}
		})
	}
}
