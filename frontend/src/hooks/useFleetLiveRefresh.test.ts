// @vitest-environment node

import { describe, expect, it, vi } from 'vitest';
import {
  LIVE_POLL_MS,
  LIVE_STALE_MS,
  countdownSeconds,
  createFleetRefreshScheduler,
  elapsedSeconds,
  fleetRefreshHealth,
} from './useFleetLiveRefresh';

const now = 100_000;

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function schedulerHarness(initialHidden = false) {
  let clock = now;
  let hidden = initialHidden;
  let nextTimerID = 1;
  const timers = new Map<number, { callback: () => void; delayMS: number }>();
  const queued: Array<() => void> = [];
  const nextRefreshUpdates: Array<number | null> = [];
  const hiddenUpdates: boolean[] = [];
  const attempts: ReturnType<typeof deferred>[] = [];
  const refresh = vi.fn(() => {
    const attempt = deferred();
    attempts.push(attempt);
    return attempt.promise;
  });
  const scheduler = createFleetRefreshScheduler({
    refresh,
    isHidden: () => hidden,
    now: () => clock,
    setTimer: (callback, delayMS) => {
      const id = nextTimerID++;
      timers.set(id, {
        callback: () => {
          timers.delete(id);
          callback();
        },
        delayMS,
      });
      return id;
    },
    clearTimer: (id) => {
      timers.delete(id);
    },
    enqueue: (callback) => queued.push(callback),
    onHidden: (value) => hiddenUpdates.push(value),
    onNextRefreshAt: (value) => nextRefreshUpdates.push(value),
  });
  return {
    scheduler,
    refresh,
    attempts,
    timers,
    queued,
    nextRefreshUpdates,
    hiddenUpdates,
    setHidden: (value: boolean) => { hidden = value; },
    setClock: (value: number) => { clock = value; },
  };
}

function health(overrides: Partial<Parameters<typeof fleetRefreshHealth>[0]> = {}) {
  return fleetRefreshHealth({
    live: true,
    refreshing: false,
    hidden: false,
    consecutiveFailures: 0,
    lastSyncedAt: now - 5_000,
    now,
    ...overrides,
  });
}

describe('Fleet Live feedback primitives', () => {
  it('pins the completion-based cadence and overdue threshold', () => {
    expect(LIVE_POLL_MS).toBe(10_000);
    expect(LIVE_STALE_MS).toBe(25_000);
  });

  it('formats authoritative age and next-refresh countdown without negative values', () => {
    expect(elapsedSeconds(now - 5_900, now)).toBe(5);
    expect(elapsedSeconds(now + 1_000, now)).toBe(0);
    expect(elapsedSeconds(null, now)).toBeNull();
    expect(countdownSeconds(now + 5_100, now)).toBe(6);
    expect(countdownSeconds(now - 1, now)).toBe(0);
  });

  it('uses off/amber/green/danger states without treating a refresh spinner as permanent', () => {
    expect(health({ live: false })).toBe('off');
    expect(health({ refreshing: true })).toBe('refreshing');
    expect(health({ live: false, refreshing: true })).toBe('refreshing');
    expect(health({ refreshing: true, lastSyncedAt: now - LIVE_STALE_MS })).toBe('stale');
    expect(health({ hidden: true })).toBe('paused');
    expect(health({ lastSyncedAt: null })).toBe('waiting');
    expect(health()).toBe('healthy');
    expect(health({ consecutiveFailures: 1 })).toBe('delayed');
    expect(health({ consecutiveFailures: 2 })).toBe('stale');
    expect(health({ lastSyncedAt: now - LIVE_STALE_MS })).toBe('stale');
  });
});

describe('Fleet completion scheduler', () => {
  it('joins concurrent triggers and schedules ten seconds only after completion', async () => {
    const h = schedulerHarness();
    h.scheduler.start();
    expect(h.queued).toHaveLength(1);
    h.queued.shift()?.();
    expect(h.refresh).toHaveBeenCalledTimes(1);
    expect(h.timers.size).toBe(0);
    expect(h.nextRefreshUpdates.at(-1)).toBeNull();

    const joined = h.scheduler.trigger();
    expect(h.refresh).toHaveBeenCalledTimes(1);
    expect(h.timers.size).toBe(0);

    h.attempts[0].resolve();
    await joined;
    expect(h.timers.size).toBe(1);
    expect([...h.timers.values()][0].delayMS).toBe(LIVE_POLL_MS);
    expect(h.nextRefreshUpdates.at(-1)).toBe(now + LIVE_POLL_MS);

    const timer = [...h.timers.values()][0];
    timer.callback();
    expect(h.refresh).toHaveBeenCalledTimes(2);
    expect(h.timers.size).toBe(0);
  });

  it('pauses while hidden and refreshes immediately when visibility returns', () => {
    const h = schedulerHarness(true);
    h.scheduler.start();
    h.queued.shift()?.();
    expect(h.refresh).not.toHaveBeenCalled();
    expect(h.hiddenUpdates.at(-1)).toBe(true);
    expect(h.timers.size).toBe(0);

    h.setHidden(false);
    h.scheduler.handleVisibilityChange();
    expect(h.hiddenUpdates.at(-1)).toBe(false);
    expect(h.refresh).toHaveBeenCalledTimes(1);
    expect(h.timers.size).toBe(0);
  });

  it('stop cancels a pending tick and prevents an in-flight completion from rearming it', async () => {
    const h = schedulerHarness();
    h.scheduler.start();
    h.queued.shift()?.();
    const first = h.scheduler.trigger();
    h.attempts[0].resolve();
    await first;
    expect(h.timers.size).toBe(1);

    h.scheduler.stop();
    expect(h.timers.size).toBe(0);
    expect(h.nextRefreshUpdates.at(-1)).toBeNull();

    h.scheduler.start();
    h.queued.shift()?.();
    const second = h.scheduler.trigger();
    h.setClock(now + 5_000);
    h.scheduler.stop();
    h.attempts[1].resolve();
    await second;
    expect(h.timers.size).toBe(0);
    expect(h.nextRefreshUpdates.at(-1)).toBeNull();
  });
});
