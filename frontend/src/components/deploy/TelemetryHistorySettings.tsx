import { useEffect, useState } from 'react';
import { emptyControllerSettings, type ControllerSettings } from '../../api/controllerClient';
import { useControllerStore, selectHasAuth } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t, type UILanguage } from '../../i18n';

// TelemetryHistorySettings (telemetry-history plan-4): the fleet card for the per-node resource-
// history sample cap (plan-2 backend field telemetry_history_cap). One numeric knob that rides the
// existing full-replace /settings contract, mirroring the config-card pattern (BootstrapSettings /
// MimicCatalogSettings): a keyed controlled form initialized from the server value on (re)mount.
//
// Semantics mirror the Go *int: blank ⇒ default (DefaultTelemetryHistoryCap), 0 ⇒ history disabled
// (the node-detail charts then show a "history off" hint), N ⇒ retain N samples per node.

// Mirrors internal/controller/telemetry_history.go DefaultTelemetryHistoryCap and
// internal/api/handler_bootstrap.go maxTelemetryHistoryCap; used for the hint + client-side bound
// (the server is authoritative on save).
const DEFAULT_CAP = 20160;
const MAX_CAP = 1_000_000;

export function TelemetryHistorySettings() {
  const language = useTopologyStore((s) => s.language);
  const settings = useControllerStore((s) => s.settings);
  const loadSettings = useControllerStore((s) => s.loadSettings);
  const saveSettings = useControllerStore((s) => s.saveSettings);
  const loading = useControllerStore((s) => s.loading);
  const hasAuth = useControllerStore(selectHasAuth);

  useEffect(() => {
    if (hasAuth && settings === null) void loadSettings();
  }, [hasAuth, settings, loadSettings]);

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-[var(--success)]">
        {t(language, 'telemetryHistorySettings.heading')}
      </h3>
      <p className="text-sm text-[var(--content-muted)]">
        {t(language, 'telemetryHistorySettings.description')}
      </p>
      {!hasAuth ? (
        <p className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded">
          {t(language, 'telemetryHistorySettings.signInToConfigure')}
        </p>
      ) : (
        <CapForm
          key={settings ? JSON.stringify(settings) : 'empty'}
          initial={settings ?? emptyControllerSettings()}
          loading={loading}
          language={language}
          onSave={saveSettings}
        />
      )}
    </section>
  );
}

function CapForm({
  initial,
  loading,
  language,
  onSave,
}: {
  initial: ControllerSettings;
  loading: boolean;
  language: UILanguage;
  onSave: (s: ControllerSettings) => Promise<string | null>;
}) {
  // Blank input ⇒ the server default (null on the wire); a number is honored verbatim (0 disables).
  const [cap, setCap] = useState<string>(initial.telemetryHistoryCap === null ? '' : String(initial.telemetryHistoryCap));
  const [localError, setLocalError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Validate mirrors the backend bound (>= 0, <= MAX_CAP, whole number). Blank is valid (⇒ default).
  const trimmed = cap.trim();
  const parsed = trimmed === '' ? null : Number(trimmed);
  const invalid =
    parsed !== null && (!/^\d+$/.test(trimmed) || !Number.isInteger(parsed) || parsed < 0 || parsed > MAX_CAP);

  const handleSave = async () => {
    if (invalid) return;
    setLocalError(null);
    setSaved(false);
    const err = await onSave({ ...initial, telemetryHistoryCap: parsed });
    if (err) setLocalError(err);
    else setSaved(true);
  };

  return (
    <div className="space-y-2">
      <div>
        <label className="text-xs text-[var(--content-muted)]">
          {t(language, 'telemetryHistorySettings.capLabel')}
        </label>
        <input
          type="number"
          min={0}
          max={MAX_CAP}
          step={1}
          value={cap}
          onChange={(e) => {
            setCap(e.target.value);
            setSaved(false);
          }}
          placeholder={String(DEFAULT_CAP)}
          data-testid="telemetry-history-cap"
          className="w-40 px-2 py-1 bg-[var(--control)] rounded text-sm font-mono border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
        />
        <p className="text-[10px] text-[var(--content-muted)] mt-0.5">
          {t(language, 'telemetryHistorySettings.capHint', { default: DEFAULT_CAP, max: MAX_CAP })}
        </p>
      </div>

      {invalid && (
        <p className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded" data-testid="telemetry-history-cap-invalid">
          {t(language, 'telemetryHistorySettings.capInvalid', { max: MAX_CAP })}
        </p>
      )}
      {localError && (
        <p className="text-xs text-[var(--danger)] bg-[var(--danger-bg)] px-2 py-1 rounded break-all">⚠️ {localError}</p>
      )}
      {saved && (
        <p className="text-xs text-[var(--success)] bg-[var(--success-bg)] px-2 py-1 rounded" data-testid="telemetry-history-saved">
          {t(language, 'telemetryHistorySettings.saved')}
        </p>
      )}

      <button
        onClick={() => void handleSave()}
        disabled={loading || invalid}
        data-testid="telemetry-history-save"
        className="mt-1 px-4 py-1.5 text-sm bg-[var(--accent)] hover:bg-[var(--accent-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--accent-fg)] font-medium"
      >
        {loading ? t(language, 'telemetryHistorySettings.saving') : t(language, 'telemetryHistorySettings.save')}
      </button>
    </div>
  );
}
