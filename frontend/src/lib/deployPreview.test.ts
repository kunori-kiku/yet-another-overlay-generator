import { describe, it, expect } from 'vitest';
import {
  emptyForceSelection,
  setForceAll,
  toggleForceNode,
  summarizeDeployPreview,
  deployPreviewRows,
  resolveDeployForce,
  currentTelemetryPolicyUpgradeOffer,
  successorTelemetryPolicyFingerprint,
  phaseOneAgentRolloutState,
  type DeployPreview,
} from './deployPreview';

// Pure unit pins for the plan-6 preview→view mapping + the Force-selection reducer. Dependency-free
// node-env (the src/lib/**/*.test.ts vitest glob) — no React, no DOM, no store.

function preview(over: Partial<DeployPreview> = {}): DeployPreview {
  return {
    keystoneFullRestage: false,
    nodes: [
      { nodeId: 'n1', name: 'router', changed: true },
      { nodeId: 'n2', name: 'peer', changed: false },
      { nodeId: 'n3', name: 'relay', changed: false },
    ],
    skippedUnenrolled: [],
    ...over,
  };
}

describe('summarizeDeployPreview', () => {
  it('counts changed vs unchanged (the N will update / M unchanged headline)', () => {
    expect(summarizeDeployPreview(preview())).toEqual({ changed: 1, unchanged: 2, total: 3 });
  });
  it('is all-unchanged when nothing changed (the "0 changed" review-blocker case)', () => {
    const p = preview({ nodes: [{ nodeId: 'n1', name: 'router', changed: false }] });
    expect(summarizeDeployPreview(p)).toEqual({ changed: 0, unchanged: 1, total: 1 });
  });
  it('is empty for no enrolled nodes', () => {
    expect(summarizeDeployPreview(preview({ nodes: [] }))).toEqual({ changed: 0, unchanged: 0, total: 0 });
  });
});

describe('ForceSelection reducers', () => {
  it('starts empty', () => {
    const s = emptyForceSelection();
    expect(s.forceAll).toBe(false);
    expect(s.forceNodes.size).toBe(0);
  });
  it('toggleForceNode adds then removes, returning fresh objects (immutable)', () => {
    const s0 = emptyForceSelection();
    const s1 = toggleForceNode(s0, 'n2');
    expect(s1).not.toBe(s0);
    expect(s1.forceNodes.has('n2')).toBe(true);
    expect(s0.forceNodes.has('n2')).toBe(false); // original untouched
    const s2 = toggleForceNode(s1, 'n2');
    expect(s2.forceNodes.has('n2')).toBe(false);
  });
  it('setForceAll preserves per-node ticks so toggling it off restores them', () => {
    const s1 = toggleForceNode(emptyForceSelection(), 'n2');
    const s2 = setForceAll(s1, true);
    expect(s2.forceAll).toBe(true);
    expect(s2.forceNodes.has('n2')).toBe(true);
    const s3 = setForceAll(s2, false);
    expect(s3.forceAll).toBe(false);
    expect(s3.forceNodes.has('n2')).toBe(true);
  });
});

describe('successor telemetry readiness offer', () => {
  it('is invalidated by a relevant policy edit and remains separate from unrelated errors', () => {
    const withDevices = {
      nodes: [{
        id: 'n1',
        deployment_mode: 'managed' as const,
        telemetry_devices: { mode: 'all-eligible-v1' as const },
      }],
    };
    const offer = {
      error: 'upgrade agents first',
      fingerprint: successorTelemetryPolicyFingerprint(withDevices),
    };
    expect(currentTelemetryPolicyUpgradeOffer(offer, withDevices)).toBe(offer);
    expect(currentTelemetryPolicyUpgradeOffer(offer, {
      nodes: [{ id: 'n1', deployment_mode: 'managed' as const }],
    })).toBeNull();
  });
});

describe('phase-one agent rollout warning', () => {
  const base = {
    targetAgentVersion: 'v2.0.0-rc.12',
    agentBins: { 'linux-amd64': {} },
    agentCanaryNodeIds: ['n1'],
    agentRolloutFleetWide: false,
  };

  it('distinguishes an inert rollout, partial canary coverage, and fleet-wide coverage', () => {
    expect(phaseOneAgentRolloutState({ ...base, targetAgentVersion: '' }, ['n1'])).toEqual({
      kind: 'missing', uncoveredNodeIDs: ['n1'],
    });
    expect(phaseOneAgentRolloutState(base, ['n2', 'n1'])).toEqual({
      kind: 'partial', uncoveredNodeIDs: ['n2'],
    });
    expect(phaseOneAgentRolloutState({ ...base, agentRolloutFleetWide: true }, ['n2', 'n1'])).toEqual({
      kind: 'ready', uncoveredNodeIDs: [],
    });
  });
});

describe('deployPreviewRows', () => {
  it('marks a changed node willStage and NOT forceable; an unchanged node forceable', () => {
    const rows = deployPreviewRows(preview(), emptyForceSelection());
    const byId = Object.fromEntries(rows.map((r) => [r.nodeId, r]));
    expect(byId.n1.willStage).toBe(true);
    expect(byId.n1.forceable).toBe(false); // already changed → nothing to force
    expect(byId.n2.willStage).toBe(false);
    expect(byId.n2.forceable).toBe(true);
  });
  it('a per-node Force tick flips willStage on an unchanged node', () => {
    const sel = toggleForceNode(emptyForceSelection(), 'n2');
    const byId = Object.fromEntries(deployPreviewRows(preview(), sel).map((r) => [r.nodeId, r]));
    expect(byId.n2.forced).toBe(true);
    expect(byId.n2.willStage).toBe(true);
    expect(byId.n3.willStage).toBe(false); // untouched
  });
  it('Force-all stages every node and makes per-node ticks moot (not forceable)', () => {
    const sel = setForceAll(emptyForceSelection(), true);
    for (const r of deployPreviewRows(preview(), sel)) {
      expect(r.willStage).toBe(true);
      expect(r.forceable).toBe(false);
    }
  });
  it('keystoneFullRestage stages every node and disables per-node force', () => {
    const rows = deployPreviewRows(preview({ keystoneFullRestage: true }), emptyForceSelection());
    for (const r of rows) {
      expect(r.willStage).toBe(true);
      expect(r.forceable).toBe(false);
    }
  });
});

describe('resolveDeployForce', () => {
  it('nothing selected → {} (a plain delta Deploy)', () => {
    expect(resolveDeployForce(emptyForceSelection())).toEqual({});
  });
  it('force_all wins and subsumes per-node picks (never both)', () => {
    let sel = toggleForceNode(emptyForceSelection(), 'n2');
    sel = setForceAll(sel, true);
    expect(resolveDeployForce(sel)).toEqual({ forceAll: true });
  });
  it('per-node picks ride as a sorted force_nodes list', () => {
    let sel = toggleForceNode(emptyForceSelection(), 'n3');
    sel = toggleForceNode(sel, 'n2');
    expect(resolveDeployForce(sel)).toEqual({ forceNodes: ['n2', 'n3'] });
  });
});
