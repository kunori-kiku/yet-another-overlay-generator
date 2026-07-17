import { useId, type ReactNode } from 'react';
import {
  Area,
  CartesianGrid,
  ComposedChart,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
  type DotItemDotProps,
} from 'recharts';
import { t, type UILanguage } from '../../i18n';
import { formatTimeSeriesTick, isolatedPointTimes } from '../../lib/timeSeries';

// TimeSeriesChart — a SERIES-GENERIC, theme-token-only line chart with an optional min/max band
// (plan-4). It knows NOTHING resource-specific: it plots whatever {key,label,unit,data} series it is
// handed, which now includes both host-resource and active-probe latency/availability charts. All
// color comes from CSS custom properties (the beta.13 lesson: zero hardcoded
// hex/rgb) — recharts resolves var(--…) in its stroke/fill props against the current theme, so the
// chart follows light/dark automatically. `language` is used only as a locale for time/number
// formatting (still generic — not tied to any metric).
//
// A container `data-testid="timeseries-chart"` and a per-series `timeseries-series-<key>` legend
// testid back the e2e locators (never color-class locators — the beta.13 e2e lesson).

// One plotted sample. avg is the line; min/max (optional) draw a soft band. avg/min/max may be null
// to mark a GAP — with connectNulls={false} the line/band break there instead of bridging the gap.
export interface TimeSeriesPoint {
  t: number | string;
  avg: number | null;
  min?: number | null;
  max?: number | null;
}

// One series (line + its band). color is an optional CSS var() token override; unit is appended to
// values in the tooltip. data is this series' own point list (series may share timestamps or not —
// the chart merges them by t).
export interface TimeSeriesSeries {
  key: string;
  label: string;
  color?: string;
  unit: string;
  data: TimeSeriesPoint[];
}

export interface TimeSeriesChartProps {
  series: TimeSeriesSeries[];
  ariaLabel?: string;
  height?: number;
  // yDomain passes straight to recharts' YAxis domain (e.g. [0, 100] for a percent metric); default
  // ['auto','auto'] lets recharts fit the data.
  yDomain?: [number | 'auto' | 'dataMin' | 'dataMax', number | 'auto' | 'dataMin' | 'dataMax'];
  // An explicit request window keeps leading/trailing data loss visible. allowDataOverflow below
  // clips an epoch-aligned bucket whose start falls just before the requested from boundary.
  xDomain?: [number, number];
  language: UILanguage;
}

// The default series color cycle — token families that already follow the theme. A series' own
// `color` overrides its slot.
const PALETTE = ['var(--accent)', 'var(--info)', 'var(--success)', 'var(--warning)', 'var(--danger)'];

// A merged chart row: the shared epoch-ms `t` plus, per series, `<key>__avg` (the line value) and
// `<key>__band` ([min,max] tuple, or null at a gap / when the series carries no band).
type ChartRow = { t: number; [seriesField: string]: number | [number, number] | null };

// buildRows merges the per-series point lists into one row array keyed by timestamp, so recharts can
// share a single X axis across every series (the load chart's three lines) and single-series charts
// fall out trivially. A NaN timestamp is skipped.
function buildRows(series: TimeSeriesSeries[]): ChartRow[] {
  const byT = new Map<number, ChartRow>();
  for (const s of series) {
    for (const pt of s.data) {
      const t = typeof pt.t === 'number' ? pt.t : Date.parse(pt.t);
      if (Number.isNaN(t)) continue;
      let row = byT.get(t);
      if (!row) {
        row = { t };
        byT.set(t, row);
      }
      row[`${s.key}__avg`] = pt.avg;
      const min = pt.min ?? null;
      const max = pt.max ?? null;
      row[`${s.key}__band`] = min !== null && max !== null ? [min, max] : null;
    }
  }
  return [...byT.values()].sort((a, b) => a.t - b.t);
}

// hasBand reports whether a series carries any min/max pair (so we only render an Area for series
// that actually have a band — load has avg only, cpu/mem have min/max).
function hasBand(s: TimeSeriesSeries): boolean {
  return s.data.some((p) => p.min != null && p.max != null);
}

// ChartTooltip is passed to <Tooltip content={…}> as an ELEMENT; recharts clones it and injects
// active/payload/label. It reads the merged row (payload[0].payload) and renders each series' avg +
// unit, all in theme tokens. A gap point (null avg) is omitted from the readout.
function ChartTooltip(props: {
  active?: boolean;
  payload?: Array<{ payload?: ChartRow }>;
  label?: number | string;
  series: TimeSeriesSeries[];
  language: UILanguage;
}): ReactNode {
  const { active, payload, label, series, language } = props;
  if (!active || !payload || payload.length === 0) return null;
  const row = payload[0]?.payload;
  if (!row) return null;
  const ms = typeof label === 'number' ? label : Number(label);
  const when = Number.isNaN(ms)
    ? ''
    : new Date(ms).toLocaleString(language, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  return (
    <div className="rounded border border-[var(--hairline)] bg-[var(--surface-elevated)] px-2 py-1 text-xs shadow">
      {when && <div className="mb-0.5 text-[var(--content-muted)]">{when}</div>}
      {series.map((s, i) => {
        const v = row[`${s.key}__avg`];
        if (typeof v !== 'number') return null;
        const color = s.color ?? PALETTE[i % PALETTE.length];
        return (
          <div key={s.key} className="flex items-center gap-1.5 text-[var(--content)]">
            <span className="inline-block h-2 w-2 rounded-full" style={{ backgroundColor: color }} />
            <span className="text-[var(--content-muted)]">{s.label}</span>
            <span className="font-mono">
              {v.toFixed(2)}
              {s.unit}
            </span>
          </div>
        );
      })}
    </div>
  );
}

export function TimeSeriesChart({ series, ariaLabel, height, yDomain, xDomain, language }: TimeSeriesChartProps) {
  const rows = buildRows(series);
  const descriptionID = useId();
  const axisTick = { fill: 'var(--content-muted)', fontSize: 11 };
  const fmtTime = (v: number | string): string => formatTimeSeriesTick(v, language, xDomain);
  const chartLabel = ariaLabel ?? t(language, 'timeSeries.chartLabel', {
    series: series.map((item) => item.label).join(', '),
  });
  const accessibleSummary = series.map((item) => {
    const averages = item.data.flatMap((point) => typeof point.avg === 'number' ? [point.avg] : []);
    if (averages.length === 0) {
      return t(language, 'timeSeries.noValues', { series: item.label });
    }
    const minima = item.data.flatMap((point) =>
      typeof point.min === 'number' ? [point.min] : typeof point.avg === 'number' ? [point.avg] : []);
    const maxima = item.data.flatMap((point) =>
      typeof point.max === 'number' ? [point.max] : typeof point.avg === 'number' ? [point.avg] : []);
    const format = (value: number) => `${value.toLocaleString(language, { maximumFractionDigits: 2 })}${item.unit}`;
    return t(language, 'timeSeries.summary', {
      series: item.label,
      latest: format(averages[averages.length - 1]),
      min: format(Math.min(...minima)),
      max: format(Math.max(...maxima)),
    });
  }).join(' ');

  return (
    <div
      data-testid="timeseries-chart"
      className="w-full"
      role="img"
      aria-label={chartLabel}
      aria-describedby={descriptionID}
    >
      <span id={descriptionID} className="sr-only">{accessibleSummary}</span>
      {/* Legend doubles as the per-series testid surface (color swatch via inline var() token). */}
      <div className="mb-1 flex flex-wrap gap-x-4 gap-y-1 text-xs text-[var(--content-muted)]">
        {series.map((s, i) => {
          const color = s.color ?? PALETTE[i % PALETTE.length];
          return (
            <span key={s.key} data-testid={`timeseries-series-${s.key}`} className="flex items-center gap-1.5">
              <span className="inline-block h-2 w-2 rounded-full" style={{ backgroundColor: color }} />
              {s.label}
              {s.unit ? ` (${s.unit})` : ''}
            </span>
          );
        })}
      </div>
      <ResponsiveContainer width="100%" height={height ?? 200}>
        <ComposedChart data={rows} margin={{ top: 4, right: 8, bottom: 0, left: -8 }}>
          <CartesianGrid stroke="var(--hairline)" strokeDasharray="3 3" vertical={false} />
          <XAxis
            dataKey="t"
            type="number"
            scale="time"
            domain={xDomain ?? ['dataMin', 'dataMax']}
            allowDataOverflow={xDomain !== undefined}
            tickFormatter={fmtTime}
            tick={axisTick}
            stroke="var(--hairline)"
            minTickGap={40}
          />
          <YAxis domain={yDomain ?? ['auto', 'auto']} tick={axisTick} stroke="var(--hairline)" width={44} />
          <Tooltip
            cursor={{ stroke: 'var(--hairline)' }}
            content={<ChartTooltip series={series} language={language} />}
          />
          {/* Bands first so the avg lines draw on top. */}
          {series.map((s, i) =>
            hasBand(s) ? (
              <Area
                key={`${s.key}__band`}
                dataKey={`${s.key}__band`}
                stroke="none"
                fill={s.color ?? PALETTE[i % PALETTE.length]}
                fillOpacity={0.14}
                connectNulls={false}
                isAnimationActive={false}
                activeDot={false}
                legendType="none"
              />
            ) : null,
          )}
          {series.map((s, i) => {
            const color = s.color ?? PALETTE[i % PALETTE.length];
            const valueKey = `${s.key}__avg`;
            // Recharts plots every series against the merged row set. Derive singleton runs from
            // that same shape so a timestamp contributed only by another series is correctly a gap.
            const isolated = isolatedPointTimes({
              data: rows.map((row) => ({
                t: row.t,
                avg: typeof row[valueKey] === 'number' ? row[valueKey] : null,
              })),
            });
            const dot = isolated.size === 0
              ? false
              : (props: DotItemDotProps) => {
                  const t = Number(props.payload?.t);
                  if (
                    !isolated.has(t) ||
                    typeof props.cx !== 'number' ||
                    typeof props.cy !== 'number'
                  ) {
                    return null;
                  }
                  return (
                    <circle
                      cx={props.cx}
                      cy={props.cy}
                      r={3}
                      fill={color}
                      stroke={color}
                      pointerEvents="none"
                    />
                  );
                };
            return (
              <Line
                key={`${s.key}__avg`}
                type="monotone"
                dataKey={valueKey}
                name={s.label}
                stroke={color}
                strokeWidth={2}
                dot={dot}
                connectNulls={false}
                isAnimationActive={false}
              />
            );
          })}
        </ComposedChart>
      </ResponsiveContainer>
    </div>
  );
}
