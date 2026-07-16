import { t, type UILanguage } from '../../i18n';
import { summarizeProbeResults } from '../../lib/probeResults';
import type { TelemetryProbeResult } from '../../types/controller';
import type { TelemetryProbe } from '../../types/topology';

export function TelemetryProbeSummary({
  configured,
  results,
  language,
}: {
  configured: readonly TelemetryProbe[];
  results: readonly TelemetryProbeResult[];
  language: UILanguage;
}) {
  const summary = summarizeProbeResults(
    configured,
    results,
  );
  if (summary.state === 'none') {
    return <span className="text-[var(--content-muted)]">—</span>;
  }
  if (summary.state === 'failure') {
    return (
      <span className="text-xs font-medium text-[var(--danger)]">
        {t(language, 'telemetryProbes.summaryFailure', {
          failure: summary.failure,
          total: summary.total,
        })}
      </span>
    );
  }
  if (summary.state === 'pending') {
    return (
      <span className="text-xs font-medium text-[var(--warning)]">
        {t(language, 'telemetryProbes.summaryPending', {
          pending: summary.pending,
          total: summary.total,
        })}
      </span>
    );
  }
  return (
    <span className="text-xs font-medium text-[var(--success)]">
      {t(language, 'telemetryProbes.summarySuccess', { total: summary.total })}
    </span>
  );
}
