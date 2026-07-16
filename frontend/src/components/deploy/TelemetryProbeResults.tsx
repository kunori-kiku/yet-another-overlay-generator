import { t, type MessageKey, type UILanguage } from '../../i18n';
import type {
  TelemetryProbeFailureReason,
  TelemetryProbeResult,
  TelemetryProbeResultStatus,
} from '../../types/controller';
import type { TelemetryProbe } from '../../types/topology';
import { formatProbeTarget, probeDisplayName, probeResultMatchesPolicy } from '../../lib/probeResults';

const FAILURE_KEYS: Record<TelemetryProbeFailureReason, MessageKey> = {
  dns_failed: 'telemetryProbes.failure.dnsFailed',
  timeout: 'telemetryProbes.failure.timeout',
  permission_denied: 'telemetryProbes.failure.permissionDenied',
  connection_refused: 'telemetryProbes.failure.connectionRefused',
  network_unreachable: 'telemetryProbes.failure.networkUnreachable',
  network_error: 'telemetryProbes.failure.networkError',
};

const STATUS_KEYS: Record<TelemetryProbeResultStatus, MessageKey> = {
  pending: 'telemetryProbes.status.pending',
  success: 'telemetryProbes.status.success',
  failure: 'telemetryProbes.status.failure',
};

function fmtTime(iso: string | undefined, language: UILanguage): string {
  if (!iso) return '—';
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString(language);
}


function statusClass(status: TelemetryProbeResultStatus | 'not_reported'): string {
  switch (status) {
    case 'success':
      return 'border-[var(--success-border)] bg-[var(--success-bg)] text-[var(--success)]';
    case 'failure':
      return 'border-[var(--danger-border)] bg-[var(--danger-bg)] text-[var(--danger)]';
    default:
      return 'border-[var(--warning-border)] bg-[var(--warning-bg)] text-[var(--warning)]';
  }
}

type Row = {
  key: string;
  id: string;
  name?: string;
  type: TelemetryProbe['type'];
  host: string;
  port?: number;
  result?: TelemetryProbeResult;
  retired: boolean;
};

export function TelemetryProbeResults({
  configured,
  results,
  language,
}: {
  configured: readonly TelemetryProbe[];
  results: readonly TelemetryProbeResult[];
  language: UILanguage;
}) {
  const byID = new Map(results.map((result) => [result.id, result]));
  const configuredByID = new Map(configured.map((probe) => [probe.id, probe]));
  const rows: Row[] = configured.map((probe) => ({
    key: `configured:${probe.id}`,
    id: probe.id,
    name: probe.name,
    type: probe.type,
    host: probe.host,
    port: probe.port,
    result: (() => {
      const candidate = byID.get(probe.id);
      return candidate && probeResultMatchesPolicy(probe, candidate) ? candidate : undefined;
    })(),
    retired: false,
  }));
  for (const result of results) {
    const configuredProbe = configuredByID.get(result.id);
    if (!configuredProbe || !probeResultMatchesPolicy(configuredProbe, result)) {
      rows.push({
        key: `reported:${result.id}:${result.type}:${result.host}:${result.port ?? ''}`,
        id: result.id,
        name: configuredProbe?.name,
        type: result.type,
        host: result.host,
        port: result.port,
        result,
        retired: true,
      });
    }
  }

  return (
    <section
      className="space-y-3 rounded-lg border border-[var(--hairline)] bg-[var(--surface)] p-3"
      data-testid="telemetry-probe-results"
    >
      <div>
        <h3 className="text-sm font-semibold text-[var(--content)]">
          {t(language, 'telemetryProbes.resultsHeading')}
        </h3>
        <p className="mt-1 text-xs text-[var(--content-muted)]">
          {t(language, 'telemetryProbes.resultsDescription')}
        </p>
      </div>
      {rows.length === 0 ? (
        <p className="text-sm text-[var(--content-muted)]">
          {t(language, 'telemetryProbes.noneConfigured')}
        </p>
      ) : (
        <div className="space-y-2">
          {rows.map((row) => {
            const status = row.result?.status ?? 'not_reported';
            const displayName = probeDisplayName(row);
            const hasDisplayName = displayName !== row.id;
            const statusLabel =
              status === 'not_reported'
                ? t(language, 'telemetryProbes.status.notReported')
                : t(language, STATUS_KEYS[status]);
            const reason = row.result?.failureReason
              ? t(language, FAILURE_KEYS[row.result.failureReason])
              : '—';
            return (
              <article
                key={row.key}
                className="rounded border border-[var(--hairline)] bg-[var(--surface-elevated)] p-3"
                data-testid={`telemetry-probe-result-${row.id}`}
              >
                <div className="flex flex-wrap items-start justify-between gap-2">
                  <div>
                    <div className="text-sm font-medium text-[var(--content)]">
                      {displayName}
                    </div>
                    <div className="mt-0.5 font-mono text-xs text-[var(--content)]">
                      {formatProbeTarget(row.host, row.port)}
                    </div>
                    <div className="mt-0.5 text-xs text-[var(--content-muted)]">
                      {row.type === 'tcp'
                        ? t(language, 'telemetryProbes.tcp')
                        : t(language, 'telemetryProbes.icmp')}
                      {hasDisplayName && <> · {row.id}</>}
                      {row.retired && (
                        <span className="ml-1 text-[var(--warning)]">
                          {t(language, 'telemetryProbes.reportedOnly')}
                        </span>
                      )}
                    </div>
                  </div>
                  <span className={`rounded border px-2 py-0.5 text-xs font-medium ${statusClass(status)}`}>
                    {statusLabel}
                  </span>
                </div>
                <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
                  <dt className="text-[var(--content-muted)]">{t(language, 'telemetryProbes.latency')}</dt>
                  <dd className="font-mono text-[var(--content)]">
                    {row.result?.latencyMS === undefined
                      ? '—'
                      : `${row.result.latencyMS.toFixed(1)} ms`}
                  </dd>
                  <dt className="text-[var(--content-muted)]">
                    {t(language, 'telemetryProbes.lastChecked')}
                  </dt>
                  <dd className="text-[var(--content)]">{fmtTime(row.result?.checkedAt, language)}</dd>
                  {status === 'failure' && (
                    <>
                      <dt className="text-[var(--content-muted)]">
                        {t(language, 'telemetryProbes.failureReason')}
                      </dt>
                      <dd className="text-[var(--danger)]">{reason}</dd>
                    </>
                  )}
                </dl>
              </article>
            );
          })}
        </div>
      )}
    </section>
  );
}
