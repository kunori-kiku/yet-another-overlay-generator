import { type UILanguage } from '../../i18n';
import type { NodeCondition } from '../../types/controller';
import { conditionVisual, conditionLabel } from '../../lib/nodeConditions';
import { MimicConditionChip } from './MimicConditionChip';

// NodeConditions renders a node's structured feedback (plan-2) as a wrapping badge strip. It is
// GENERIC by construction: color comes from conditionVisual (the single status→class authority) and
// the label from conditionLabel (no per-type branching). plan-6 adds the FIRST known-type renderer:
// a `mimic` condition gets the rich MimicConditionChip (localized, categorized) instead of the
// generic chip; every other type still falls through to the generic render with zero new code. The
// curated, length-capped message is the tooltip (title) — never a raw dump. Empty ⇒ null.
export function NodeConditions({ conditions, language }: { conditions: NodeCondition[]; language: UILanguage }) {
  if (!conditions || conditions.length === 0) return null;
  return (
    <span className="flex flex-wrap gap-1">
      {conditions.map((c) =>
        c.type === 'mimic' ? (
          <MimicConditionChip key={`${c.type}:${c.reason}`} c={c} language={language} />
        ) : (
          <span
            key={`${c.type}:${c.reason}`}
            title={c.message || undefined}
            className={`px-2 py-0.5 rounded text-xs border ${conditionVisual(c.status)}`}
          >
            {conditionLabel(c)}
          </span>
        ),
      )}
    </span>
  );
}
