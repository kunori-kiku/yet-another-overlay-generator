import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t, type UILanguage } from '../../i18n';
import { TimeSeriesChart, type TimeSeriesSeries } from '../charts/TimeSeriesChart';
import {
  GRANULARITIES,
  RANGE_PRESETS,
  granularityStep,
  metricSeries,
  parseGoDuration,
  rangeWindow,
} from '../../lib/telemetryHistory';
import type { Granularity, NodeHistory, RangePreset } from '../../lib/telemetryHistory';

// NodeResourceHistory (telemetry-history plan-4): the node-detail CPU/RAM/load history charts. It
// owns the range preset (1h/6h/24h/7d) + granularity (auto/30s/5m/30m/1h) controls, fetches the
// plan-3 endpoint on view, parameter change, and each successful page refresh, then renders three
// reusable TimeSeriesCharts (CPU %, load 1/5/15, memory used %). LIVE-ONLY: the fetched history is NEVER
// persisted (the stripLiveTelemetry custody rule). Disabled/empty/loading/error each have a state.

// A percent metric pins the Y axis to 0..100 so a flat-ish series is not visually exaggerated; load
// has no natural ceiling, so it auto-fits.
const PERCENT_DOMAIN: [number, number] = [0, 100];

interface NodeResourceHistoryProps {
  nodeId: string;
  // The parent fleet refresh updates this after a successful manual/Live poll. It is a trigger only;
  // history stays component-local and is never copied into the persisted controller store.
  refreshAt?: number | null;
}

export function NodeResourceHistory({ nodeId, refreshAt }: NodeResourceHistoryProps) {
  const language = useTopologyStore((s) => s.language);
  const fetchNodeHistory = useControllerStore((s) => s.fetchNodeHistory);

  const [range, setRange] = useState<RangePreset>('6h');
  const [granularity, setGranularity] = useState<Granularity>('auto');
  const [history, setHistory] = useState<NodeHistory | null>(null);
  // Bound the axis to the exact window that produced history. Keeping it alongside the response
  // prevents a moving Date.now() render from showing an axis that does not match the fetched data.
  const [historyWindow, setHistoryWindow] = useState<[number, number] | null>(null);
  // Start loading (the first fetch is always in flight on mount); the parent keys this component on
  // the node id so a node switch remounts and resets to this initial loading state.
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  // Fetch on mount + whenever the node or a param changes. Component-local setState is confined to
  // the async .then/.catch callbacks (which run after the effect's synchronous phase, so the
  // react-hooks set-state-in-effect rule is satisfied — the codebase's fetch effects never setState
  // synchronously); the loading/error RESET on a param change happens in the change handlers below.
  // The `ignore` flag drops a stale/late response (a fast param change, or an unmount).
  useEffect(() => {
    let ignore = false;
    const { from, to } = rangeWindow(range, Date.now());
    fetchNodeHistory(nodeId, from, to, granularityStep(granularity))
      .then((h) => {
        if (!ignore) {
          setHistory(h);
          setHistoryWindow([Date.parse(from), Date.parse(to)]);
          setError(false);
          setLoading(false);
        }
      })
      .catch(() => {
        if (!ignore) {
          setHistory(null);
          setHistoryWindow(null);
          setError(true);
          setLoading(false);
        }
      });
    return () => {
      ignore = true;
    };
  }, [nodeId, range, granularity, refreshAt, fetchNodeHistory]);

  // Param changes reset to a clean loading state (allowed here — event handlers, not the effect
  // body) so switching from an error/empty view shows the spinner immediately, then the effect
  // above refetches for the new params.
  const changeRange = (p: RangePreset) => {
    setLoading(true);
    setError(false);
    setRange(p);
  };
  const changeGranularity = (g: Granularity) => {
    setLoading(true);
    setError(false);
    setGranularity(g);
  };

  const buckets = history?.buckets ?? [];
  const stepMs = parseGoDuration(history?.step ?? '');
  const xDomain = historyWindow ?? undefined;

  const cpuSeries: TimeSeriesSeries[] = [
    {
      key: 'cpu',
      label: t(language, 'nodeHistory.cpuSeries'),
      unit: '%',
      color: 'var(--accent)',
      data: metricSeries(buckets, stepMs, (b) => b.cpuPct),
    },
  ];
  const loadSeries: TimeSeriesSeries[] = [
    { key: 'load1', label: t(language, 'nodeHistory.load1'), unit: '', color: 'var(--accent)', data: metricSeries(buckets, stepMs, (b) => b.load1) },
    { key: 'load5', label: t(language, 'nodeHistory.load5'), unit: '', color: 'var(--info)', data: metricSeries(buckets, stepMs, (b) => b.load5) },
    { key: 'load15', label: t(language, 'nodeHistory.load15'), unit: '', color: 'var(--success)', data: metricSeries(buckets, stepMs, (b) => b.load15) },
  ];
  const memSeries: TimeSeriesSeries[] = [
    {
      key: 'mem',
      label: t(language, 'nodeHistory.memSeries'),
      unit: '%',
      color: 'var(--success)',
      data: metricSeries(buckets, stepMs, (b) => b.memUsedPct),
    },
  ];

  const segClass = (selected: boolean) =>
    `px-2.5 py-1 text-xs ${
      selected ? 'bg-[var(--accent)] text-[var(--accent-fg)]' : 'text-[var(--content)] hover:bg-[var(--control-hover)]'
    }`;

  return (
    <div
      className="rounded border border-[var(--hairline)] bg-[var(--surface)] px-3 py-2"
      data-testid="node-resource-history"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-[var(--content)]">{t(language, 'nodeHistory.heading')}</div>
        <div className="flex flex-wrap items-center gap-3">
          {/* Range presets */}
          <div className="flex items-center gap-1.5">
            <span className="text-xs text-[var(--content-muted)]">{t(language, 'nodeHistory.rangeLabel')}</span>
            <div className="flex w-fit items-center overflow-hidden rounded border border-[var(--hairline)] bg-[var(--control)]">
              {RANGE_PRESETS.map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => changeRange(p)}
                  aria-pressed={range === p}
                  data-testid={`history-range-${p}`}
                  className={segClass(range === p)}
                >
                  {p}
                </button>
              ))}
            </div>
          </div>
          {/* Granularity */}
          <label className="flex items-center gap-1.5 text-xs text-[var(--content-muted)]">
            {t(language, 'nodeHistory.granularityLabel')}
            <select
              value={granularity}
              onChange={(e) => changeGranularity(e.target.value as Granularity)}
              data-testid="history-granularity"
              className="rounded border border-[var(--hairline)] bg-[var(--control)] px-1.5 py-1 text-xs text-[var(--content)] outline-none focus:border-[var(--accent)]"
            >
              {GRANULARITIES.map((g) => (
                <option key={g} value={g}>
                  {g === 'auto' ? t(language, 'nodeHistory.granularityAuto') : g}
                </option>
              ))}
            </select>
          </label>
        </div>
      </div>

      {/* States: disabled (history off) > error > loading(first) > empty > charts. */}
      {history?.disabled ? (
        <p className="mt-2 text-xs text-[var(--content-muted)]" data-testid="history-disabled">
          {t(language, 'nodeHistory.disabled')}{' '}
          <Link to="/settings" className="text-[var(--info)] hover:underline">
            {t(language, 'nodeHistory.disabledCta')}
          </Link>
        </p>
      ) : error ? (
        <p className="mt-2 text-xs text-[var(--danger)]" data-testid="history-error">
          {t(language, 'nodeHistory.error')}
        </p>
      ) : loading && !history ? (
        <p className="mt-2 text-xs text-[var(--content-muted)]" data-testid="history-loading">
          {t(language, 'nodeHistory.loading')}
        </p>
      ) : buckets.length === 0 ? (
        <p className="mt-2 text-xs text-[var(--content-muted)]" data-testid="history-empty">
          {t(language, 'nodeHistory.empty')}
        </p>
      ) : (
        <div className="mt-3 space-y-4">
          <HistoryChart title={t(language, 'nodeHistory.cpuTitle')} series={cpuSeries} yDomain={PERCENT_DOMAIN} xDomain={xDomain} language={language} />
          <HistoryChart title={t(language, 'nodeHistory.loadTitle')} series={loadSeries} xDomain={xDomain} language={language} />
          <HistoryChart title={t(language, 'nodeHistory.memTitle')} series={memSeries} yDomain={PERCENT_DOMAIN} xDomain={xDomain} language={language} />
        </div>
      )}
    </div>
  );
}

function HistoryChart({
  title,
  series,
  yDomain,
  xDomain,
  language,
}: {
  title: string;
  series: TimeSeriesSeries[];
  yDomain?: [number, number];
  xDomain?: [number, number];
  language: UILanguage;
}) {
  return (
    <div>
      <div className="mb-1 text-xs font-medium text-[var(--content-muted)]">{title}</div>
      <TimeSeriesChart series={series} yDomain={yDomain} xDomain={xDomain} height={180} language={language} />
    </div>
  );
}
