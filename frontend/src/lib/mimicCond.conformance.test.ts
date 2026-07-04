import { describe, it, expect } from 'vitest';
import { REASON_LABEL } from './mimicCond';
import { t } from '../i18n';

// plan-6: the mimic chip's reason→label map. Pure, node-env (no jsdom). Guards the curated-label
// contract (every emitted reason renders a non-blank localized label; never a raw dump). The chip's
// COLOR is conditionVisual (covered by nodeConditions.conformance.test.ts) — one status→class authority.

describe('REASON_LABEL', () => {
  // The keys MUST equal the agent classifyMimic reason codes (plan-5).
  const reasons = ['Active', 'FellBackToUDP', 'KernelTooOld', 'EbpfLoadFailed', 'InstallFailed', 'EgressUnresolved', 'NativeDowngradedSkb'];
  it('maps every emitted reason to a present, non-empty EN label', () => {
    for (const r of reasons) {
      const key = REASON_LABEL[r];
      expect(key, `reason ${r} must have a label key`).toBeTruthy();
      expect(t('en', key).length, `reason ${r} EN label must be non-empty`).toBeGreaterThan(0);
    }
  });
});
