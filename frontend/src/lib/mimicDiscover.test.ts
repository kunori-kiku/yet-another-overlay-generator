// mimicDiscover.test.ts — the pure logic behind the plan-4 mimic-catalog discover checklist.
// Node-env (no jsdom): we test deriveKey (label prefill) + collidingKeys (the duplicate-label guard
// that blocks "Add selected"), the two decisions that must be correct for the checklist to avoid a
// silent pin loss.

import { describe, expect, it } from 'vitest';
import { deriveKey, collidingKeys } from './mimicDiscover';

describe('deriveKey', () => {
  it('derives <codename>-<arch> from a <codename>_mimic_<...>_<arch>.deb name', () => {
    expect(deriveKey('bookworm_mimic_1.4.0_amd64.deb')).toBe('bookworm-amd64');
    expect(deriveKey('jammy_mimic_1.4.0-1_arm64.deb')).toBe('jammy-arm64');
  });

  it('returns blank for a dkms package (arch-independent — operator must label it)', () => {
    expect(deriveKey('bookworm_mimic-dkms_1.4.0_all.deb')).toBe('');
    expect(deriveKey('mimic-dkms_0.4.0_all.deb')).toBe('');
  });

  it('returns blank for a name that does not match the convention', () => {
    // No <codename>_ prefix (mimic-first naming), a non-.deb, and a totally unrelated asset.
    expect(deriveKey('mimic_0.4.0_amd64.deb')).toBe('');
    expect(deriveKey('checksums.txt')).toBe('');
    expect(deriveKey('mimic_1.4.0_amd64.ddeb')).toBe('');
  });
});

describe('collidingKeys', () => {
  it('returns empty when all checked keys are unique and free of existing rows', () => {
    expect(collidingKeys(['bookworm-amd64', 'jammy-arm64'], []).size).toBe(0);
  });

  it('flags two checked keys that share a label (the mislabel / -dkms collision)', () => {
    const dup = collidingKeys(['bookworm-amd64', 'bookworm-amd64'], []);
    expect(dup.has('bookworm-amd64')).toBe(true);
    expect(dup.size).toBe(1);
  });

  it('flags a checked key that collides with an EXISTING deb row (would drop its pin on save)', () => {
    const dup = collidingKeys(['bookworm-amd64'], ['bookworm-amd64']);
    expect(dup.has('bookworm-amd64')).toBe(true);
  });

  it('ignores empty/whitespace keys (reported separately as needs-a-label) and trims', () => {
    expect(collidingKeys(['', '   '], []).size).toBe(0);
    // a trimmed checked key still collides with an existing one
    expect(collidingKeys([' bookworm-amd64 '], ['bookworm-amd64']).has('bookworm-amd64')).toBe(true);
  });
});
