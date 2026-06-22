import { t, type UILanguage } from '../../i18n';
import type { NodeCondition } from '../../types/controller';
import { REASON_LABEL, mimicStatusClass } from '../../lib/mimicCond';

// MimicConditionChip is the known-type renderer for type==='mimic', plugged into plan-2's generic
// <NodeConditions> strip. It renders a localized, curated label (e.g. "Mimic: fell back to UDP
// (kernel lacks eBPF)") colored by status, with the agent's curated message as the tooltip — never a
// raw dump. An unknown future reason falls through to the agent's reason string (never blank).
export function MimicConditionChip({ c, language }: { c: NodeCondition; language: UILanguage }) {
  const labelKey = REASON_LABEL[c.reason];
  const label = labelKey ? t(language, labelKey) : c.reason || t(language, 'mimicCond.label');
  return (
    <span
      title={c.message || undefined}
      className={`px-2 py-0.5 rounded text-xs border ${mimicStatusClass(c.status)}`}
    >
      {t(language, 'mimicCond.label')}: {label}
    </span>
  );
}
