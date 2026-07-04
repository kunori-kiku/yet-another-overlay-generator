// mimicDiscover.test.ts — the pure logic behind the mimic-catalog discover checklist (plan-4;
// two-package mimic-provisioning-reliability plan-2). Node-env (no jsdom): we test deriveKey (label
// prefill for BOTH the mimic and the mimic-dkms companion), deriveSlot (which package an asset is),
// and collidingKeys (the slot-aware duplicate-label guard) — the decisions that must be correct so
// the checklist pairs a mimic + mimic-dkms into ONE row without a silent pin loss.

import { describe, expect, it } from 'vitest';
import { deriveKey, deriveSlot, collidingKeys } from './mimicDiscover';

describe('deriveKey', () => {
  it('derives <codename>-<arch> from a <codename>_mimic_<...>_<arch>.deb name', () => {
    expect(deriveKey('bookworm_mimic_1.4.0_amd64.deb')).toBe('bookworm-amd64');
    expect(deriveKey('jammy_mimic_1.4.0-1_arm64.deb')).toBe('jammy-arm64');
  });

  it('derives the SAME <codename>-<arch> for the mimic-dkms companion (so it pairs into one row)', () => {
    expect(deriveKey('bookworm_mimic-dkms_0.7.1-1_amd64.deb')).toBe('bookworm-amd64');
    expect(deriveKey('noble_mimic-dkms_0.7.1-1_arm64.deb')).toBe('noble-arm64');
  });

  it('returns blank for a name that does not match the convention', () => {
    expect(deriveKey('mimic_0.4.0_amd64.deb')).toBe(''); // no <codename>_ prefix
    expect(deriveKey('checksums.txt')).toBe('');
    expect(deriveKey('mimic_1.4.0_amd64.ddeb')).toBe('');
    expect(deriveKey('bookworm_mimic-dbgsym_0.7.1-1_amd64.deb')).toBe(''); // neither mimic nor mimic-dkms
  });
});

describe('deriveSlot', () => {
  it('is dkms for a mimic-dkms asset, mimic otherwise', () => {
    expect(deriveSlot('bookworm_mimic-dkms_0.7.1-1_amd64.deb')).toBe('dkms');
    expect(deriveSlot('bookworm_mimic_0.7.1-1_amd64.deb')).toBe('mimic');
  });
});

describe('collidingKeys', () => {
  it('returns empty when checked (key, slot) pairs are all unique and free of existing rows', () => {
    expect(
      collidingKeys(
        [
          { key: 'bookworm-amd64', slot: 'mimic' },
          { key: 'jammy-arm64', slot: 'mimic' },
        ],
        [],
      ).size,
    ).toBe(0);
  });

  it('does NOT flag a mimic + dkms pair sharing one label (different slots — they pair into one row)', () => {
    const dup = collidingKeys(
      [
        { key: 'bookworm-amd64', slot: 'mimic' },
        { key: 'bookworm-amd64', slot: 'dkms' },
      ],
      [],
    );
    expect(dup.size).toBe(0);
  });

  it('flags two SAME-slot assets under one label (a mislabel that would drop a pin on save)', () => {
    const dup = collidingKeys(
      [
        { key: 'bookworm-amd64', slot: 'mimic' },
        { key: 'bookworm-amd64', slot: 'mimic' },
      ],
      [],
    );
    expect(dup.has('bookworm-amd64')).toBe(true);
    expect(dup.size).toBe(1);
  });

  it('flags a checked slot already occupied by an EXISTING row, but not an empty slot', () => {
    const existing = [{ key: 'bookworm-amd64', hasMimic: true, hasDkms: false }];
    // re-adding the occupied mimic slot collides:
    expect(collidingKeys([{ key: 'bookworm-amd64', slot: 'mimic' }], existing).has('bookworm-amd64')).toBe(true);
    // adding the empty dkms slot does NOT collide — it fills the companion:
    expect(collidingKeys([{ key: 'bookworm-amd64', slot: 'dkms' }], existing).size).toBe(0);
  });

  it('ignores empty/whitespace keys (reported separately as needs-a-label) and trims', () => {
    expect(
      collidingKeys(
        [
          { key: '', slot: 'mimic' },
          { key: '   ', slot: 'mimic' },
        ],
        [],
      ).size,
    ).toBe(0);
    expect(
      collidingKeys([{ key: ' bookworm-amd64 ', slot: 'mimic' }], [{ key: 'bookworm-amd64', hasMimic: true, hasDkms: false }]).has(
        'bookworm-amd64',
      ),
    ).toBe(true);
  });
});
