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
		{"single peer never handshaked (all down)", ifaceLine("wg-a") + "\n" + peerLine("wg-a", 0), reasonWGLinkDown, model.ConditionStatusWarn},
		{"ALL peers never handshaked", ifaceLine("wg-a") + "\n" + peerLine("wg-a", 0) + "\n" + ifaceLine("wg-b") + "\n" + peerLine("wg-b", 0),
			reasonWGLinkDown, model.ConditionStatusWarn},
		{"peer stale", ifaceLine("wg-a") + "\n" + peerLine("wg-a", stale), reasonWGPeerHandshakeStale, model.ConditionStatusWarn},
		{"all peers up", ifaceLine("wg-a") + "\n" + peerLine("wg-a", fresh), reasonWGAllPeersUp, model.ConditionStatusOK},
		// One offline peer among up peers must NOT flip the whole node to LinkDown — it is SomePeersDown.
		{"some peers down (one never, one up)", ifaceLine("wg-a") + "\n" + peerLine("wg-a", 0) + "\n" + ifaceLine("wg-b") + "\n" + peerLine("wg-b", fresh),
			reasonWGSomePeersDown, model.ConditionStatusWarn},
		// never (partial) outranks stale.
		{"some-never beats stale", ifaceLine("wg-a") + "\n" + peerLine("wg-a", 0) + "\n" + ifaceLine("wg-b") + "\n" + peerLine("wg-b", stale),
			reasonWGSomePeersDown, model.ConditionStatusWarn},
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

// TestSamplePeers pins the per-peer link detail the telemetry metric carries (the collapsible panel):
// one entry per peer line, the peer name derived from the wg-<peer> interface, and the up/stale/never
// classification + endpoint sanitization.
func TestSamplePeers(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second).Unix()
	stale := now.Add(-10 * time.Minute).Unix()
	// bravo is a never-connected peer with no endpoint yet — wg prints "(none)", which must be
	// sanitized to empty so the panel shows no spurious endpoint.
	bravoNever := "wg-bravo\tPEERPUB=\t(none)\t(none)\t10.10.0.2/32\t0\t0\t0\t0"
	dump := ifaceLine("wg-alpha") + "\n" + peerLine("wg-alpha", fresh) + "\n" +
		ifaceLine("wg-bravo") + "\n" + bravoNever + "\n" +
		ifaceLine("wg-charlie") + "\n" + peerLine("wg-charlie", stale)

	peers := samplePeers([]byte(dump), now)
	if len(peers) != 3 {
		t.Fatalf("got %d peers, want 3: %+v", len(peers), peers)
	}
	want := []struct {
		peer, status string
		hasHS        bool
	}{
		{"alpha", "up", true},
		{"bravo", "never", false},
		{"charlie", "stale", true},
	}
	for i, w := range want {
		if peers[i].Peer != w.peer || peers[i].Status != w.status {
			t.Errorf("peer[%d] = {%q,%q}, want {%q,%q}", i, peers[i].Peer, peers[i].Status, w.peer, w.status)
		}
		if (peers[i].LastHandshake != 0) != w.hasHS {
			t.Errorf("peer[%d] %q LastHandshake=%d, want hasHandshake=%v", i, w.peer, peers[i].LastHandshake, w.hasHS)
		}
	}
	// "(none)" endpoint is sanitized to empty (no spurious endpoint shown for a not-yet-connected peer).
	if peers[1].Endpoint != "" {
		t.Errorf("never-handshaked peer endpoint = %q, want empty (sanitized from (none))", peers[1].Endpoint)
	}
}

// TestWireguardPeersSampler proves the telemetry sampler emits the per-peer detail under the
// wireguard_peers metric key (best-effort: a probe error emits nothing).
func TestWireguardPeersSampler(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	orig := wgShowFn
	t.Cleanup(func() { wgShowFn = orig })

	wgShowFn = func() ([]byte, error) { return nil, errors.New("wg: not found") }
	if conds, m := (wireguardPeersSampler{}).Sample(now); conds != nil || m != nil {
		t.Fatalf("probe error must emit nothing, got conds=%v metrics=%v", conds, m)
	}

	dump := ifaceLine("wg-alpha") + "\n" + peerLine("wg-alpha", now.Add(-10*time.Second).Unix())
	wgShowFn = func() ([]byte, error) { return []byte(dump), nil }
	conds, m := (wireguardPeersSampler{}).Sample(now)
	if conds != nil {
		t.Fatalf("peers sampler must emit no conditions (it is a metric), got %+v", conds)
	}
	if _, ok := m[wgMetricKey]; !ok {
		t.Fatalf("metrics missing %q key: %+v", wgMetricKey, m)
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
