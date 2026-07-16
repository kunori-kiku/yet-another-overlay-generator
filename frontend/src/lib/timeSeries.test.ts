// @vitest-environment node

import { afterEach, describe, expect, it, vi } from 'vitest';
import { formatTimeSeriesTick, isolatedPointTimes } from './timeSeries';

afterEach(() => vi.restoreAllMocks());

function series(values: Array<number | null>) {
  return { data: values.map((avg, index) => ({ t: index * 1000, avg })) };
}

describe('isolatedPointTimes', () => {
  it('requests a visible dot for one real point even when gap sentinels surround it', () => {
    expect([...isolatedPointTimes(series([null, 12.4, null]))]).toEqual([1000]);
  });

  it('marks only isolated points, never every point in a mixed connected/gapped series', () => {
    expect([...isolatedPointTimes(series([12.4, null, 13.1]))]).toEqual([0, 2000]);
    expect([...isolatedPointTimes(series([12.4, 13.1, null, 9.2]))]).toEqual([3000]);
  });

  it('keeps ordinary connected lines and all-gap series dot-free', () => {
    expect(isolatedPointTimes(series([12.4, 13.1])).size).toBe(0);
    expect(isolatedPointTimes(series([null, null])).size).toBe(0);
  });

  it('does not expand one isolated point into a thousand DOM markers', () => {
    const values = Array.from({ length: 1000 }, (_, index) => index === 500 ? null : index);
    values[999] = null;
    values[998] = 42;
    values[997] = null;
    expect([...isolatedPointTimes(series(values))]).toEqual([998_000]);
  });
});

describe('formatTimeSeriesTick', () => {
  it('uses clock-only labels for short windows and date-bearing labels for 24h/7d windows', () => {
    const timeOnly = vi.spyOn(Date.prototype, 'toLocaleTimeString').mockReturnValue('CLOCK');
    const withDate = vi.spyOn(Date.prototype, 'toLocaleString').mockReturnValue('DATE + HOUR');
    const at = Date.parse('2026-07-16T10:00:00Z');

    expect(formatTimeSeriesTick(at, 'en', [at - 6 * 3600_000, at])).toBe('CLOCK');
    expect(timeOnly).toHaveBeenCalledOnce();
    expect(withDate).not.toHaveBeenCalled();

    expect(formatTimeSeriesTick(at, 'en', [at - 7 * 24 * 3600_000, at])).toBe('DATE + HOUR');
    expect(withDate).toHaveBeenCalledOnce();
  });
});
