import { useEffect, useState, type ComponentType } from 'react';
import { Link } from 'react-router-dom';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t, type MessageKey, type UILanguage } from '../../i18n';
import { TimeSeriesChart, type TimeSeriesSeries } from '../charts/TimeSeriesChart';
import {
  effectiveExpectedStatus,
  formatProbeDestination,
  probeDisplayName,
} from '../../lib/probeResults';
import {
  GRANULARITIES,
  HISTORY_CHART_FAMILIES,
  RANGE_PRESETS,
  createLatestRequestCoordinator,
  createObservedRequestScheduler,
  configuredHistoryProbeSelector,
  formatHistoryResolution,
  granularityStep,
  historyRefreshFailed,
  historyRefreshIdle,
  historyRefreshStarted,
  historyRefreshSucceeded,
  initialHistoryRefreshViewState,
  deviceMetricSeries,
  metricSeries,
  parseGoDuration,
  probeAvailabilitySeries,
  probeHistoryFallbackIntervalMS,
  probeHistoryMatchesPolicy,
  probeLatencySeries,
  rangeWindow,
  resolutionWasWidened,
  summarizeProbeFailures,
} from '../../lib/telemetryHistory';
import type {
  Granularity,
  HistoryChartFamily,
  NodeHistory,
  NodeHistoryRequestOptions,
  RangePreset,
} from '../../lib/telemetryHistory';
import type { TelemetryProbeFailureReason } from '../../types/controller';
import type { TelemetryProbe } from '../../types/topology';
import {
  DEVICE_NUMERIC_DEFINITIONS,
  type DeviceInventoryMetric,
  type DeviceNumericKey,
} from '../../lib/deviceTelemetry';

// NodeResourceHistory retains its historical exported name, but now renders the complete node
// telemetry-history response: CPU/RAM/load, the selected CURRENT configured probe's latency,
// availability and failures, plus one exact detected device's numeric charts. One range/resolution
// picker drives every family. The response is
// component-local and NEVER persisted (the stripLiveTelemetry custody rule).

// A percent metric pins the Y axis to 0..100 so a flat-ish series is not visually exaggerated; load
// has no natural ceiling, so it auto-fits.
const PERCENT_DOMAIN: [number, number] = [0, 100];

const FAILURE_KEYS: Record<TelemetryProbeFailureReason, MessageKey> = {
  dns_failed: 'telemetryProbes.failure.dnsFailed',
  timeout: 'telemetryProbes.failure.timeout',
  permission_denied: 'telemetryProbes.failure.permissionDenied',
  connection_refused: 'telemetryProbes.failure.connectionRefused',
  network_unreachable: 'telemetryProbes.failure.networkUnreachable',
  network_error: 'telemetryProbes.failure.networkError',
  unexpected_status: 'telemetryProbes.failure.unexpectedStatus',
};

const DEVICE_HISTORY_METRIC_KEYS = {
  disk_filesystem_used_pct: 'telemetryDevices.metric.filesystemUsed',
  disk_read_bytes_per_second: 'telemetryDevices.metric.readRate',
  disk_write_bytes_per_second: 'telemetryDevices.metric.writeRate',
  disk_io_busy_pct: 'telemetryDevices.metric.ioBusy',
  gpu_utilization_pct: 'telemetryDevices.metric.gpuUtilization',
  gpu_vram_used_pct: 'telemetryDevices.metric.vramUsed',
} satisfies Record<DeviceNumericKey, MessageKey>;

const DEVICE_HISTORY_COLORS: Record<DeviceNumericKey, string> = {
  disk_filesystem_used_pct: 'var(--success)',
  disk_read_bytes_per_second: 'var(--info)',
  disk_write_bytes_per_second: 'var(--accent)',
  disk_io_busy_pct: 'var(--warning)',
  gpu_utilization_pct: 'var(--accent)',
  gpu_vram_used_pct: 'var(--info)',
};

function failureReasonLabel(reason: string, language: UILanguage): string {
  if (reason === 'uncategorized') return t(language, 'nodeHistory.probeFailureUncategorized');
  const key = FAILURE_KEYS[reason as TelemetryProbeFailureReason];
  return key ? t(language, key) : reason.replaceAll('_', ' ');
}

interface NodeResourceHistoryProps {
  nodeId: string;
  deviceInventory?: DeviceInventoryMetric;
  deviceTelemetryEnabled?: boolean;
  // A change in this node's last_seen means fresh telemetry was actually received. It is a trigger
  // only; history stays component-local and is never copied into the persisted controller store.
  refreshAt?: string | number | null;
}

interface HistoryLoadQuery {
  requestKey: string;
  observationKey: string;
  nodeId: string;
  from: string;
  to: string;
  step?: string;
  options: Omit<NodeHistoryRequestOptions, 'signal'>;
  window: [number, number];
  requestedGranularity: Granularity;
}

export interface HistoryChartFamilySectionProps {
  history: NodeHistory;
  stepMs: number;
  xDomain?: [number, number];
  language: UILanguage;
  configuredProbes: readonly TelemetryProbe[];
  selectedProbeID: string | null;
  onSelectProbeID: (id: string) => void;
  deviceInventory?: DeviceInventoryMetric;
  deviceTelemetryEnabled: boolean;
  selectedDeviceID: string | null;
  onSelectDeviceID: (id: string) => void;
}

// This is the production dispatch table, not a documentation-only manifest. The shared family
// literal drives iteration below and `satisfies` makes adding a family fail the frontend build until
// a concrete section renderer is registered. The rendered-fixture test additionally rejects a
// registered no-op that never reaches TimeSeriesChart.
const HISTORY_CHART_RENDERERS = {
  resource: ResourceHistorySection,
  probe: ProbeHistorySection,
  device: DeviceHistorySection,
} satisfies Record<HistoryChartFamily, ComponentType<HistoryChartFamilySectionProps>>;

export function HistoryChartFamilySection({
  family,
  ...props
}: HistoryChartFamilySectionProps & { family: HistoryChartFamily }) {
  const Renderer = HISTORY_CHART_RENDERERS[family];
  return <Renderer {...props} />;
}

export function NodeResourceHistory({
  nodeId,
  deviceInventory,
  deviceTelemetryEnabled = false,
  refreshAt,
}: NodeResourceHistoryProps) {
  const language = useTopologyStore((s) => s.language);
  const topologyNode = useTopologyStore((s) => s.nodes.find((node) => node.id === nodeId));
  const configuredProbes = topologyNode?.telemetry_probes ?? [];
  const fetchNodeHistory = useControllerStore((s) => s.fetchNodeHistory);

  const [range, setRange] = useState<RangePreset>('6h');
  const [granularity, setGranularity] = useState<Granularity>('auto');
  const [selectedProbeID, setSelectedProbeID] = useState<string | null>(configuredProbes[0]?.id ?? null);
  const [selectedDeviceID, setSelectedDeviceID] = useState<string | null>(
    deviceInventory?.devices[0]?.seriesId ?? null,
  );
  const [retryNonce, setRetryNonce] = useState(0);
  const [displayedGranularity, setDisplayedGranularity] = useState<Granularity | null>(null);
  // Keep the response and its exact request window together. The reducer-style helpers make the
  // last-good-on-failure invariant explicit and unit-testable without persisting any live data.
  const [refreshState, setRefreshState] = useState(initialHistoryRefreshViewState);
  const { history, window: historyWindow, updating, error, lastUpdatedAt } = refreshState;

  const [requestCoordinator] = useState(() => createLatestRequestCoordinator<HistoryLoadQuery, NodeHistory>({
    key: (query) => query.requestKey,
    execute: (query, signal) => fetchNodeHistory(
      query.nodeId,
      query.from,
      query.to,
      query.step,
      { ...query.options, signal },
    ),
    onStart: () => {
      setRefreshState(historyRefreshStarted);
    },
    onSuccess: (nextHistory, query) => {
      setDisplayedGranularity(query.requestedGranularity);
      setRefreshState((state) => historyRefreshSucceeded(state, nextHistory, query.window, Date.now()));
    },
    onError: () => {
      // Keep the last successful chart/window on screen; a short polling or CDN gap should look
      // stale-with-warning, not like the node lost all retained history.
      setRefreshState(historyRefreshFailed);
    },
    onIdle: () => setRefreshState(historyRefreshIdle),
  }));

  const [requestScheduler] = useState(() => createObservedRequestScheduler<HistoryLoadQuery>({
    observationKey: (query) => query.observationKey,
    request: (query) => requestCoordinator.request(query),
    dispose: () => requestCoordinator.dispose(),
  }));

  useEffect(() => () => requestScheduler.dispose(), [requestScheduler]);

  const selectedProbe = configuredProbes.find((probe) => probe.id === selectedProbeID) ?? configuredProbes[0];
  // Fleet first paints from its persistence-safe cache, which deliberately has no device inventory.
  // Derive the first live row as a fallback so the first authenticated refresh activates the exact
  // selector without synchronizing a second copy of inventory into component state.
  const selectedDevice = deviceInventory?.devices.find((device) => device.seriesId === selectedDeviceID)
    ?? deviceInventory?.devices[0];
  // Fetch on mount, a parameter/exact-selector change, or a node-specific telemetry receipt. The
  // coordinator permits one request at a time, aborts a superseded key, and retains only the latest
  // same-key Live tick for a follow-up. Its callbacks are microtask-delivered, outside effect setup.
  useEffect(() => {
    const { from, to } = rangeWindow(range, Date.now());
    const selector = configuredHistoryProbeSelector(selectedProbe);
    const selectorKey = selector
      ? selector.type === 'url'
        ? `${selector.id}\u0000url\u0000${selector.url}\u0000${selector.expectedStatus}`
        : `${selector.id}\u0000${selector.type}\u0000${selector.host}\u0000${
          selector.type === 'tcp' ? selector.port : ''
        }`
      : 'resource-only';
    const deviceSelectorKey = selectedDevice
      ? `${selectedDevice.kind}\u0000${selectedDevice.seriesId}`
      : 'no-device';
    const requestKey = `${nodeId}\u0000${range}\u0000${granularity}\u0000${selectorKey}\u0000${deviceSelectorKey}`;
    const options: NodeHistoryRequestOptions = {
      ...(selector ? { probe: selector } : { includeProbes: false }),
      ...(selectedDevice
        ? { device: { kind: selectedDevice.kind, deviceId: selectedDevice.seriesId } }
        : {}),
    };
    requestScheduler.observe({
      requestKey,
      observationKey: `${requestKey}\u0000${String(refreshAt ?? '')}\u0000retry-${retryNonce}`,
      nodeId,
      from,
      to,
      step: granularityStep(granularity),
      options,
      window: [Date.parse(from), Date.parse(to)],
      requestedGranularity: granularity,
    });
  }, [
    nodeId,
    range,
    granularity,
    refreshAt,
    retryNonce,
    requestScheduler,
    selectedProbe,
    selectedDevice,
  ]);

  const stepMs = parseGoDuration(history?.step ?? '');
  const xDomain = historyWindow ?? undefined;
  const updatedAtLabel = lastUpdatedAt === null
    ? ''
    : new Date(lastUpdatedAt).toLocaleTimeString(language, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  const effectiveResolution = history?.step ? formatHistoryResolution(history.step) : '';
  const resolutionWidened = history !== null && displayedGranularity !== null &&
    resolutionWasWidened(displayedGranularity, history.step);
  const retryHistory = () => setRetryNonce((nonce) => nonce + 1);

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
        <div className="text-sm font-medium text-[var(--content)]">{t(language, 'nodeHistory.heading')}</div>
        <div className="flex flex-wrap items-center gap-3">
          {/* Range presets */}
          <div className="flex items-center gap-1.5">
            <span className="text-xs text-[var(--content-muted)]">{t(language, 'nodeHistory.rangeLabel')}</span>
            <div className="flex w-fit items-center overflow-hidden rounded border border-[var(--hairline)] bg-[var(--control)]">
              {RANGE_PRESETS.map((p) => (
                <button
                  key={p}
                  type="button"
                  onClick={() => setRange(p)}
                  aria-pressed={range === p}
                  data-testid={`history-range-${p}`}
                  className={segClass(range === p)}
                >
                  {p}
                </button>
              ))}
            </div>
          </div>
          {/* Resolution */}
          <label className="flex items-center gap-1.5 text-xs text-[var(--content-muted)]">
            {t(language, 'nodeHistory.granularityLabel')}
            <select
              value={granularity}
              onChange={(e) => setGranularity(e.target.value as Granularity)}
              aria-describedby="history-resolution-hint"
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
      <p
        id="history-resolution-hint"
        className="mt-1 text-xs text-[var(--content-muted)]"
        data-testid="history-resolution-hint"
      >
        {t(language, 'nodeHistory.granularityHint')}
      </p>

      {history && (
        <div className="mt-2 min-h-4 text-xs" data-testid="history-refresh-feedback">
          {updating ? (
            <p
              className="flex items-center gap-1.5 text-[var(--content-muted)]"
              data-testid="history-updating"
            >
              <span
                className="inline-block h-2.5 w-2.5 rounded-full border border-[var(--content-muted)] border-t-transparent motion-safe:animate-spin motion-reduce:animate-none"
                aria-hidden="true"
              />
              {t(language, 'nodeHistory.updating')}
            </p>
          ) : error ? (
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-[var(--warning)]" data-testid="history-update-failed" role="status">
                {t(language, 'nodeHistory.updateFailedShowingLast')}
              </p>
              <button
                type="button"
                onClick={retryHistory}
                data-testid="history-retry-retained"
                className="rounded border border-[var(--hairline)] px-2 py-0.5 text-[var(--info)] hover:bg-[var(--control)]"
              >
                {t(language, 'nodeHistory.retry')}
              </button>
            </div>
          ) : lastUpdatedAt !== null ? (
            <p className="text-[var(--content-muted)]" data-testid="history-updated">
              {t(language, 'nodeHistory.updatedAt', { time: updatedAtLabel })}
            </p>
          ) : null}
        </div>
      )}

      {history && !history.disabled && effectiveResolution && (
        <p
          className="mt-1 text-xs text-[var(--content-muted)]"
          data-testid="history-effective-resolution"
          role="status"
        >
          {resolutionWidened && displayedGranularity !== null
            ? t(language, 'nodeHistory.effectiveResolutionWidened', {
                effective: effectiveResolution,
                requested: displayedGranularity,
              })
            : t(language, 'nodeHistory.effectiveResolution', { resolution: effectiveResolution })}
        </p>
      )}

      {/* Shared states: disabled > error > first-load. Resource and probe empty states are separate
          because either side may have data while the other legitimately does not. */}
      {!history && updating ? (
        <p className="mt-2 text-xs text-[var(--content-muted)]" data-testid="history-loading" role="status">
          {t(language, 'nodeHistory.loading')}
        </p>
      ) : !history && error ? (
        <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
          <p className="text-[var(--danger)]" data-testid="history-error" role="alert">
            {t(language, 'nodeHistory.error')}
          </p>
          <button
            type="button"
            onClick={retryHistory}
            data-testid="history-retry-initial"
            className="rounded border border-[var(--hairline)] px-2 py-0.5 text-[var(--info)] hover:bg-[var(--control)]"
          >
            {t(language, 'nodeHistory.retry')}
          </button>
        </div>
      ) : history?.disabled ? (
        <p className="mt-2 text-xs text-[var(--content-muted)]" data-testid="history-disabled">
          {t(language, 'nodeHistory.disabled')}{' '}
          <Link to="/settings" className="text-[var(--info)] hover:underline">
            {t(language, 'nodeHistory.disabledCta')}
          </Link>
        </p>
      ) : (
        <div className="mt-3 space-y-5">
          {HISTORY_CHART_FAMILIES.map((family) => {
            return (
              <HistoryChartFamilySection
                key={family}
                family={family}
                history={history ?? { step: '', disabled: false, buckets: [], probes: [], devices: [] }}
                stepMs={stepMs}
                xDomain={xDomain}
                language={language}
                configuredProbes={configuredProbes}
                selectedProbeID={selectedProbeID}
                onSelectProbeID={setSelectedProbeID}
                deviceInventory={deviceInventory}
                deviceTelemetryEnabled={deviceTelemetryEnabled}
                selectedDeviceID={selectedDeviceID}
                onSelectDeviceID={setSelectedDeviceID}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}

function ResourceHistorySection({ history, stepMs, xDomain, language }: HistoryChartFamilySectionProps) {
  const buckets = history.buckets;
  const cpuSeries: TimeSeriesSeries[] = [{
    key: 'cpu',
    label: t(language, 'nodeHistory.cpuSeries'),
    unit: '%',
    color: 'var(--accent)',
    data: metricSeries(buckets, stepMs, (bucket) => bucket.cpuPct),
  }];
  const loadSeries: TimeSeriesSeries[] = [
    { key: 'load1', label: t(language, 'nodeHistory.load1'), unit: '', color: 'var(--accent)', data: metricSeries(buckets, stepMs, (bucket) => bucket.load1) },
    { key: 'load5', label: t(language, 'nodeHistory.load5'), unit: '', color: 'var(--info)', data: metricSeries(buckets, stepMs, (bucket) => bucket.load5) },
    { key: 'load15', label: t(language, 'nodeHistory.load15'), unit: '', color: 'var(--success)', data: metricSeries(buckets, stepMs, (bucket) => bucket.load15) },
  ];
  const memSeries: TimeSeriesSeries[] = [{
    key: 'mem',
    label: t(language, 'nodeHistory.memSeries'),
    unit: '%',
    color: 'var(--success)',
    data: metricSeries(buckets, stepMs, (bucket) => bucket.memUsedPct),
  }];

  return (
    <section className="space-y-4" data-testid="resource-history-section">
      <h4 className="text-xs font-semibold text-[var(--content)]">
        {t(language, 'nodeHistory.resourceHeading')}
      </h4>
      {buckets.length === 0 ? (
        <p className="text-xs text-[var(--content-muted)]" data-testid="history-empty">
          {t(language, 'nodeHistory.empty')}
        </p>
      ) : (
        <>
          <HistoryChart title={t(language, 'nodeHistory.cpuTitle')} series={cpuSeries} yDomain={PERCENT_DOMAIN} xDomain={xDomain} language={language} />
          <HistoryChart title={t(language, 'nodeHistory.loadTitle')} series={loadSeries} xDomain={xDomain} language={language} />
          <HistoryChart title={t(language, 'nodeHistory.memTitle')} series={memSeries} yDomain={PERCENT_DOMAIN} xDomain={xDomain} language={language} />
        </>
      )}
    </section>
  );
}

function ProbeHistorySection({
  history,
  stepMs,
  xDomain,
  language,
  configuredProbes,
  selectedProbeID,
  onSelectProbeID,
}: HistoryChartFamilySectionProps) {
  // The select is driven only by CURRENT configured probes. If the draft changed the destination
  // under an existing id, the exact matcher intentionally refuses the old series instead of
  // presenting it as evidence about the new target.
  const selectedProbe = configuredProbes.find((probe) => probe.id === selectedProbeID) ?? configuredProbes[0];
  const selectedProbeHistory = selectedProbe
    ? history.probes.find((series) => probeHistoryMatchesPolicy(selectedProbe, series))
    : undefined;
  const probeBuckets = selectedProbeHistory?.buckets ?? [];
  // Prefer the controller-observed cadence for the exact deployed series. rc.9 did not return it, so
  // only that legacy shape falls back to the exact current policy (60s is the signed default).
  // Updated controllers' per-bucket intervals still take precedence at each schedule boundary.
  const fallbackIntervalMS = probeHistoryFallbackIntervalMS(
    selectedProbeHistory?.intervalMS,
    selectedProbe?.interval_seconds,
  );
  const latencySeries: TimeSeriesSeries[] = [{
    key: 'probe-latency',
    label: t(language, 'nodeHistory.probeLatencySeries'),
    unit: 'ms',
    color: 'var(--info)',
    data: probeLatencySeries(probeBuckets, stepMs, fallbackIntervalMS),
  }];
  const availabilitySeries: TimeSeriesSeries[] = [{
    key: 'probe-availability',
    label: t(language, 'nodeHistory.probeAvailabilitySeries'),
    unit: '%',
    color: 'var(--success)',
    data: probeAvailabilitySeries(probeBuckets, stepMs, fallbackIntervalMS),
  }];
  const failureSummary = summarizeProbeFailures(probeBuckets);
  const hasLatency = latencySeries[0].data.some((point) => typeof point.avg === 'number');
  const hasAvailability = availabilitySeries[0].data.some((point) => typeof point.avg === 'number');

  return (
    <section
      className="space-y-4 border-t border-[var(--hairline)] pt-4"
      data-testid="probe-history-section"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-xs font-semibold text-[var(--content)]">
          {t(language, 'nodeHistory.probeHeading')}
        </h4>
        {configuredProbes.length > 0 && (
          <label className="flex items-center gap-1.5 text-xs text-[var(--content-muted)]">
            {t(language, 'nodeHistory.probeSelect')}
            <select
              value={selectedProbe?.id ?? ''}
              onChange={(event) => onSelectProbeID(event.target.value)}
              data-testid="history-probe-select"
              className="max-w-[min(28rem,70vw)] rounded border border-[var(--hairline)] bg-[var(--control)] px-1.5 py-1 text-xs text-[var(--content)] outline-none focus:border-[var(--accent)]"
            >
              {configuredProbes.map((probe) => {
                const displayName = probeDisplayName(probe);
                return (
                  <option key={probe.id} value={probe.id}>
                    {displayName}
                    {displayName !== probe.id ? ` · ${probe.id}` : ''}
                    {' · '}{probe.type.toUpperCase()} · {formatProbeDestination(probe)}
                    {probe.type === 'url' ? ` · ${effectiveExpectedStatus(probe)}` : ''}
                  </option>
                );
              })}
            </select>
          </label>
        )}
      </div>

      {configuredProbes.length === 0 ? (
        <p className="text-xs text-[var(--content-muted)]" data-testid="probe-history-not-configured">
          {t(language, 'nodeHistory.probeNotConfigured')}
        </p>
      ) : probeBuckets.length === 0 ? (
        <p className="text-xs text-[var(--content-muted)]" data-testid="probe-history-empty">
          {t(language, 'nodeHistory.probeEmpty')}
        </p>
      ) : (
        <>
          {hasLatency ? (
            <HistoryChart
              title={t(language, 'nodeHistory.probeLatencyTitle')}
              series={latencySeries}
              xDomain={xDomain}
              language={language}
            />
          ) : (
            <p className="text-xs text-[var(--content-muted)]" data-testid="probe-history-no-latency">
              {t(language, 'nodeHistory.probeNoLatency')}
            </p>
          )}
          {hasAvailability && (
            <HistoryChart
              title={t(language, 'nodeHistory.probeAvailabilityTitle')}
              series={availabilitySeries}
              yDomain={PERCENT_DOMAIN}
              xDomain={xDomain}
              language={language}
            />
          )}
          <div data-testid="probe-history-failures">
            <div className="mb-1 text-xs font-medium text-[var(--content-muted)]">
              {t(language, 'nodeHistory.probeFailuresTitle')}
            </div>
            {failureSummary.length === 0 ? (
              <p className="text-xs text-[var(--success)]">
                {t(language, 'nodeHistory.probeNoFailures')}
              </p>
            ) : (
              <ul className="space-y-1 text-xs text-[var(--content)]">
                {failureSummary.map(({ reason, count }) => (
                  <li key={reason} className="flex items-center justify-between gap-3">
                    <span>{failureReasonLabel(reason, language)}</span>
                    <span className="font-mono text-[var(--danger)]">{count}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </>
      )}
    </section>
  );
}

function DeviceHistorySection({
  history,
  stepMs,
  xDomain,
  language,
  deviceInventory,
  deviceTelemetryEnabled,
  selectedDeviceID,
  onSelectDeviceID,
}: HistoryChartFamilySectionProps) {
  const selectedDevice = deviceInventory?.devices.find((device) => device.seriesId === selectedDeviceID)
    ?? deviceInventory?.devices[0];
  const selectedHistory = selectedDevice
    ? history.devices.find((series) =>
        series.deviceId === selectedDevice.seriesId && series.kind === selectedDevice.kind)
    : undefined;
  const buckets = selectedHistory?.buckets ?? [];
  const definitions = selectedDevice
    ? DEVICE_NUMERIC_DEFINITIONS.filter((definition) => definition.kind === selectedDevice.kind)
    : [];

  return (
    <section
      className="space-y-4 border-t border-[var(--hairline)] pt-4"
      data-testid="device-history-section"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-xs font-semibold text-[var(--content)]">
          {t(language, 'nodeHistory.deviceHeading')}
        </h4>
        {(deviceInventory?.devices.length ?? 0) > 0 && (
          <label className="flex items-center gap-1.5 text-xs text-[var(--content-muted)]">
            {t(language, 'nodeHistory.deviceSelect')}
            <select
              value={selectedDevice?.seriesId ?? ''}
              onChange={(event) => onSelectDeviceID(event.target.value)}
              data-testid="history-device-select"
              className="max-w-[min(28rem,70vw)] rounded border border-[var(--hairline)] bg-[var(--control)] px-1.5 py-1 text-xs text-[var(--content)] outline-none focus:border-[var(--accent)]"
            >
              {deviceInventory?.devices.map((device) => (
                <option key={`${device.kind}:${device.seriesId}`} value={device.seriesId}>
                  {device.label} · {device.kind} · {device.seriesId}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>

      {!deviceInventory || deviceInventory.devices.length === 0 ? (
        <p className="text-xs text-[var(--content-muted)]" data-testid="device-history-not-configured">
          {deviceTelemetryEnabled
            ? t(language, 'nodeHistory.deviceWaiting')
            : t(language, 'nodeHistory.deviceNotConfigured')}
        </p>
      ) : buckets.length === 0 ? (
        <p className="text-xs text-[var(--content-muted)]" data-testid="device-history-empty">
          {t(language, 'nodeHistory.deviceEmpty')}
        </p>
      ) : (
        <div className="space-y-4">
          {definitions.map((definition) => {
            const label = t(language, DEVICE_HISTORY_METRIC_KEYS[definition.key]);
            const series: TimeSeriesSeries[] = [{
              key: definition.key,
              label,
              unit: definition.unit,
              color: DEVICE_HISTORY_COLORS[definition.key],
              data: deviceMetricSeries(buckets, stepMs, definition.key),
            }];
            return (
              <HistoryChart
                key={definition.key}
                title={label}
                series={series}
                yDomain={definition.unit === '%' ? PERCENT_DOMAIN : undefined}
                xDomain={xDomain}
                language={language}
              />
            );
          })}
        </div>
      )}
    </section>
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
      <TimeSeriesChart
        series={series}
        ariaLabel={title}
        yDomain={yDomain}
        xDomain={xDomain}
        height={180}
        language={language}
      />
    </div>
  );
}
