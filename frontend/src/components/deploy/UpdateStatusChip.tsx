import { t, type MessageKey, type UILanguage } from '../../i18n';
import type { ControllerNode } from '../../types/controller';
import type { ControllerSettings } from '../../api/controllerClient';
import { deriveUpdateState, type UpdateState } from '../../lib/updateStatus';

// UpdateStatusChip renders a node's agent-self-update status (controller-panel-rollout-ui plan-5),
// shared by the Fleet registry table and the node detail page. When no rollout is configured
// (state 'off') it renders a muted dash, not a chip — keeping the empty-target safety contract
// visible. settings===null is treated as 'off' (defensive: refresh loads settings best-effort).

const CHIP_CLASS: Record<UpdateState, string> = {
  off: '',
  'not-targeted': 'bg-gray-800 text-gray-400 border-gray-600',
  pending: 'bg-amber-900/40 text-amber-300 border-amber-700',
  applying: 'bg-amber-900/40 text-amber-300 border-amber-700',
  applied: 'bg-green-900/40 text-green-300 border-green-700',
  failed: 'bg-red-900/40 text-red-300 border-red-700',
  stale: 'bg-gray-700/40 text-gray-300 border-gray-600',
};

const LABEL_KEY: Record<UpdateState, MessageKey> = {
  off: 'updateStatus.notTargeted', // unused (off renders a dash) but keeps the map total
  'not-targeted': 'updateStatus.notTargeted',
  pending: 'updateStatus.pending',
  applying: 'updateStatus.applying',
  applied: 'updateStatus.applied',
  failed: 'updateStatus.failed',
  stale: 'updateStatus.stale',
};

export function UpdateStatusChip({
  node,
  settings,
  language,
}: {
  node: ControllerNode;
  settings: ControllerSettings | null;
  language: UILanguage;
}) {
  const state = deriveUpdateState(node, settings);
  if (state === 'off') return <span className="text-gray-500">—</span>;

  // Tooltip carries the raw agent health line; for 'failed' it also flags the best-effort caveat
  // (the 'abandoned:' marker is transient — see updateStatus.ts / Principle 7).
  const base = node.lastHealth || '';
  const title =
    state === 'failed'
      ? `${base}${base ? ' ' : ''}(${t(language, 'updateStatus.failedBestEffort')})`
      : base || undefined;

  return (
    <span title={title} className={`px-2 py-0.5 rounded text-xs border ${CHIP_CLASS[state]}`}>
      {t(language, LABEL_KEY[state])}
    </span>
  );
}
