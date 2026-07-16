import { useEffect, useState } from 'react';
import { t, type MessageKey, type UILanguage } from '../../i18n';
import {
  countdownSeconds,
  elapsedSeconds,
  fleetRefreshHealth,
  type FleetLiveRefreshState,
  type FleetRefreshHealth,
} from '../../hooks/useFleetLiveRefresh';

const HEALTH_KEYS: Record<FleetRefreshHealth, MessageKey> = {
  off: 'fleetRefresh.liveOff',
  refreshing: 'fleetRefresh.refreshing',
  paused: 'fleetRefresh.paused',
  waiting: 'fleetRefresh.waiting',
  healthy: 'fleetRefresh.liveHealthy',
  delayed: 'fleetRefresh.delayed',
  stale: 'fleetRefresh.stale',
};

const HEALTH_CLASSES: Record<FleetRefreshHealth, string> = {
  off: 'text-[var(--content-muted)]',
  refreshing: 'text-[var(--warning)]',
  paused: 'text-[var(--warning)]',
  waiting: 'text-[var(--warning)]',
  healthy: 'text-[var(--success)]',
  delayed: 'text-[var(--warning)]',
  stale: 'text-[var(--danger)]',
};

const HEALTH_DOT_CLASSES: Record<FleetRefreshHealth, string> = {
  off: 'bg-[var(--content-muted)]',
  refreshing: 'bg-[var(--warning)]',
  paused: 'bg-[var(--warning)]',
  waiting: 'bg-[var(--warning)]',
  healthy: 'bg-[var(--success)]',
  delayed: 'bg-[var(--warning)]',
  stale: 'bg-[var(--danger)]',
};

// Routine 10-second healthy→refreshing→healthy motion stays visible but silent. Only states that
// need operator attention (plus the initial waiting state) become polite live-region updates.
const ANNOUNCED_HEALTH = new Set<FleetRefreshHealth>(['paused', 'waiting', 'delayed', 'stale']);

export function FleetRefreshControls({
  state,
  language,
  refreshTestID,
}: {
  state: FleetLiveRefreshState;
  language: UILanguage;
  refreshTestID: string;
}) {
  // Keep the one-second display ticker inside this tiny control. If it lived in the page-level hook,
  // every tick would re-render the whole Fleet registry or node-detail/chart subtree.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);
  const health = fleetRefreshHealth({ ...state, now });
  const age = elapsedSeconds(state.lastSyncedAt, now);
  const countdown = countdownSeconds(state.nextRefreshAt, now);
  const announcement = ANNOUNCED_HEALTH.has(health) ? t(language, HEALTH_KEYS[health]) : '';

  return (
    <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-1 text-xs">
      <label className="flex items-center gap-1.5 text-[var(--content-muted)]">
        <input
          type="checkbox"
          checked={state.live}
          onChange={(event) => state.setLive(event.target.checked)}
          data-testid="fleet-live-toggle"
        />
        {t(language, 'updateStatus.live')}
      </label>

      <div data-testid="fleet-refresh-status" className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
        {/* Age/countdown and routine healthy/refreshing transitions remain ordinary text so assistive
            technology is not forced into a continuous announcement loop every ten seconds. */}
        <span
          data-testid="fleet-refresh-visible-health"
          className={`inline-flex items-center gap-1.5 font-medium ${HEALTH_CLASSES[health]}`}
        >
          <span aria-hidden="true" className={`inline-block h-2 w-2 rounded-full ${HEALTH_DOT_CLASSES[health]}`} />
          {t(language, HEALTH_KEYS[health])}
        </span>
        {/* Persist across every state so an exceptional transition changes content inside an already-
            mounted live region. Routine states deliberately clear it instead of announcing. */}
        <span
          className="sr-only"
          aria-live="polite"
          aria-atomic="true"
          data-testid="fleet-refresh-announcement"
        >
          {announcement}
        </span>
        <span className="text-[var(--content-muted)]">
          {age === null
            ? t(language, 'fleetRefresh.neverUpdated')
            : t(language, 'fleetRefresh.updatedAgo', { seconds: age })}
        </span>
        {state.live && !state.hidden && countdown !== null && !state.refreshing && (
          <span className="text-[var(--content-muted)]">
            {t(language, 'fleetRefresh.nextIn', { seconds: countdown })}
          </span>
        )}
      </div>

      <button
        type="button"
        onClick={() => void state.refreshNow()}
        disabled={state.refreshing}
        data-testid={refreshTestID}
        className="inline-flex items-center gap-1 rounded border border-[var(--hairline)] px-2 py-1 text-[var(--info)] hover:bg-[var(--control)] disabled:text-[var(--content-muted)]"
      >
        <span
          aria-hidden="true"
          className={state.refreshing ? 'inline-block motion-safe:animate-spin motion-reduce:animate-none' : 'inline-block'}
        >
          ↻
        </span>
        {state.refreshing ? t(language, 'fleetRefresh.refreshingButton') : t(language, 'fleetRefresh.refresh')}
      </button>
    </div>
  );
}
