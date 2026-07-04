import { describe, it, expect } from 'vitest';
import { mapNativeXDP } from './controllerClient';

// mimic-provisioning plan-4: the agent's egress-NIC native-XDP capability heuristic
// (telemetry.native_xdp) → NativeXDP at the wire boundary. Defensive: a missing metric or an
// out-of-set capability yields undefined (the panel renders no hint), driver/kernel default to "".

describe('mapNativeXDP', () => {
  it('maps each valid capability with driver + kernel', () => {
    expect(mapNativeXDP({ capability: 'supported', driver: 'ena', kernel: '6.1.0' })).toEqual({
      capability: 'supported',
      driver: 'ena',
      kernel: '6.1.0',
    });
    expect(mapNativeXDP({ capability: 'unsupported', driver: 'e1000', kernel: '5.10.0' })).toEqual({
      capability: 'unsupported',
      driver: 'e1000',
      kernel: '5.10.0',
    });
    // Absent driver/kernel default to "".
    expect(mapNativeXDP({ capability: 'conditional' })).toEqual({ capability: 'conditional', driver: '', kernel: '' });
  });

  it('returns undefined for a missing metric or an out-of-set capability (never a phantom hint)', () => {
    expect(mapNativeXDP(undefined)).toBeUndefined();
    expect(mapNativeXDP({})).toBeUndefined();
    expect(mapNativeXDP({ capability: '' })).toBeUndefined();
    expect(mapNativeXDP({ capability: 'bogus', driver: 'x' })).toBeUndefined();
  });
});
