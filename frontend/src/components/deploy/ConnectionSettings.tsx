import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { Field } from '../../ui/Field';

// ConnectionSettings is the controller connection settings (the Connection section of /settings). As
// of plan-4, login/identity/break-glass moved out of the full-screen LoginPage (D2) and the UserMenu
// (logout); this keeps only the connection endpoints (persisted) and the "connect / refresh" action.
export function ConnectionSettings() {
  const language = useTopologyStore((s) => s.language);

  const baseURL = useControllerStore((s) => s.baseURL);
  const pathPrefix = useControllerStore((s) => s.pathPrefix);
  const agentBaseURL = useControllerStore((s) => s.agentBaseURL);
  const setConfig = useControllerStore((s) => s.setConfig);
  const refresh = useControllerStore((s) => s.refresh);
  const loading = useControllerStore((s) => s.loading);
  const error = useControllerStore((s) => s.error);
  const lastSyncedAt = useControllerStore((s) => s.lastSyncedAt);

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-[var(--accent)]">
        {t(language, 'connectionSettings.controllerConnection')}
      </h3>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <Field
          label={t(language, 'connectionSettings.operatorBaseURL')}
          type="text"
          value={baseURL}
          onChange={(e) => setConfig({ baseURL: e.target.value })}
          placeholder="http://localhost:8080"
        />
        <Field
          label={t(language, 'connectionSettings.secretPathPrefixOptional')}
          type="text"
          value={pathPrefix}
          onChange={(e) => setConfig({ pathPrefix: e.target.value })}
          placeholder="/s3cr3t"
          hint={t(language, 'connectionSettings.mustMatchTheServer')}
        />
        <Field
          label={t(language, 'connectionSettings.agentBaseURL')}
          type="text"
          value={agentBaseURL}
          onChange={(e) => setConfig({ agentBaseURL: e.target.value })}
          placeholder="http://localhost:9090"
        />
      </div>
      <p className="text-[10px] text-[var(--content-muted)]">
        {t(language, 'connectionSettings.signInHappensOn')}
      </p>
      {/* Refresh as a bottom submit-style action — gives the connection form a
          clear "submit" affordance, connecting/syncing the panel with the backend. */}
      <button
        onClick={() => refresh()}
        disabled={loading}
        className="w-full py-2 text-sm font-medium bg-[var(--accent)] hover:bg-[var(--accent-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--accent-fg)]"
      >
        {loading
          ? t(language, 'connectionSettings.syncing')
          : t(language, 'connectRefresh')}
      </button>
      {error && (
        <p className="text-xs text-[var(--danger)] bg-[var(--danger-bg)] px-2 py-1 rounded break-all">⚠️ {error}</p>
      )}
      {lastSyncedAt !== null && (
        <p className="text-[10px] text-[var(--content-muted)]">
          {t(language, 'connectionSettings.lastSynced')}: {new Date(lastSyncedAt).toLocaleString()}
        </p>
      )}
    </section>
  );
}
