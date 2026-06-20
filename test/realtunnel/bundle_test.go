//go:build linux && integration

package realtunnel

import "testing"

// bundle_test.go — the oracle-integrity gate (Phase 3). It needs NO root/netns/nspawn: it only
// compiles the shipped simple-mesh fixture through the real cmd/compiler path and asserts the
// exported per-node bundle is complete and checksum-verified. This is the foundation the netns
// scenarios build on; if the bundle the test would bring up is not the bundle that ships, every
// downstream assertion is meaningless.

func TestBundleProducesVerifiedArtifacts(t *testing.T) {
	topo := loadTopology(t, repoFile(t, "examples/simple-mesh/topology.json"))
	out := t.TempDir()
	b := produceBundle(t, topo, out)
	dirs := b.requireBundleFiles(t)
	t.Logf("simple-mesh bundle: %d node dir(s) verified (%v)", len(dirs), dirs)
}
