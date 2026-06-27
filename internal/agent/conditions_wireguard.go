package agent

import (
	"context"
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
	reasonWGSomePeersDown      = "SomePeersDown" // some (not all) peers never handshaked — the node is connected, a link is down
	reasonWGLinkDown           = "LinkDown"      // NO peer has handshaked (all down, or a fresh apply) — the node's wireguard is down
	reasonWGNoInterfaces       = "NoInterfaces"
)

// wgHandshakeStaleAfter: a peer whose latest handshake is older than this is "stale" (the link may be
// flapping or the peer gone). Generous (well past a typical Babel/WG keepalive horizon) so a healthy
// idle link is never falsely flagged.
const wgHandshakeStaleAfter = 5 * time.Minute

// wgShowTimeout bounds the `wg show all dump` probe so a wedged wg/netlink can NEVER block the
// telemetry heartbeat (which runs this twice per beat). Without it, a hung probe would stall every
// subsequent beat and freeze the node's Last Seen on the controller while the daemon looks alive — a
// beta.16 "alive but frozen" smoke finding. Generous: a healthy `wg show` returns in milliseconds.
const wgShowTimeout = 10 * time.Second

// wgShowFn runs `wg show all dump` (under wgShowTimeout) and returns its stdout, indirected so a test
// can inject a fixture without a kernel WireGuard interface. A non-nil error (wg absent, not root, no
// interfaces, or the timeout) is BEST-EFFORT: the caller emits NO condition and NEVER fails the cycle.
var wgShowFn = func() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wgShowTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "wg", "show", "all", "dump").Output()
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
//	ALL peers never handshaked           -> LinkDown           (warn)  // whole-node down / fresh apply
//	SOME (not all) peers never handshaked-> SomePeersDown      (warn)  // connected; a link is down (Babel routes around)
//	a peer's handshake older than stale  -> PeerHandshakeStale (warn)
//	all peers fresh                      -> AllPeersUp         (ok)
//	>=1 iface but zero peers             -> "" (no condition — nothing meaningful)
//
// The all-vs-some split matters on a mesh: a single offline/asymmetric peer (one wg-<peer>
// interface with no handshake) must NOT flip the whole node to the alarming LinkDown when its other
// links are up — that is SomePeersDown. The per-peer detail (which link, last handshake) rides the
// telemetry metrics map (wireguardPeersSampler), surfaced as a collapsible panel.
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
	case never == peers:
		return reasonWGLinkDown, model.ConditionStatusWarn,
			"all " + strconv.Itoa(peers) + " peers down (no handshake)"
	case never > 0:
		return reasonWGSomePeersDown, model.ConditionStatusWarn,
			strconv.Itoa(never) + "/" + strconv.Itoa(peers) + " peers down (no handshake)"
	case stale > 0:
		return reasonWGPeerHandshakeStale, model.ConditionStatusWarn,
			strconv.Itoa(stale) + "/" + strconv.Itoa(peers) + " peers stale (handshake aged)"
	default:
		return reasonWGAllPeersUp, model.ConditionStatusOK,
			strconv.Itoa(peers) + "/" + strconv.Itoa(peers) + " peers up"
	}
}

// wgMetricKey is the telemetry metrics-map key carrying the per-peer WireGuard link detail.
const wgMetricKey = "wireguard_peers"

// wgPeerHealth is one WireGuard peer's live link health — the per-peer detail BEHIND the aggregate
// wireguard condition, carried on the telemetry metrics map (metrics["wireguard_peers"]) and
// rendered as a collapsible per-link panel. Peer is the link label derived from the interface name
// (`wg-<peer>` minus its prefix). NOTE: for a long remote name (>12 chars) or any backup link the
// interface is HASHED (internal/naming: wg-<clean[:8]><sha[:4]>), so the label is a stable but
// truncated tail rather than the clean peer name — Interface (always unique) + Endpoint disambiguate,
// and a readable node-name label is a controller-side follow-up (the controller knows the topology).
// LastHandshake is unix seconds (0 = never); Status is up | stale | never. No key material is exposed
// (no keys, no allowed-ips).
type wgPeerHealth struct {
	Peer          string `json:"peer"`
	Interface     string `json:"interface"`
	Endpoint      string `json:"endpoint,omitempty"`
	LastHandshake int64  `json:"last_handshake"`
	Status        string `json:"status"`
}

// samplePeers parses `wg show all dump` into the per-peer link-health list (PURE; table-tested). One
// entry per PEER line (>=9 fields); the peer name is derived from its wg-<peer> interface (the
// per-peer interface model), falling back to the interface name itself. The handshake at field 5 is
// classified up | stale | never with the same wgHandshakeStaleAfter horizon as the aggregate.
func samplePeers(dump []byte, now time.Time) []wgPeerHealth {
	var peers []wgPeerHealth
	for _, line := range strings.Split(string(dump), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 9 {
			continue // interface header (5 fields) or other; only PEER lines carry a handshake
		}
		iface := fields[0]
		endpoint := fields[3]
		if endpoint == "(none)" {
			endpoint = ""
		}
		hs, err := strconv.ParseInt(fields[5], 10, 64)
		status, last := "up", int64(0)
		switch {
		case err != nil || hs == 0:
			status = "never"
		default:
			last = hs
			if now.Sub(time.Unix(hs, 0)) > wgHandshakeStaleAfter {
				status = "stale"
			}
		}
		peers = append(peers, wgPeerHealth{
			Peer:          strings.TrimPrefix(iface, "wg-"),
			Interface:     iface,
			Endpoint:      endpoint,
			LastHandshake: last,
			Status:        status,
		})
	}
	return peers
}

// wireguardPeersSampler is the telemetry Sampler that emits the per-peer WireGuard link health into
// the metrics map (metrics["wireguard_peers"]). It is the framework's first real metric probe — a new
// monitored signal added by registering a Sampler in BuildTelemetry, riding the existing heartbeat
// with zero wire change. Best-effort: a probe error (wg absent / not root) yields no metric, never a
// cycle failure; an empty fleet (no peers) emits nothing.
type wireguardPeersSampler struct{}

func (wireguardPeersSampler) Name() string { return "wireguard-peers" }

func (wireguardPeersSampler) Sample(now time.Time) ([]model.Condition, map[string]any) {
	out, err := wgShowFn()
	if err != nil {
		return nil, nil
	}
	peers := samplePeers(out, now)
	if len(peers) == 0 {
		return nil, nil
	}
	return nil, map[string]any{wgMetricKey: peers}
}
