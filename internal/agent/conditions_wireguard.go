package agent

import (
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// conditions_wireguard.go is the best-effort WireGuard link-health sampler (plan-3). It probes
// `wg show all dump` cheaply once per apply cycle and classifies it into a single wireguard condition
// (model.ConditionTypeWireGuard). BEST-EFFORT is load-bearing: a probe error (wg absent, not root, no
// interfaces) yields NO condition and NEVER fails a cycle (PRINCIPLES — deployable configs /
// keep-last-good). The dump is parsed in Go and re-shelled nowhere (root-script safety).

// Closed reason enum for the wireguard link-health condition.
const (
	reasonWGAllPeersUp         = "AllPeersUp"
	reasonWGPeerHandshakeStale = "PeerHandshakeStale"
	reasonWGLinkDown           = "LinkDown"
	reasonWGNoInterfaces       = "NoInterfaces"
)

// wgHandshakeStaleAfter: a peer whose latest handshake is older than this is "stale" (the link may be
// flapping or the peer gone). Generous (well past a typical Babel/WG keepalive horizon) so a healthy
// idle link is never falsely flagged.
const wgHandshakeStaleAfter = 5 * time.Minute

// wgShowFn runs `wg show all dump` and returns its stdout, indirected so a test can inject a fixture
// without a kernel WireGuard interface. A non-nil error (wg absent, not root, no interfaces) is
// BEST-EFFORT: the caller emits NO condition and NEVER fails the cycle.
var wgShowFn = func() ([]byte, error) {
	return exec.Command("wg", "show", "all", "dump").Output()
}

// sampleWireGuardCondition probes link health via `wg show all dump` and classifies it into one
// wireguard condition. The bool is false on any probe error (best-effort) or when there is nothing
// meaningful to report; "no condition" is the (model.Condition{}, false) sentinel, never a nil
// pointer. The message is a curated peer count, never a raw dump.
func sampleWireGuardCondition(now time.Time) (model.Condition, bool) {
	out, err := wgShowFn()
	if err != nil {
		return model.Condition{}, false // best-effort: no probe, no condition, no cycle failure
	}
	reason, status, msg := classifyWGDump(out, now)
	if reason == "" {
		return model.Condition{}, false
	}
	return classify(model.ConditionTypeWireGuard, status, reason, msg, now), true
}

// classifyWGDump is the PURE classifier over `wg show all dump` output (table-tested). The dump is a
// TSV where every line is prefixed with its interface name. An INTERFACE line has 5 fields
// (iface, private-key, public-key, listen-port, fwmark); a PEER line has 9 (iface, public-key,
// preshared-key, endpoint, allowed-ips, latest-handshake, rx, tx, persistent-keepalive) — so the
// latest-handshake UNIX epoch is field index 5 ("0" = never handshaked).
//
//	zero interfaces                      -> NoInterfaces       (warn)
//	>=1 iface, a peer never handshaked   -> LinkDown           (warn)
//	a peer's handshake older than stale  -> PeerHandshakeStale (warn)
//	all peers fresh                      -> AllPeersUp         (ok)
//	>=1 iface but zero peers             -> "" (no condition — nothing meaningful)
func classifyWGDump(dump []byte, now time.Time) (reason, status, msg string) {
	var interfaces, peers, never, stale int
	for _, line := range strings.Split(string(dump), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		switch {
		case len(fields) == 5: // interface header line
			interfaces++
		case len(fields) >= 9: // peer line; latest-handshake at index 5
			peers++
			hs, err := strconv.ParseInt(fields[5], 10, 64)
			if err != nil || hs == 0 {
				never++
				continue
			}
			if now.Sub(time.Unix(hs, 0)) > wgHandshakeStaleAfter {
				stale++
			}
		}
	}
	switch {
	case interfaces == 0:
		return reasonWGNoInterfaces, model.ConditionStatusWarn, "no WireGuard interfaces up"
	case peers == 0:
		return "", "", "" // interfaces but no peers: nothing meaningful to flag
	case never > 0:
		return reasonWGLinkDown, model.ConditionStatusWarn,
			strconv.Itoa(never) + "/" + strconv.Itoa(peers) + " peers never handshaked"
	case stale > 0:
		return reasonWGPeerHandshakeStale, model.ConditionStatusWarn,
			strconv.Itoa(stale) + "/" + strconv.Itoa(peers) + " peers stale (handshake aged)"
	default:
		return reasonWGAllPeersUp, model.ConditionStatusOK,
			strconv.Itoa(peers) + "/" + strconv.Itoa(peers) + " peers up"
	}
}
