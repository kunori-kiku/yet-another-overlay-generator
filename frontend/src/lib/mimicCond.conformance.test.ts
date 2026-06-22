import { describe, it, expect } from 'vitest';
import { REASON_LABEL, mimicStatusClass } from './mimicCond';
import { t } from '../i18n';

// plan-6: the mimic chip's reason→label + status→class logic. Pure, node-env (no jsdom). Guards the
// curated-label contract (every emitted reason renders a non-blank localized label; never a raw dump).

describe('REASON_LABEL', () => {
  // The keys MUST equal the agent classifyMimic reason codes (plan-5).
  const reasons = ['Active', 'FellBackToUDP', 'KernelTooOld', 'EbpfLoadFailed', 'InstallFailed'];
  it('maps every emitted reason to a present, non-empty EN label', () => {
    for (const r of reasons) {
      const key = REASON_LABEL[r];
      expect(key, `reason ${r} must have a label key`).toBeTruthy();
      expect(t('en', key).length, `reason ${r} EN label must be non-empty`).toBeGreaterThan(0);
    }
  });
});

describe('mimicStatusClass', () => {
  it('maps each status to a distinct, non-empty class; unknown falls back (never blank)', () => {
    const ok = mimicStatusClass('ok');
    const warn = mimicStatusClass('warn');
    const error = mimicStatusClass('error');
    const unknown = mimicStatusClass('unknown');
    for (const c of [ok, warn, error, unknown]) expect(c.length).toBeGreaterThan(0);
    expect(new Set([ok, warn, error]).size).toBe(3); // the three meaningful statuses are distinct
    expect(mimicStatusClass('something-new')).toBe(unknown); // total over any status
  });
});
