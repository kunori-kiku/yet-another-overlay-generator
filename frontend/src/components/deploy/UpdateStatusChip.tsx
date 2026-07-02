import { t, type MessageKey, type UILanguage } from '../../i18n';
import type { ControllerNode } from '../../types/controller';
import type { ControllerSettings } from '../../api/controllerClient';
import { deriveUpdateState, chipTitle, type UpdateState } from '../../lib/updateStatus';

// UpdateStatusChip renders a node's agent-self-update status (controller-panel-rollout-ui plan-5),
// shared by the Fleet registry table and the node detail page. When no rollout is configured
// (state 'off') it renders a muted dash, not a chip — keeping the empty-target safety contract
// visible. settings===null is treated as 'off' (defensive: refresh loads settings best-effort).

const CHIP_CLASS: Record<UpdateState, string> = {
  off: '',
  'not-targeted': 'bg-[var(--control)] text-[var(--content-muted)] border-[var(--hairline)]',
  pending: 'bg-[var(--warning-bg)] text-[var(--warning)] border-[var(--warning-border)]',
  applying: 'bg-[var(--warning-bg)] text-[var(--warning)] border-[var(--warning-border)]',
  applied: 'bg-[var(--success-bg)] text-[var(--success)] border-[var(--success-border)]',
  failed: 'bg-[var(--danger-bg)] text-[var(--danger)] border-[var(--danger-border)]',
  stale: 'bg-[var(--control)] text-[var(--content)] border-[var(--hairline)]',
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
  if (state === 'off') return <span className="text-[var(--content-muted)]">—</span>;

  // Tooltip: the curated structured-condition message (plan-3) or the legacy lastHealth line, with the
  // best-effort caveat appended only on the legacy 'failed' path — see chipTitle (pure + tested).
  const title = chipTitle(node, state, t(language, 'updateStatus.failedBestEffort'));

  return (
    <span title={title} className={`px-2 py-0.5 rounded text-xs border ${CHIP_CLASS[state]}`}>
      {t(language, LABEL_KEY[state])}
    </span>
  );
}
