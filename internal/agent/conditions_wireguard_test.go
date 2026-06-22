package agent

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// ifaceLine / peerLine build `wg show all dump` rows (tab-separated, interface-prefixed): an interface
// line has 5 fields, a peer line 9 (latest-handshake at index 5).
func ifaceLine(iface string) string {
	return iface + "\tPRIV=\tPUB=\t51820\toff"
}
func peerLine(iface string, handshakeEpoch int64) string {
	return fmt.Sprintf("%s\tPEERPUB=\t(none)\t1.2.3.4:51820\t10.10.0.2/32\t%d\t0\t0\t0", iface, handshakeEpoch)
}

// TestClassifyWGDump is the table test over the pure `wg show all dump` classifier.
func TestClassifyWGDump(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second).Unix()
	stale := now.Add(-10 * time.Minute).Unix() // past wgHandshakeStaleAfter (5m)

	cases := []struct {
		name       string
		dump       string
		wantReason string
		wantStatus string
	}{
		{"no interfaces", "", reasonWGNoInterfaces, model.ConditionStatusWarn},
		{"interface, no peers", ifaceLine("wg-a"), "", ""},
		{"peer never handshaked", ifaceLine("wg-a") + "\n" + peerLine("wg-a", 0), reasonWGLinkDown, model.ConditionStatusWarn},
		{"peer stale", ifaceLine("wg-a") + "\n" + peerLine("wg-a", stale), reasonWGPeerHandshakeStale, model.ConditionStatusWarn},
		{"all peers up", ifaceLine("wg-a") + "\n" + peerLine("wg-a", fresh), reasonWGAllPeersUp, model.ConditionStatusOK},
		{"never beats stale", ifaceLine("wg-a") + "\n" + peerLine("wg-a", 0) + "\n" + peerLine("wg-a", stale),
			reasonWGLinkDown, model.ConditionStatusWarn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, status, msg := classifyWGDump([]byte(tc.dump), now)
			if reason != tc.wantReason || status != tc.wantStatus {
				t.Fatalf("got (%q,%q) want (%q,%q)", reason, status, tc.wantReason, tc.wantStatus)
			}
			if reason != "" && msg == "" {
				t.Fatalf("non-empty reason %q must carry a curated message", reason)
			}
		})
	}
}

// TestSampleWireGuardCondition_BestEffort proves a probe error yields NO condition (never an error),
// and a good dump yields the classified condition.
func TestSampleWireGuardCondition_BestEffort(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	orig := wgShowFn
	t.Cleanup(func() { wgShowFn = orig })

	// Probe error → best-effort: no condition, no panic, no error surfaced.
	wgShowFn = func() ([]byte, error) { return nil, errors.New("wg: command not found") }
	if c, has := sampleWireGuardCondition(now); has {
		t.Fatalf("probe error must yield no condition, got %+v", c)
	}

	// Good dump (all peers up) → a wireguard/AllPeersUp condition.
	dump := ifaceLine("wg-a") + "\n" + peerLine("wg-a", now.Add(-10*time.Second).Unix())
	wgShowFn = func() ([]byte, error) { return []byte(dump), nil }
	c, has := sampleWireGuardCondition(now)
	if !has || c.Type != model.ConditionTypeWireGuard || c.Reason != reasonWGAllPeersUp || c.Status != model.ConditionStatusOK {
		t.Fatalf("good dump: has=%v cond=%+v, want wireguard/AllPeersUp/ok", has, c)
	}
}
