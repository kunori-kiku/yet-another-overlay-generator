import { describe, expect, it } from 'vitest';
import { nodeDeploymentModeUpdate } from './nodeDeploymentMode';

describe('nodeDeploymentModeUpdate', () => {
  it('clears every agent telemetry policy on manual transition and preserves them on managed', () => {
    const node = {
      telemetry_probes: [{ id: 'dns', type: 'icmp' as const, host: 'resolver.example' }],
      telemetry_devices: { mode: 'all-eligible-v1' as const },
    };

    expect(nodeDeploymentModeUpdate(node, 'manual')).toEqual({
      deployment_mode: 'manual',
      telemetry_probes: undefined,
      telemetry_devices: undefined,
    });
    expect(nodeDeploymentModeUpdate(node, 'managed')).toEqual({
      deployment_mode: undefined,
      telemetry_probes: node.telemetry_probes,
      telemetry_devices: node.telemetry_devices,
    });
  });
});
