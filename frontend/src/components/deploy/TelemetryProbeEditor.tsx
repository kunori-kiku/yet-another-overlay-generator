import { t, type UILanguage } from '../../i18n';
import { uuid } from '../../lib/uuid';
import type { Node, TelemetryProbe } from '../../types/topology';

const MAX_PROBES = 16;
const DEFAULT_INTERVAL_SECONDS = 60;
const DEFAULT_TIMEOUT_MILLISECONDS = 2000;
const MAX_NAME_CODE_POINTS = 128;
// Match Go unicode.IsPrint: letters, marks, numbers, punctuation, symbols, and ASCII space only.
const PRINTABLE_NAME = /^[\p{L}\p{M}\p{N}\p{P}\p{S} ]*$/u;

interface Props {
  node: Node;
  keystonePinned: boolean | null;
  language: UILanguage;
  updateNode: (id: string, updates: Partial<Node>) => void;
}

// This is deliberately a closed policy editor, not a generic command or URL editor. The operator
// may enter one IP address or DNS hostname, choose ICMP echo or one TCP port, and set bounded
// scheduling values. The backend validates and signs the exact deployed policy.
export function TelemetryProbeEditor({
  node,
  keystonePinned,
  language,
  updateNode,
}: Props) {
  const probes = node.telemetry_probes ?? [];
  const manual = node.deployment_mode === 'manual';

  const replace = (next: TelemetryProbe[]) => {
    updateNode(node.id, { telemetry_probes: next.length ? next : undefined });
  };
  const patchProbe = (id: string, patch: Partial<TelemetryProbe>) => {
    replace(probes.map((probe) => (probe.id === id ? { ...probe, ...patch } : probe)));
  };
  const addProbe = () => {
    if (manual || probes.length >= MAX_PROBES) return;
    replace([
      ...probes,
      { id: `probe-${uuid()}`, type: 'icmp', host: '' },
    ]);
  };

  return (
    <details
      open
      className="rounded-lg border border-[var(--hairline)] bg-[var(--surface)] p-3"
      data-testid="telemetry-probe-editor"
    >
      <summary className="cursor-pointer text-sm font-semibold text-[var(--content)]">
        {t(language, 'telemetryProbes.heading')}
      </summary>
      <div className="mt-2 space-y-2">
        <p className="text-xs text-[var(--content-muted)]">
          {t(language, 'telemetryProbes.description')}
        </p>
        <p className="rounded border border-[var(--warning-border)] bg-[var(--warning-bg)] p-2 text-xs text-[var(--content)]">
          {t(language, 'telemetryProbes.destinationWarning')}
        </p>

        {manual ? (
          <p className="text-xs text-[var(--content-muted)]">
            {t(language, 'telemetryProbes.manualUnsupported')}
          </p>
        ) : (
          <>
            {keystonePinned === true ? (
              <p className="text-xs text-[var(--success)]" data-testid="telemetry-probes-signed">
                {t(language, 'telemetryProbes.signed')}
              </p>
            ) : keystonePinned === false ? (
              <p className="text-xs text-[var(--warning)]" data-testid="telemetry-probes-keystone-required">
                {t(language, 'telemetryProbes.keystoneRequired')}
              </p>
            ) : (
              <p className="text-xs text-[var(--content-muted)]">
                {t(language, 'telemetryProbes.keystoneChecking')}
              </p>
            )}

            {probes.map((probe) => {
              const rawName = probe.name ?? '';
              const nameInvalid = rawName !== rawName.trim() ||
                Array.from(rawName).length > MAX_NAME_CODE_POINTS ||
                !PRINTABLE_NAME.test(rawName);
              const nameErrorID = `telemetry-probe-${probe.id}-name-error`;
              return (
                <div key={probe.id} className="space-y-2 rounded border border-[var(--hairline)] p-2">
                  <label className="block text-xs text-[var(--content-muted)]">
                    {t(language, 'telemetryProbes.name')}
                    <input
                      type="text"
                      value={rawName}
                      autoComplete="off"
                      placeholder={t(language, 'telemetryProbes.namePlaceholder')}
                      onChange={(event) => patchProbe(probe.id, { name: event.target.value || undefined })}
                      onBlur={(event) => {
                        const trimmed = event.target.value.trim();
                        if (trimmed !== event.target.value) {
                          patchProbe(probe.id, { name: trimmed || undefined });
                        }
                      }}
                      aria-invalid={nameInvalid || undefined}
                      aria-describedby={nameInvalid ? nameErrorID : undefined}
                      className={`mt-1 w-full rounded border bg-[var(--control)] px-2 py-1 text-xs ${nameInvalid
                        ? 'border-[var(--danger-border)]'
                        : 'border-[var(--hairline)]'}`}
                    />
                  </label>
                  {nameInvalid && (
                    <p id={nameErrorID} role="alert" className="text-xs text-[var(--danger)]">
                      {t(language, 'telemetryProbes.nameInvalid')}
                    </p>
                  )}
                  <p className="break-all text-xs text-[var(--content-muted)]">
                    {t(language, 'telemetryProbes.id')}{' '}
                    <span className="font-mono">{probe.id}</span>
                  </p>
                  <div className="grid grid-cols-2 gap-2">
                    <label className="text-xs text-[var(--content-muted)]">
                      {t(language, 'telemetryProbes.type')}
                      <select
                        value={probe.type}
                        onChange={(event) => {
                          const type = event.target.value as TelemetryProbe['type'];
                          patchProbe(probe.id, {
                            type,
                            port: type === 'tcp' ? (probe.port ?? 443) : undefined,
                          });
                        }}
                        className="mt-1 w-full rounded border border-[var(--hairline)] bg-[var(--control)] px-2 py-1 text-xs"
                      >
                        <option value="icmp">{t(language, 'telemetryProbes.icmp')}</option>
                        <option value="tcp">{t(language, 'telemetryProbes.tcp')}</option>
                      </select>
                    </label>
                    <label className="text-xs text-[var(--content-muted)]">
                      {t(language, 'telemetryProbes.target')}
                      <input
                        type="text"
                        value={probe.host}
                        maxLength={253}
                        autoComplete="off"
                        spellCheck={false}
                        placeholder={t(language, 'telemetryProbes.hostPlaceholder')}
                        onChange={(event) => patchProbe(probe.id, { host: event.target.value })}
                        className="mt-1 w-full rounded border border-[var(--hairline)] bg-[var(--control)] px-2 py-1 text-xs"
                      />
                    </label>
                  </div>
                  <p className="text-xs text-[var(--content-muted)]">
                    {t(language, 'telemetryProbes.hostHint')}
                  </p>

                  <div className={`grid gap-2 ${probe.type === 'tcp' ? 'grid-cols-3' : 'grid-cols-2'}`}>
                    {probe.type === 'tcp' && (
                      <label className="text-xs text-[var(--content-muted)]">
                        {t(language, 'telemetryProbes.port')}
                        <input
                          type="number"
                          min={1}
                          max={65535}
                          value={probe.port ?? 443}
                          onChange={(event) =>
                            patchProbe(probe.id, {
                              port: Number.parseInt(event.target.value, 10) || 1,
                            })
                          }
                          className="mt-1 w-full rounded border border-[var(--hairline)] bg-[var(--control)] px-2 py-1 text-xs"
                        />
                      </label>
                    )}
                    <label className="text-xs text-[var(--content-muted)]">
                      {t(language, 'telemetryProbes.interval')}
                      <input
                        type="number"
                        min={30}
                        max={3600}
                        value={probe.interval_seconds ?? DEFAULT_INTERVAL_SECONDS}
                        onChange={(event) => {
                          const value = Number.parseInt(event.target.value, 10);
                          patchProbe(probe.id, {
                            interval_seconds:
                              !Number.isFinite(value) || value === DEFAULT_INTERVAL_SECONDS ? undefined : value,
                          });
                        }}
                        className="mt-1 w-full rounded border border-[var(--hairline)] bg-[var(--control)] px-2 py-1 text-xs"
                      />
                    </label>
                    <label className="text-xs text-[var(--content-muted)]">
                      {t(language, 'telemetryProbes.timeout')}
                      <input
                        type="number"
                        min={100}
                        max={5000}
                        value={probe.timeout_milliseconds ?? DEFAULT_TIMEOUT_MILLISECONDS}
                        onChange={(event) => {
                          const value = Number.parseInt(event.target.value, 10);
                          patchProbe(probe.id, {
                            timeout_milliseconds:
                              !Number.isFinite(value) || value === DEFAULT_TIMEOUT_MILLISECONDS ? undefined : value,
                          });
                        }}
                        className="mt-1 w-full rounded border border-[var(--hairline)] bg-[var(--control)] px-2 py-1 text-xs"
                      />
                    </label>
                  </div>

                  <button
                    type="button"
                    onClick={() => replace(probes.filter((candidate) => candidate.id !== probe.id))}
                    className="text-xs text-[var(--danger)] hover:underline"
                  >
                    {t(language, 'telemetryProbes.remove')}
                  </button>
                </div>
              );
            })}

            <button
              type="button"
              onClick={addProbe}
              disabled={probes.length >= MAX_PROBES}
              className="w-full rounded bg-[var(--accent)] px-2 py-1 text-xs text-[var(--accent-fg)] disabled:cursor-not-allowed disabled:opacity-50"
            >
              + {t(language, 'telemetryProbes.add')}
            </button>
            {probes.length >= MAX_PROBES && (
              <p className="text-xs text-[var(--content-muted)]">
                {t(language, 'telemetryProbes.maximum', { count: MAX_PROBES })}
              </p>
            )}
          </>
        )}
      </div>
    </details>
  );
}
