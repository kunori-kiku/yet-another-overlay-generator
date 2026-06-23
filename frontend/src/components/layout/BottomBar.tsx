import { useTopologyStore } from '../../stores/topologyStore';
import { t, tValidationError } from '../../i18n';

export function BottomBar() {
  const { validateResult, error, validate, isValidating, nodes, edges, domains, language } =
    useTopologyStore();

  return (
    <div className="p-3 h-full flex flex-col">
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-sm font-semibold text-[var(--content-muted)] uppercase tracking-wider">
          {t(language, 'bottomBar.validationStatus')}
        </h2>
        <div className="flex items-center gap-4">
          <span className="text-xs text-[var(--content-muted)]">
            {t(language, 'bottomBar.domains')}: {domains.length} | {t(language, 'bottomBar.nodes')}: {nodes.length} | {t(language, 'bottomBar.edges')}: {edges.length}
          </span>
          <button
            onClick={() => validate()}
            disabled={isValidating || nodes.length === 0}
            className="px-3 py-1 bg-[var(--warning-solid)] text-[var(--warning-solid-fg)] hover:bg-[var(--warning-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-xs"
          >
            {isValidating ? t(language, 'bottomBar.validating') : t(language, 'bottomBar.validateTopology')}
          </button>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto space-y-1">
        {/* Global error */}
        {error && (
          <div className="text-sm text-[var(--danger)] bg-[var(--danger-bg)] px-2 py-1 rounded">
            ❌ {error}
          </div>
        )}

        {/* Validation result */}
        {validateResult && (
          <>
            {validateResult.valid && (
              <div className="text-sm text-[var(--success)] bg-[var(--success-bg)] px-2 py-1 rounded">
                {t(language, 'bottomBar.topologyValidationPassed')}
              </div>
            )}

            {validateResult.errors?.map((e, i) => (
              <div
                key={`err-${i}`}
                className="text-xs text-[var(--danger)] bg-[var(--danger-bg)] px-2 py-1 rounded"
              >
                ❌ [{e.field}] {tValidationError(e, language)}
              </div>
            ))}

            {validateResult.warnings?.map((w, i) => (
              <div
                key={`warn-${i}`}
                className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded"
              >
                ⚠️ [{w.field}] {tValidationError(w, language)}
              </div>
            ))}
          </>
        )}

        {!validateResult && !error && (
          <p className="text-xs text-[var(--content-muted)] italic">
            {t(language, 'bottomBar.clickValidateTopologyTo')}
          </p>
        )}
      </div>
    </div>
  );
}
