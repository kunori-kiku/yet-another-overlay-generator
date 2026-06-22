import type { NodeCondition } from '../../types/controller';
import { conditionVisual, conditionLabel } from '../../lib/nodeConditions';

// NodeConditions renders a node's structured feedback (plan-2) as a wrapping badge strip. It is
// GENERIC by construction: color comes from conditionVisual (the single status→class authority) and
// the label from conditionLabel (no per-type branching), so later plans (selfupdate/wireguard/mimic)
// add condition PRODUCERS with zero new rendering. The curated, length-capped message is the tooltip
// (title) — never a raw dump. Empty ⇒ null (no "no conditions" filler in a table cell).
export function NodeConditions({ conditions }: { conditions: NodeCondition[] }) {
  if (!conditions || conditions.length === 0) return null;
  return (
    <span className="flex flex-wrap gap-1">
      {conditions.map((c) => (
        <span
          key={`${c.type}:${c.reason}`}
          title={c.message || undefined}
          className={`px-2 py-0.5 rounded text-xs border ${conditionVisual(c.status)}`}
        >
          {conditionLabel(c)}
        </span>
      ))}
    </span>
  );
}
