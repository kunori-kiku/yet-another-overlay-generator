import { useCallback, useEffect, useRef, useState } from 'react';
import { useControllerStore, selectLoggedIn } from '../stores/controllerStore';
import { useUiStore } from '../stores/uiStore';

// Ten seconds is fast enough to watch probe/update convergence while the completion-based scheduler
// below guarantees a slow controller never accumulates overlapping requests or catch-up bursts.
export const LIVE_POLL_MS = 10_000;
export const LIVE_STALE_MS = 25_000;

export type FleetRefreshHealth = 'off' | 'refreshing' | 'paused' | 'waiting' | 'healthy' | 'delayed' | 'stale';

export interface FleetLiveRefreshState {
  live: boolean;
  setLive: (value: boolean) => void;
  refreshNow: () => Promise<void>;
  refreshing: boolean;
  hidden: boolean;
  consecutiveFailures: number;
  lastSyncedAt: number | null;
  nextRefreshAt: number | null;
}

export interface FleetRefreshScheduler {
  start: () => void;
  stop: () => void;
  trigger: () => Promise<void>;
  handleVisibilityChange: () => void;
}

interface FleetRefreshSchedulerOptions {
  refresh: () => Promise<void>;
  isHidden: () => boolean;
  now: () => number;
  setTimer: (callback: () => void, delayMS: number) => number;
  clearTimer: (timer: number) => void;
  enqueue: (callback: () => void) => void;
  onHidden: (hidden: boolean) => void;
  onNextRefreshAt: (at: number | null) => void;
}

// Pure completion scheduler used by the hook and by deterministic timer tests. One scheduler owns
// one in-flight promise: every immediate/manual/visibility/timer trigger joins it, and the next ten-
// second timer is installed only in that promise's finally path. stop() is the Live-disable/unmount
// boundary; it clears an existing timer and prevents an in-flight completion from rearming one.
export function createFleetRefreshScheduler(options: FleetRefreshSchedulerOptions): FleetRefreshScheduler {
  let active = false;
  let timer: number | null = null;
  let inFlight: Promise<void> | null = null;

  const clearSchedule = () => {
    if (timer !== null) options.clearTimer(timer);
    timer = null;
    options.onNextRefreshAt(null);
  };

  const scheduleNext = () => {
    clearSchedule();
    if (!active || options.isHidden()) return;
    const next = options.now() + LIVE_POLL_MS;
    options.onNextRefreshAt(next);
    timer = options.setTimer(() => {
      timer = null;
      void trigger();
    }, LIVE_POLL_MS);
  };

  const trigger = (): Promise<void> => {
    if (!active) return Promise.resolve();
    clearSchedule();
    if (options.isHidden()) {
      options.onHidden(true);
      return Promise.resolve();
    }
    options.onHidden(false);
    if (inFlight) return inFlight;

    let attempt: Promise<void>;
    try {
      attempt = options.refresh();
    } catch (error) {
      attempt = Promise.reject(error);
    }
    const request = attempt.finally(() => {
      inFlight = null;
      scheduleNext();
    });
    inFlight = request;
    return request;
  };

  return {
    start: () => {
      if (active) return;
      active = true;
      options.enqueue(() => void trigger());
    },
    stop: () => {
      active = false;
      clearSchedule();
    },
    trigger,
    handleVisibilityChange: () => {
      const hidden = options.isHidden();
      options.onHidden(hidden);
      clearSchedule();
      if (!hidden) void trigger();
    },
  };
}

export function elapsedSeconds(timestamp: number | null, now: number): number | null {
  return timestamp === null ? null : Math.max(0, Math.floor((now - timestamp) / 1000));
}

export function countdownSeconds(timestamp: number | null, now: number): number | null {
  return timestamp === null ? null : Math.max(0, Math.ceil((timestamp - now) / 1000));
}

export function fleetRefreshHealth(state: Pick<
  FleetLiveRefreshState,
  'live' | 'refreshing' | 'hidden' | 'consecutiveFailures' | 'lastSyncedAt'
> & { now: number }): FleetRefreshHealth {
  if (!state.live) return state.refreshing ? 'refreshing' : 'off';
  if (state.hidden) return 'paused';
  if (state.lastSyncedAt !== null && state.now - state.lastSyncedAt >= LIVE_STALE_MS) return 'stale';
  if (state.consecutiveFailures >= 2) return 'stale';
  if (state.refreshing) return 'refreshing';
  if (state.consecutiveFailures === 1) return 'delayed';
  if (state.lastSyncedAt === null) return 'waiting';
  return 'healthy';
}

// Shared by /fleet and /fleet/nodes/:id. It refreshes once on mount/auth, then — only when Live is
// enabled — schedules the next poll ten seconds AFTER the prior request completes. `inFlightRef`
// makes mount, manual, visibility-return, and scheduled triggers join the same promise. Hidden tabs
// pause; becoming visible refreshes immediately and starts a fresh completion-based countdown.
export function useFleetLiveRefresh(): FleetLiveRefreshState {
  const refreshFleetView = useControllerStore((state) => state.refreshFleetView);
  const loggedIn = useControllerStore(selectLoggedIn);
  // This must be Fleet-specific: a successful topology save advances the controller's general sync
  // clock but says nothing about whether the node/telemetry snapshot was refreshed.
  const lastSyncedAt = useControllerStore((state) => state.lastFleetSyncedAt);
  const live = useUiStore((state) => state.fleetLive);
  const setFleetLive = useUiStore((state) => state.setFleetLive);
  const [refreshing, setRefreshing] = useState(false);
  const [hidden, setHidden] = useState(() => typeof document !== 'undefined' && document.hidden);
  const [consecutiveFailures, setConsecutiveFailures] = useState(0);
  const [nextRefreshAt, setNextRefreshAt] = useState<number | null>(null);
  const mountedRef = useRef(false);
  const inFlightRef = useRef<Promise<void> | null>(null);
  const schedulerRef = useRef<FleetRefreshScheduler | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const setLive = useCallback((value: boolean) => {
    if (!value) setNextRefreshAt(null);
    setFleetLive(value);
  }, [setFleetLive]);

  const performRefresh = useCallback((): Promise<void> => {
    if (!loggedIn || !mountedRef.current) return Promise.resolve();
    if (inFlightRef.current) return inFlightRef.current;

    if (mountedRef.current) setRefreshing(true);
    const request = refreshFleetView()
      .then((updated) => {
        if (!mountedRef.current) return;
        // A false result means an authenticated context change, a newer Fleet read, or a foreground
        // mutation already owning the global loading gate. It is neither success nor failure.
        if (updated) setConsecutiveFailures(0);
      })
      .catch(() => {
        // Fleet Live reads reject locally instead of touching the mutation error banner. Count the
        // failed observation once and swallow it so fire-and-forget scheduler callers remain safe.
        if (mountedRef.current) {
          setConsecutiveFailures((count) => count + 1);
        }
      })
      .finally(() => {
        inFlightRef.current = null;
        if (mountedRef.current) setRefreshing(false);
      });
    inFlightRef.current = request;
    return request;
  }, [loggedIn, refreshFleetView]);

  // Manual refreshes share the active Live scheduler, so they cancel its old countdown, join any
  // in-flight attempt, and start the next ten seconds only after completion. With Live off, this is
  // the same one-shot single-flight refresh as before.
  const refreshNow = useCallback((): Promise<void> => {
    return schedulerRef.current?.trigger() ?? performRefresh();
  }, [performRefresh]);

  // Server truth over the persisted advisory cache on first view/session availability.
  useEffect(() => {
    if (loggedIn) queueMicrotask(() => void refreshNow());
  }, [loggedIn, refreshNow]);

  useEffect(() => {
    if (!live || !loggedIn) return;
    const scheduler = createFleetRefreshScheduler({
      refresh: performRefresh,
      isHidden: () => document.hidden,
      now: () => Date.now(),
      setTimer: (callback, delayMS) => window.setTimeout(callback, delayMS),
      clearTimer: (timer) => window.clearTimeout(timer),
      enqueue: (callback) => queueMicrotask(callback),
      onHidden: setHidden,
      onNextRefreshAt: setNextRefreshAt,
    });
    schedulerRef.current = scheduler;

    const onVisibilityChange = () => scheduler.handleVisibilityChange();
    document.addEventListener('visibilitychange', onVisibilityChange);

    // Enabling Live is an immediate refresh. If the mount request is still running, refreshNow joins
    // it and its completion schedules the first tick.
    scheduler.start();

    return () => {
      scheduler.stop();
      if (schedulerRef.current === scheduler) schedulerRef.current = null;
      document.removeEventListener('visibilitychange', onVisibilityChange);
    };
  }, [live, loggedIn, performRefresh]);

  return {
    live,
    setLive,
    refreshNow,
    refreshing,
    hidden,
    consecutiveFailures,
    lastSyncedAt,
    nextRefreshAt: live && loggedIn ? nextRefreshAt : null,
  };
}
