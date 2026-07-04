import { describe, it, expect } from 'vitest';
import { toSettingsJSON, mapSettings, emptyControllerSettings, type MimicDebPin } from './controllerClient';

// two-package mimic-provisioning-reliability plan-2: a mimic catalog row pins the userspace `mimic`
// .deb AND its `mimic-dkms` companion. These pin the wire round-trip (toSettingsJSON <-> mapSettings)
// for the companion fields (camelCase dkmsAsset/dkmsSha256 <-> snake_case dkms_asset/dkms_sha256) and
// the legacy mimic-only back-compat (no phantom companion; byte-clean wire).

const SHA_A = 'a'.repeat(64);
const SHA_B = 'b'.repeat(64);

describe('mimicDebs two-package settings round-trip', () => {
  it('round-trips a full mimic + dkms companion pin', () => {
    const pin: MimicDebPin = {
      asset: 'bookworm_mimic_0.7.1-1_amd64.deb',
      sha256: SHA_A,
      dkmsAsset: 'bookworm_mimic-dkms_0.7.1-1_amd64.deb',
      dkmsSha256: SHA_B,
    };
    const s = { ...emptyControllerSettings(), mimicDebs: { 'bookworm-amd64': pin } };
    expect(mapSettings(toSettingsJSON(s)).mimicDebs['bookworm-amd64']).toEqual(pin);
  });

  it('a legacy mimic-only pin round-trips with the dkms fields absent (undefined)', () => {
    const pin: MimicDebPin = { asset: 'bookworm_mimic_0.7.1-1_amd64.deb', sha256: SHA_A };
    const s = { ...emptyControllerSettings(), mimicDebs: { 'bookworm-amd64': pin } };
    const back = mapSettings(toSettingsJSON(s)).mimicDebs['bookworm-amd64'];
    expect(back.asset).toBe(pin.asset);
    expect(back.sha256).toBe(pin.sha256);
    expect(back.dkmsAsset).toBeUndefined();
    expect(back.dkmsSha256).toBeUndefined();
  });

  it('does not emit dkms_* wire keys for a mimic-only pin (byte-clean, old-reader back-compat)', () => {
    const s = { ...emptyControllerSettings(), mimicDebs: { 'bookworm-amd64': { asset: 'm.deb', sha256: SHA_A } } };
    const entry = toSettingsJSON(s).mimic_debs!['bookworm-amd64'];
    expect('dkms_asset' in entry).toBe(false);
    expect('dkms_sha256' in entry).toBe(false);
  });

  it('an empty dkms_* string on the wire maps to undefined (not a phantom companion)', () => {
    const wire = {
      ...toSettingsJSON(emptyControllerSettings()),
      mimic_debs: { 'bookworm-amd64': { asset: 'm.deb', sha256: SHA_A, dkms_asset: '', dkms_sha256: '' } },
    };
    const back = mapSettings(wire).mimicDebs['bookworm-amd64'];
    expect(back.dkmsAsset).toBeUndefined();
    expect(back.dkmsSha256).toBeUndefined();
  });
});
