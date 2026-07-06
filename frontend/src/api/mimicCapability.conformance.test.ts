import { describe, it, expect } from 'vitest';
import { mapMimicCapability } from './controllerClient';

// mimic-runtime-reliability plan-3: the agent's "can this node run mimic" heuristic
// (telemetry.mimic_capability) → MimicCapability at the wire boundary. Defensive: a missing metric or
// an out-of-set capability yields undefined (the panel renders no warning); kernel defaults to "".

describe('mapMimicCapability', () => {
  it('maps each valid capability with kernel', () => {
    expect(mapMimicCapability({ capability: 'ready', kernel: '6.1.0-40-cloud-amd64' })).toEqual({
      capability: 'ready',
      kernel: '6.1.0-40-cloud-amd64',
    });
    expect(mapMimicCapability({ capability: 'unbuildable', kernel: '6.1.0-13-cloud-amd64' })).toEqual({
      capability: 'unbuildable',
      kernel: '6.1.0-13-cloud-amd64',
    });
    expect(mapMimicCapability({ capability: 'buildable' })).toEqual({ capability: 'buildable', kernel: '' });
  });

  it('returns undefined for a missing metric or an out-of-set capability (never a phantom warning)', () => {
    expect(mapMimicCapability(undefined)).toBeUndefined();
    expect(mapMimicCapability({})).toBeUndefined();
    expect(mapMimicCapability({ capability: '' })).toBeUndefined();
    expect(mapMimicCapability({ capability: 'bogus', kernel: 'x' })).toBeUndefined();
  });
});
