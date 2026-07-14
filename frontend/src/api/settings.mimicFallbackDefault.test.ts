import { describe, it, expect } from 'vitest';
import { toSettingsJSON, mapSettings, emptyControllerSettings } from './controllerClient';

// plan-6: the fleet-wide mimicFallbackDefault rides the full-replace /settings contract. These pin the
// wire round-trip (toSettingsJSON -> mapSettings) for each tri-state value + the old-settings default.

describe('mimicFallbackDefault settings round-trip', () => {
  for (const v of ['', 'udp', 'none']) {
    it(`round-trips ${v || '(empty)'}`, () => {
      const s = { ...emptyControllerSettings(), mimicFallbackDefault: v };
      expect(mapSettings(toSettingsJSON(s)).mimicFallbackDefault).toBe(v);
    });
  }

  it('absent mimic_fallback_default maps to "" (old saved settings back-compat)', () => {
    const wire = { ...toSettingsJSON(emptyControllerSettings()) };
    delete wire.mimic_fallback_default;
    expect(mapSettings(wire).mimicFallbackDefault).toBe('');
  });

  it('full-replace: the new field does not narrow the contract (siblings survive)', () => {
    const s = {
      ...emptyControllerSettings(),
      mimicFallbackDefault: 'udp',
      agentReleaseBaseURL: 'https://example/dl',
      mimicVersion: 'v1.4.0',
    };
    const back = mapSettings(toSettingsJSON(s));
    expect(back.mimicFallbackDefault).toBe('udp');
    expect(back.agentReleaseBaseURL).toBe('https://example/dl');
    expect(back.mimicVersion).toBe('v1.4.0');
  });
});
