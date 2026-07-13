import { describe, it, expect } from 'vitest';
import { toSettingsJSON, mapSettings, emptyControllerSettings } from './controllerClient';

// telemetry-history plan-4: the per-node resource-history cap (telemetry_history_cap) rides the
// full-replace /settings contract as a nullable number mirroring the Go *int (nil ⇒ default; an
// explicit 0 ⇒ disabled; N ⇒ cap N). These pin the wire round-trip (toSettingsJSON <-> mapSettings)
// including the load-bearing null-vs-0 distinction — a 0 must survive as an explicit "disable", while
// null must be OMITTED so the server keeps its nil-pointer default.

describe('telemetryHistoryCap settings round-trip', () => {
  it('null is omitted on the wire and maps back to null (server default)', () => {
    const s = { ...emptyControllerSettings(), telemetryHistoryCap: null };
    const wire = toSettingsJSON(s);
    expect('telemetry_history_cap' in wire).toBe(false); // nil *int ⇒ absent, not 0
    expect(mapSettings(wire).telemetryHistoryCap).toBeNull();
  });

  it('0 survives as an explicit disable (present on the wire)', () => {
    const s = { ...emptyControllerSettings(), telemetryHistoryCap: 0 };
    const wire = toSettingsJSON(s);
    expect(wire.telemetry_history_cap).toBe(0);
    expect(mapSettings(wire).telemetryHistoryCap).toBe(0);
  });

  it('a positive cap round-trips verbatim', () => {
    const s = { ...emptyControllerSettings(), telemetryHistoryCap: 20160 };
    expect(mapSettings(toSettingsJSON(s)).telemetryHistoryCap).toBe(20160);
  });

  it('absent telemetry_history_cap maps to null (old saved settings back-compat)', () => {
    const wire = { ...toSettingsJSON(emptyControllerSettings()) };
    delete wire.telemetry_history_cap;
    expect(mapSettings(wire).telemetryHistoryCap).toBeNull();
  });

  it('full-replace: the new field does not narrow the contract (siblings survive)', () => {
    const s = {
      ...emptyControllerSettings(),
      telemetryHistoryCap: 0,
      mimicFallbackDefault: 'udp',
      agentReleaseBaseURL: 'https://example/dl',
    };
    const back = mapSettings(toSettingsJSON(s));
    expect(back.telemetryHistoryCap).toBe(0);
    expect(back.mimicFallbackDefault).toBe('udp');
    expect(back.agentReleaseBaseURL).toBe('https://example/dl');
  });
});
