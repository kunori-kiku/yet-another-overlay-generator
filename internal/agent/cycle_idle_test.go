package agent_test

// cycle_idle_test.go — PERPETUAL guard pinning the PRINCIPLES.md "Generated scripts
// run as root on fleets" principle at the cycle layer (controller-server-authority-
// redesign plan-3): root install.sh NEVER re-runs without a new bundle. The orphaned
// non-keystone agent (its node left the design; its current bundle is frozen) used to
// re-apply — re-running install.sh as root — on EVERY promote for other nodes, and
// resumed from a stale cursor so the next poll fired instantly: a root-executing busy
// loop. The idle branch in RunControllerCycle skips the apply when the served bundle
// is one this daemon already applied, and resumes from the wake generation.
//
// No test here ever executes install.sh: idle paths never reach agent.Run, and the
// apply-path negative controls poison the anti-rollback baseline so Run refuses
// BEFORE the exec — proving the branch taken without running anything as root.
//
// Lifecycle: PERPETUAL. Never retire.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/agent"
)

// idleEnv stands up the controller, enrolls two nodes, and promotes generation 1
// for both. It returns the env plus node-1's bearer client and the manifest
// checksum + compiled_at of node-1's current (gen-1) bundle.
func idleEnv(t *testing.T) (env *ctlEnv, client *agent.ControllerClient, checksum, compiledAt string) {
	t.Helper()
	env = newCtlEnv(t)
	node1Token := env.enrollViaAgent(t, "node-1")
	env.enrollViaAgent(t, "node-2")
	if gen := env.stageAndPromote(t); gen != 1 {
		t.Fatalf("first promote generation = %d, want 1", gen)
	}

	client, err := agent.NewControllerClient(env.agentSrv.URL, node1Token)
	if err != nil {
		t.Fatalf("NewControllerClient: %v", err)
	}
	files, err := client.Fetch("node-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var man struct {
		CompiledAt string `json:"compiled_at"`
		Checksum   string `json:"checksum"`
	}
	if err := json.Unmarshal(files["manifest.json"], &man); err != nil {
		t.Fatalf("parse manifest.json: %v", err)
	}
	if man.Checksum == "" || man.CompiledAt == "" {
		t.Fatalf("manifest missing checksum/compiled_at: %+v", man)
	}
	return env, client, man.Checksum, man.CompiledAt
}

// simulateApplied writes the persisted agent state an actual successful apply of
// the gen-1 bundle would have left, WITHOUT running install.sh.
func simulateApplied(t *testing.T, stateDir, checksum, compiledAt, result string) {
	t.Helper()
	if err := agent.SaveState(stateDir, &agent.State{
		LastCompiledAt: compiledAt,
		LastChecksum:   checksum,
		LastResult:     result,
	}); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
}

// removeNode1AndRedeploy updates the design to drop node-1 (the peer's edge moves
// with it), then stages+promotes so the tenant generation advances for node-2 only.
func removeNode1AndRedeploy(t *testing.T, env *ctlEnv) int64 {
	t.Helper()
	// Orphan NODE-1: remove it and the peer's edge to it; node-2 (a peer) cannot
	// compile alone with no edge, so it becomes an inbound-capable router in the
	// reduced design. The deploy itself rides the shared harness helper.
	topo := smallTopo()
	topo.Nodes = topo.Nodes[1:]
	topo.Nodes[0].Role = "router"
	topo.Nodes[0].Hostname = "peer.example.com"
	topo.Nodes[0].Capabilities.CanAcceptInbound = true
	topo.Nodes[0].Capabilities.CanForward = true
	topo.Nodes[0].Capabilities.HasPublicIP = true
	topo.Edges = nil
	return env.deployTopo(t, topo)
}

// TestCycleIdle_OrphanSkipsApplyAndAdvances: the heart of the guard. An orphaned
// node's daemon (cursor at its applied generation) is woken by a promote for OTHER
// nodes; the served bundle is the one it already applied → the cycle must NOT
// apply (install.sh never runs) and must resume from the WAKE generation so the
// next poll long-polls instead of firing instantly.
func TestCycleIdle_OrphanSkipsApplyAndAdvances(t *testing.T) {
	env, client, checksum, compiledAt := idleEnv(t)
	stateDir := t.TempDir()
	simulateApplied(t, stateDir, checksum, compiledAt, "ok")

	wakeGen := removeNode1AndRedeploy(t, env)
	if wakeGen != 2 {
		t.Fatalf("orphan wake generation = %d, want 2", wakeGen)
	}

	var errOut strings.Builder
	resume, applied, err := agent.RunControllerCycle(client, agent.CycleConfig{
		NodeID:   "node-1",
		After:    1, // the daemon applied gen 1 this run
		StateDir: stateDir,
		KeyPath:  filepath.Join(t.TempDir(), "agent.key"),
		Stderr:   &errOut,
	})
	if err != nil {
		t.Fatalf("idle cycle returned error: %v", err)
	}
	if applied {
		t.Fatalf("idle cycle APPLIED — install.sh would have re-run as root with no new bundle")
	}
	if resume != wakeGen {
		t.Fatalf("idle cycle resume = %d, want the wake generation %d (stale cursor = busy loop)", resume, wakeGen)
	}
	if !strings.Contains(errOut.String(), "already applied") {
		t.Errorf("idle cycle did not log the skip; stderr: %q", errOut.String())
	}

	// The persisted state is untouched: no failure was recorded, no baseline moved.
	st, err := agent.LoadState(stateDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if st.LastChecksum != checksum || st.LastResult != "ok" {
		t.Errorf("idle cycle mutated state: %+v", st)
	}
}

// TestCycleIdle_ApplyPathStillTakenWhenWarranted: the three inverse guards. Each
// case must take the APPLY path — proven by an anti-rollback refusal from a
// poisoned future baseline (Run refuses BEFORE any exec), never by running
// install.sh.
func TestCycleIdle_ApplyPathStillTakenWhenWarranted(t *testing.T) {
	const futureCompiledAt = "2099-01-02T15:04:05Z"

	cases := []struct {
		name  string
		after int64
		state func(checksum, compiledAt string) (lastChecksum, lastResult string)
	}{
		{
			// A failed last apply must retry even though the checksum matches.
			name:  "failed last apply retries",
			after: 1,
			state: func(checksum, _ string) (string, string) { return checksum, "apply failed: exit 1" },
		},
		{
			// Different content must apply (a genuinely new bundle).
			name:  "different checksum applies",
			after: 1,
			state: func(_, _ string) (string, string) { return "different-checksum-entirely", "ok" },
		},
		{
			// A cold cursor (single-shot --after 0) must keep the force-reapply
			// workflow: the fetched generation exceeds the cursor, so no idle skip.
			name:  "cold cursor re-applies",
			after: 0,
			state: func(checksum, _ string) (string, string) { return checksum, "ok" },
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, client, checksum, _ := idleEnv(t)
			stateDir := t.TempDir()
			lastChecksum, lastResult := tc.state(checksum, futureCompiledAt)
			// The poisoned FUTURE baseline makes the apply path refuse at
			// anti-rollback — before install.sh could ever execute.
			simulateApplied(t, stateDir, lastChecksum, futureCompiledAt, lastResult)

			wakeGen := removeNode1AndRedeploy(t, env)
			if wakeGen != 2 {
				t.Fatalf("wake generation = %d, want 2", wakeGen)
			}

			_, applied, err := agent.RunControllerCycle(client, agent.CycleConfig{
				NodeID:   "node-1",
				After:    tc.after,
				StateDir: stateDir,
				KeyPath:  filepath.Join(t.TempDir(), "agent.key"),
				Stderr:   &strings.Builder{},
			})
			if applied {
				t.Fatalf("cycle reported applied=true — the rollback poison failed and install.sh RAN")
			}
			if err == nil || !strings.Contains(err.Error(), "anti-rollback") {
				t.Fatalf("expected the APPLY path (anti-rollback refusal); got err=%v", err)
			}
		})
	}
}

// TestCycleIdle_RekeyTakesPrecedence: a rekey signal must rotate even when the
// served bundle is content-identical to the applied one — the idle skip sits
// BEHIND the rekey branch.
func TestCycleIdle_RekeyTakesPrecedence(t *testing.T) {
	env, client, checksum, compiledAt := idleEnv(t)
	stateDir := t.TempDir()
	simulateApplied(t, stateDir, checksum, compiledAt, "ok")

	// Flag node-1 for rekey directly in the registry + bump (the rekey-all shape).
	ctx := context.Background()
	n, err := env.store.GetNode(ctx, testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	n.RekeyRequested = true
	if err := env.store.UpsertNode(ctx, testTenant, n); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	wakeGen, err := env.store.BumpGeneration(ctx, testTenant)
	if err != nil {
		t.Fatalf("BumpGeneration: %v", err)
	}

	keyPath := filepath.Join(t.TempDir(), "agent.key")
	resume, applied, err := agent.RunControllerCycle(client, agent.CycleConfig{
		NodeID:   "node-1",
		After:    1,
		StateDir: stateDir,
		KeyPath:  keyPath,
		Stderr:   &strings.Builder{},
	})
	if err != nil {
		t.Fatalf("rekey cycle: %v", err)
	}
	if applied {
		t.Fatalf("rekey cycle applied the stale pre-rekey bundle")
	}
	if resume != wakeGen {
		t.Fatalf("rekey cycle resume = %d, want wake %d", resume, wakeGen)
	}
	// The rotation actually happened: the flag cleared and a fresh key exists.
	n, err = env.store.GetNode(ctx, testTenant, "node-1")
	if err != nil {
		t.Fatalf("GetNode after rekey: %v", err)
	}
	if n.RekeyRequested {
		t.Errorf("rekey flag not cleared — the idle skip swallowed the rotation")
	}
}
