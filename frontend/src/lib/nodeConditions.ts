// nodeConditions.ts holds the PURE logic for the structured Node Conditions channel (plan-2): the
// status→badge-class resolver, the badge label, and the wire→type mapper. It lives in lib/ — not in
// the component — for the same reason deriveUpdateState does (updateStatus.ts): pure, exported, and
// unit-testable in the `node` vitest environment with no jsdom. NodeConditions.tsx is the thin
// render layer over these helpers.

import type { NodeCondition } from '../types/controller';

// STATUS_CLASS maps a condition status to a Tailwind badge class. The canonical set is
// ok/warn/error/unknown (the backend classify() is the source of truth); an unrecognized status
// falls through to the neutral 'unknown' look so a new status never renders blank. Mirrors
// UpdateStatusChip.CHIP_CLASS — drives the chip from the semantic STATUS token families
// (ok→success, warn→warning, error→danger, unknown→info) so the chip is legible in BOTH light and
// dark themes (the prior raw `*-900/40 text-*-300` palette was dark-only and went light-on-light).
const STATUS_CLASS: Record<string, string> = {
  ok: 'bg-[var(--success-bg)] text-[var(--success)] border-[var(--success-border)]',
  unknown: 'bg-[var(--info-bg)] text-[var(--info)] border-[var(--info-border)]',
  warn: 'bg-[var(--warning-bg)] text-[var(--warning)] border-[var(--warning-border)]',
  error: 'bg-[var(--danger-bg)] text-[var(--danger)] border-[var(--danger-border)]',
};

// conditionVisual is the PURE status→class resolver (no DOM). Any unrecognized status ⇒ the neutral
// 'unknown' class, so the strip is total over any backend status.
export function conditionVisual(status: string): string {
  return STATUS_CLASS[status] ?? STATUS_CLASS.unknown;
}

// conditionLabel is the badge text: "<type>: <reason>" (the closed code, not the message). Generic —
// no per-type branching, so a new condition type renders with zero code change. (A later plan may add
// a richer known-type chip by overriding the render at this boundary — the extension hook.)
export function conditionLabel(c: NodeCondition): string {
  return c.reason ? `${c.type}: ${c.reason}` : c.type;
}

// ConditionWire is the snake_case operator wire shape (controller conditionJSON). It is mapped to the
// camelCase NodeCondition by mapNodeConditions; kept here so the mapping + its test live in one place.
export interface ConditionWire {
  type: string;
  status: string;
  reason: string;
  message?: string;
  since?: string;
  observed_at: string;
}

// mapNodeConditions projects the wire array onto camelCase NodeConditions. Absent array ⇒ [] (the
// omitempty wire field arrives undefined); absent message ⇒ '' (so the UI never reads undefined);
// observed_at → observedAt. Pure — unit-tested without the controllerClient transport.
export function mapNodeConditions(wire: ConditionWire[] | undefined): NodeCondition[] {
  return (wire ?? []).map((c) => ({
    type: c.type,
    status: c.status,
    reason: c.reason,
    message: c.message ?? '',
    since: c.since ?? '',
    observedAt: c.observed_at,
  }));
}
