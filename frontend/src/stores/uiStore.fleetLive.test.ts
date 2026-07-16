// @vitest-environment node

import { afterEach, describe, expect, it } from 'vitest';
import { useUiStore } from './uiStore';

afterEach(() => {
  useUiStore.setState({ fleetLive: false });
});

describe('Fleet Live shell preference', () => {
  it('is shared in memory but excluded from the persisted UI allowlist', () => {
    useUiStore.getState().setFleetLive(true);
    expect(useUiStore.getState().fleetLive).toBe(true);

    const partialize = useUiStore.persist.getOptions().partialize;
    const persisted = partialize?.(useUiStore.getState());
    expect(persisted).not.toHaveProperty('fleetLive');
  });
});
