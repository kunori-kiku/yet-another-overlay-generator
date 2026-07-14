package renderer

import (
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/compiler"
	"github.com/kunorikiku/yet-another-overlay-generator/internal/model"
)

// TestRenderDeployScripts_ClientTcpMimicTornDown closes the plan-3 review gap: a CLIENT whose sole wg0
// link is transport=="tcp" has mimic provisioned, but the client carries NO PeerInfo in peerMap (its wg0
// lives in ClientConfigs, never passed to RenderDeployScripts), so HasMimic can't be read from peers. It
// is derived from the topology edge instead — otherwise deploy-all --uninstall orphans the client's
// boot-persistent mimic@<egress> unit + root eBPF program. Only the client has SSH here, so any mimic
// teardown in the rendered output is unambiguously the client's.
func TestRenderDeployScripts_ClientTcpMimicTornDown(t *testing.T) {
	topo := &model.Topology{
		Project: model.Project{Name: "p"},
		Nodes: []model.Node{
			{ID: "relay", Name: "relay", Role: "relay", Platform: "debian"}, // no SSH → skipped by the deploy script
			{ID: "cli", Name: "cli", Role: "client", Platform: "debian", SSHAlias: "cli-a"},
		},
		Edges: []model.Edge{
			{ID: "e1", FromNodeID: "cli", ToNodeID: "relay", Transport: "tcp", IsEnabled: true},
		},
	}
	peerMap := map[string][]compiler.PeerInfo{"cli": {}} // client: no PeerInfo (wg0 lives in ClientConfigs)

	bash, ps1, err := RenderDeployScripts(topo, peerMap, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, s := range []struct{ name, script string }{{"bash", bash}, {"ps1", ps1}} {
		if !strings.Contains(s.script, `systemctl disable --now "mimic@`) {
			t.Errorf("%s: a client+tcp node's uninstall must tear down its mimic@ unit (peerMap is blind to client mimic)", s.name)
		}
	}

	// Negative control: flip the client's link to UDP → no mimic → no teardown (proves the tcp edge, not
	// something else, is what triggers it; without the fix the tcp case above would also fail — no teardown).
	topo.Edges[0].Transport = "udp"
	bashUDP, ps1UDP, err := RenderDeployScripts(topo, peerMap, nil)
	if err != nil {
		t.Fatalf("render (udp): %v", err)
	}
	for _, s := range []struct{ name, script string }{{"bash", bashUDP}, {"ps1", ps1UDP}} {
		if strings.Contains(s.script, `systemctl disable --now "mimic@`) {
			t.Errorf("%s: a client with a UDP link must NOT get a mimic teardown", s.name)
		}
	}
}
