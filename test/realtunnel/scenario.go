//go:build linux && integration

package realtunnel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/naming"
)

// scenario.go — Phases 4-6: wire a topology's nodes into per-node systemd-nspawn containers on a
// shared underlay bridge, rewrite the shipped TEST-NET endpoints to the reachable underlay, run the
// UNMODIFIED install.sh in each (Option B), and assert the overlay works on the kernel.
//
// Underlay vs overlay: the bridge subnet (underlaySubnet) is the "internet" the WireGuard Endpoints
// dial; it is deliberately disjoint from EVERY fixture's overlay/transit CIDRs (10.10–10.48, e.g.
// simple-mesh 10.11 + transit 10.10, nat-hub 10.20, relay 10.30, c3 10.40, c4 10.48) so a route to
// an overlay IP can ONLY exist because babel converged it over a formed tunnel — never because of
// the underlay.
const underlaySubnet = "10.123.0" // node i -> 10.123.0.(i+1)/24 on the bridge

// rtNode is a node under test: its identity + role + the allocated overlay IP (from the compiler) +
// the assigned underlay IP + its bundle directory + the running container.
type rtNode struct {
	id, name   string
	role       string // compiler role (router/relay/gateway/peer/client) — drives reachability predicates
	overlayIP  string // compiler-allocated dummy0 address (the convergence/ping target)
	underlayIP string // bridge address the WG Endpoint dials
	dir        string // host path of this node's exported bundle (bind-mounted into the container)
	c          *container
}

// reachPredicate decides whether overlay traffic from one node to another is EXPECTED to route, so
// the all-pairs convergence/ping assertions assert only the connectivity a topology actually
// provides. Most YAOG topologies are fully reachable (per-peer AllowedIPs are 0.0.0.0/0 with Table=
// off and babel owning the routing table, so even a router hub forwards spoke-to-spoke), so allPairs
// is the common case; a scenario that deliberately withholds a path supplies its own predicate.
type reachPredicate func(from, to *rtNode) bool

// allPairs expects every ordered node pair to be reachable over the overlay.
func allPairs(_, _ *rtNode) bool { return true }

// scenario is a brought-up topology: the underlay bridge + the running node containers.
type scenario struct {
	bridge string
	nodes  []*rtNode
}

// assignUnderlay maps each node id to a distinct underlay IP on the bridge subnet, by node order.
func assignUnderlay(topo model.Topology) map[string]string {
	m := make(map[string]string, len(topo.Nodes))
	for i, n := range topo.Nodes {
		m[n.ID] = fmt.Sprintf("%s.%d", underlaySubnet, i+1)
	}
	return m
}

// rewriteEndpoints rewrites every edge's endpoint_host to the underlay IP of the node it dials (the
// to-node) and every node's public_endpoints[].host to that node's own underlay IP — BEFORE compile,
// so the generated WG `Endpoint = host:port` dials the reachable bridge address instead of the
// shipped (unroutable) TEST-NET address. The compiler-allocated port is preserved (host only).
func rewriteEndpoints(topo *model.Topology, underlay map[string]string) {
	for i := range topo.Edges {
		e := &topo.Edges[i]
		if e.EndpointHost == "" {
			continue
		}
		if ip, ok := underlay[e.ToNodeID]; ok {
			e.EndpointHost = ip
		}
	}
	for i := range topo.Nodes {
		n := &topo.Nodes[i]
		ip, ok := underlay[n.ID]
		if !ok {
			continue
		}
		for j := range n.PublicEndpoints {
			n.PublicEndpoints[j].Host = ip
		}
	}
}

// bringUp brings up topoPath as a scenario: rewrite endpoints, compile+export the real bundle, create
// the underlay bridge, boot a container per node (bundle bound read-only), assign its underlay IP,
// verify raw underlay reachability (BEFORE any WG — distinguishes an underlay miswire from a config
// bug, R3), then run the UNMODIFIED install.sh in each. Returns the running scenario.
func bringUp(t *testing.T, rootfs, topoPath string) *scenario {
	t.Helper()
	topo := loadTopology(t, topoPath)
	underlay := assignUnderlay(topo)
	rewriteEndpoints(&topo, underlay)

	out := t.TempDir()
	bundle := produceBundle(t, topo, out)
	bundle.requireBundleFiles(t) // oracle integrity: the bundle we run is the bundle that ships

	bridge := bridgeName()
	run(t, "ip", "link", "add", bridge, "type", "bridge")
	run(t, "ip", "link", "set", bridge, "up")
	t.Cleanup(func() { _, _ = tryRun("ip", "link", "del", bridge) })

	sc := &scenario{bridge: bridge}
	for i := range bundle.result.Topology.Nodes {
		n := bundle.result.Topology.Nodes[i]
		nd := &rtNode{
			id:         n.ID,
			name:       n.Name,
			role:       n.Role,
			overlayIP:  n.OverlayIP,
			underlayIP: underlay[n.ID],
			// Export directories are keyed exclusively by the portable node ID. A
			// display name is not a filesystem identity and may differ (as it does
			// in simple-mesh), so binding the name-keyed path makes nspawn exit
			// before machine registration because the host source does not exist.
			dir: filepath.Join(out, n.ID),
		}
		nd.c = bootContainer(t, rootfs, machineName(n.Name), bootOpts{
			bridge: bridge,
			binds:  []string{nd.dir + ":/opt/yaog-bundle"},
		})
		// nspawn's veth end inside the container is host0; assign the underlay IP + bring it up.
		nd.c.exec(t, "ip", "addr", "add", nd.underlayIP+"/24", "dev", "host0")
		nd.c.exec(t, "ip", "link", "set", "host0", "up")
		sc.nodes = append(sc.nodes, nd)
	}

	sc.requireUnderlayReachable(t)

	// Activation: run the UNMODIFIED install.sh (Option B) — it installs configs, brings up dummy0 +
	// each wg-quick interface, applies sysctl, installs the SNAT rule, and enables+starts babeld, all
	// via the container's real systemd.
	for _, nd := range sc.nodes {
		nd.c.exec(t, "bash", "-c", "cd /opt/yaog-bundle && bash install.sh")
	}
	return sc
}

// requireUnderlayReachable pings every other node's underlay IP from each node (R3): the bridge L2 is
// correct BEFORE any WireGuard is involved, so a later failure is a config bug, not a miswire.
func (sc *scenario) requireUnderlayReachable(t *testing.T) {
	t.Helper()
	for _, from := range sc.nodes {
		for _, to := range sc.nodes {
			if to.id == from.id {
				continue
			}
			waitFor(t, 15*time.Second, fmt.Sprintf("underlay %s->%s (%s)", from.name, to.name, to.underlayIP), func() bool {
				_, err := from.c.tryExec("ping", "-c", "1", "-W", "1", to.underlayIP)
				return err == nil
			})
		}
	}
}

// requireHandshakes (Phase 6a, REQUIRED) polls every WG interface on every node until it reports a
// non-zero latest-handshake — the tunnels formed.
func (sc *scenario) requireHandshakes(t *testing.T) {
	t.Helper()
	for _, nd := range sc.nodes {
		for _, iface := range sc.wgInterfaces(t, nd) {
			waitFor(t, 30*time.Second, fmt.Sprintf("handshake on %s/%s", nd.name, iface), func() bool {
				out, err := nd.c.tryExec("wg", "show", iface, "latest-handshakes")
				if err != nil {
					return false
				}
				for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
					f := strings.Fields(line)
					if len(f) == 2 && f[1] != "0" && f[1] != "" {
						return true
					}
				}
				return false
			})
		}
	}
}

// requireRouteConvergence (Phase 6b, REQUIRED) polls each node's kernel routing table for a route to
// every reachable other node's OverlayIP/32 — babel converged the AnnounceSelf redistribute over the
// tunnels. The reach predicate selects which ordered pairs the topology is expected to connect.
func (sc *scenario) requireRouteConvergence(t *testing.T, reach reachPredicate) {
	t.Helper()
	for _, from := range sc.nodes {
		for _, to := range sc.nodes {
			if to.id == from.id || !reach(from, to) {
				continue
			}
			waitFor(t, 60*time.Second, fmt.Sprintf("route %s->%s/32 (%s)", from.name, to.name, to.overlayIP), func() bool {
				out, err := from.c.tryExec("ip", "route", "get", to.overlayIP)
				if err != nil {
					return false
				}
				// "unreachable" / "via" without a wg dev means not converged; a wg-dev route is good.
				return strings.Contains(out, "dev wg-") && !strings.Contains(out, "unreachable")
			})
		}
	}
}

// requireOverlayPing (Phase 6c, REQUIRED) pings every reachable other node's overlay IP from each
// node's overlay IP and requires 0% loss — the generated overlay actually routes packets end to end.
// The reach predicate selects which ordered pairs the topology is expected to connect.
func (sc *scenario) requireOverlayPing(t *testing.T, reach reachPredicate) {
	t.Helper()
	for _, from := range sc.nodes {
		for _, to := range sc.nodes {
			if to.id == from.id || !reach(from, to) {
				continue
			}
			out := from.c.exec(t, "ping", "-c", "3", "-W", "2", "-I", from.overlayIP, to.overlayIP)
			if !strings.Contains(out, " 0% packet loss") {
				t.Fatalf("overlay ping %s(%s)->%s(%s) lost packets:\n%s", from.name, from.overlayIP, to.name, to.overlayIP, out)
			}
		}
	}
}

// requireSNATRewrite (Phase 6d, REQUIRED floor) asserts the overlay-SNAT rule is installed on each
// node AND functionally rewrites transit-sourced traffic to the overlay source. The functional proof:
// ping another node's overlay IP sourced from THIS node's transit IP. Transit IPs are allocated /32
// (no shared subnet), so a transit-sourced packet's reply is routable back ONLY if egress SNAT
// rewrote the source to the babel-announced overlay IP — without the rewrite the target replies to a
// /32 transit address it has no route to, and the ping is lost. 0% loss therefore proves the rewrite
// fired (the unique-to-netns data-plane check the byte/agent/UI layers structurally cannot see). The
// probe POLLS: SNAT-carried delivery needs the overlay route to have converged first, so a single
// shot can lose to timing on the first node even though the rewrite is correct.
func (sc *scenario) requireSNATRewrite(t *testing.T) {
	t.Helper()
	for _, nd := range sc.nodes {
		// Rule presence (nft or iptables path, whichever install.sh chose).
		ruleset, _ := nd.c.tryExec("sh", "-c", "nft list ruleset 2>/dev/null; iptables-save -t nat 2>/dev/null")
		if !strings.Contains(ruleset, "snat") && !strings.Contains(ruleset, "SNAT") {
			t.Fatalf("node %s: no overlay-SNAT rule installed:\n%s", nd.name, ruleset)
		}
	}
	for _, from := range sc.nodes {
		transit := sc.aTransitIP(t, from)
		if transit == "" {
			t.Fatalf("node %s: no transit IP found on a wg interface", from.name)
		}
		to := sc.otherNode(t, from)
		waitFor(t, 60*time.Second, fmt.Sprintf("SNAT rewrite %s(transit %s)->%s(%s)", from.name, transit, to.name, to.overlayIP), func() bool {
			ok, _ := sc.snatFunctionalOK(t, from, transit, to)
			return ok
		})
	}
}

// snatFunctionalOK pings `to`'s overlay IP from `from` sourced at `transit` and reports whether the
// reply made it back (0% loss). It is NON-fatal (ping exits non-zero on loss) because it is shared by
// requireSNATRewrite (which polls until it returns true) and the negative proof (which fails when,
// after the drop-snat fault, it still returns true — proving the assertion has teeth).
func (sc *scenario) snatFunctionalOK(t *testing.T, from *rtNode, transit string, to *rtNode) (bool, string) {
	t.Helper()
	out, err := from.c.tryExec("ping", "-c", "3", "-W", "2", "-I", transit, to.overlayIP)
	if err != nil {
		return false, out
	}
	return strings.Contains(out, " 0% packet loss"), out
}

// applyFault deliberately breaks a wire on every node (the negative-proof injector). The only fault
// is `drop-snat`: remove the overlay-SNAT rule so the transit->overlay source rewrite no longer
// happens — exactly the data-plane defect requireSNATRewrite must catch. Unknown faults are fatal so
// a typo in REALTUNNEL_NEGATIVE never silently runs a no-op (vacuously "passing") red-proof.
func (sc *scenario) applyFault(t *testing.T, fault string) {
	t.Helper()
	switch fault {
	case "drop-snat":
		for _, nd := range sc.nodes {
			// Remove both possible rule homes (install.sh chose one); best-effort each.
			nd.c.tryExec("sh", "-c", "nft flush ruleset 2>/dev/null; iptables -t nat -F 2>/dev/null; true")
		}
	default:
		t.Fatalf("realtunnel: unknown REALTUNNEL_NEGATIVE fault %q (supported: drop-snat)", fault)
	}
}

// reverseEndpointPresent reports whether nodeName's rendered WireGuard config for the peer peerName
// carries an `Endpoint =` line. It reads the exported bundle (not the kernel), so it is a
// deterministic, race-free assertion on the compiler's endpoint resolution — the C3
// reverse-fallback contract and the C4 link-direction suppression contract both pin on it.
// The per-peer config file is named `<InterfaceName>.conf`, where the interface name is resolved via
// the single naming authority (naming.WgInterfaceName) rather than string-concatenating "wg-"+peer —
// so a long or non-lowercase peer name (which the renderer hashes/sanitizes) still maps to the right
// file. (The C3/C4 fixtures use only primary, non-backup links, so WgInterfaceName is exact here.)
func (sc *scenario) reverseEndpointPresent(t *testing.T, nodeName, peerName string) bool {
	t.Helper()
	var nd *rtNode
	for _, n := range sc.nodes {
		if n.name == nodeName {
			nd = n
			break
		}
	}
	if nd == nil {
		t.Fatalf("reverseEndpointPresent: no node named %q in scenario", nodeName)
	}
	path := filepath.Join(nd.dir, "wireguard", naming.WgInterfaceName(peerName)+".conf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reverseEndpointPresent: read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Endpoint =") {
			return true
		}
	}
	return false
}

// wgInterfaces returns the node's active WireGuard interface names (`wg show interfaces`).
func (sc *scenario) wgInterfaces(t *testing.T, nd *rtNode) []string {
	t.Helper()
	out := nd.c.exec(t, "wg", "show", "interfaces")
	ifaces := strings.Fields(strings.TrimSpace(out))
	if len(ifaces) == 0 {
		t.Fatalf("node %s has no WireGuard interfaces up after install.sh", nd.name)
	}
	return ifaces
}

// aTransitIP returns one transit IP (10.10.0.x) assigned to a wg-* interface on the node.
func (sc *scenario) aTransitIP(t *testing.T, nd *rtNode) string {
	t.Helper()
	out := nd.c.exec(t, "sh", "-c", "ip -4 -o addr show | grep ' wg-' || true")
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "inet "); i >= 0 {
			rest := strings.Fields(line[i+len("inet "):])
			if len(rest) > 0 {
				return strings.SplitN(rest[0], "/", 2)[0]
			}
		}
	}
	return ""
}

// otherNode returns any node distinct from nd, fataling if the scenario has no second node (every
// caller needs a peer to probe — a single-node topology is a fixture error, not a silent nil-deref).
func (sc *scenario) otherNode(t *testing.T, nd *rtNode) *rtNode {
	t.Helper()
	for _, o := range sc.nodes {
		if o.id != nd.id {
			return o
		}
	}
	t.Fatalf("otherNode: scenario has no node distinct from %s (need >=2 nodes for the transit probe)", nd.name)
	return nil
}

// dumpDiagnostics logs the kernel/WG/babel state of every node (called on failure for forensics).
func (sc *scenario) dumpDiagnostics(t *testing.T) {
	t.Helper()
	for _, nd := range sc.nodes {
		for _, probe := range [][]string{
			{"wg", "show"},
			{"ip", "addr"},
			{"ip", "route"},
			{"sh", "-c", "iptables-save -t nat 2>/dev/null; nft list ruleset 2>/dev/null"},
			{"sh", "-c", "ip -6 route 2>/dev/null | head -40"},
		} {
			out, _ := nd.c.tryExec(probe...)
			t.Logf("=== %s: %s ===\n%s", nd.name, strings.Join(probe, " "), out)
		}
	}
}
