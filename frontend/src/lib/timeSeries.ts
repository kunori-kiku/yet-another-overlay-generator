// Pure chart-shape helpers kept outside React component modules so Fast Refresh sees component-only
// exports. Return only singleton-run timestamps: enabling Recharts' ordinary dot object for a series
// with one isolated point would also draw up to a thousand unnecessary circles on connected runs.
export function isolatedPointTimes(series: {
  data: ReadonlyArray<{ t: number | string; avg: number | null }>;
}): Set<number> {
  const isolated = new Set<number>();
  let run: number[] = [];
  const flush = () => {
    if (run.length === 1) isolated.add(run[0]);
    run = [];
  };
  for (const point of series.data) {
    const t = typeof point.t === 'number' ? point.t : Date.parse(point.t);
    if (typeof point.avg === 'number' && Number.isFinite(point.avg) && Number.isFinite(t)) {
      run.push(t);
    } else {
      flush();
    }
  }
  flush();
  return isolated;
}

const MULTI_DAY_TICK_THRESHOLD_MS = 24 * 60 * 60 * 1000;

// Short windows need precise clock labels; 24-hour and multi-day windows need a date component or
// repeated times become ambiguous. Kept pure so the wide-range chart contract is directly tested.
export function formatTimeSeriesTick(
  value: number | string,
  language: string,
  xDomain?: readonly [number, number],
): string {
  const ms = typeof value === 'number' ? value : Number(value);
  if (!Number.isFinite(ms)) return '';
  const date = new Date(ms);
  const span = xDomain ? Math.max(0, xDomain[1] - xDomain[0]) : 0;
  if (span >= MULTI_DAY_TICK_THRESHOLD_MS) {
    return date.toLocaleString(language, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
    });
  }
  return date.toLocaleTimeString(language, { hour: '2-digit', minute: '2-digit' });
}
