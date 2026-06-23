import { useTopologyStore } from '../../stores/topologyStore';
import { t, tValidationError } from '../../i18n';

function previewText(content: string | undefined, maxLines = 4, maxChars = 220): string {
  if (!content) return 'N/A';
  const lines = content.split('\n').slice(0, maxLines).join('\n');
  if (lines.length > maxChars) {
    return `${lines.slice(0, maxChars)}...`;
  }
  return lines;
}

// CompilePreview shows the compile result (extracted from RightPanel and moved to /deploy as a stable
// home). It displays the manifest, compile warnings, the per-node WireGuard / babel / sysctl / install
// config previews, and the project-wide auto-deploy scripts.
// Rendered only when a compileResult exists.
export function CompilePreview() {
  const language = useTopologyStore((s) => s.language);
  const compileResult = useTopologyStore((s) => s.compileResult);

  if (!compileResult) return null;

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg max-w-3xl">
      <h2 className="text-sm font-semibold text-[var(--content-muted)] uppercase tracking-wider mb-2">
        {t(language, 'compilePreview.compileResult')}
      </h2>
      <div className="text-xs text-[var(--content)] space-y-1">
        <p>{t(language, 'compilePreview.nodeCount')}: {compileResult.manifest.node_count}</p>
        <p>Checksum: {compileResult.manifest.checksum}</p>
        <p>{t(language, 'compilePreview.compiledAt')}: {compileResult.manifest.compiled_at}</p>
      </div>
      {/* Compile warnings: non-fatal notices from semantic validation (double NAT, edges missing an
          endpoint, isolated nodes, etc.), shown after a successful compile so the operator does not
          publish an effectively unreachable overlay on a "green" compile. */}
      {compileResult.warnings && compileResult.warnings.length > 0 && (
        <div className="mt-2 space-y-1">
          <h3 className="text-xs font-semibold text-[var(--warning)] uppercase tracking-wider">
            {t(language, 'compilePreview.compileWarnings')}
          </h3>
          {compileResult.warnings.map((w, i) => (
            <div
              key={`compile-warn-${i}`}
              className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded"
            >
              ⚠️ [{w.field}] {tValidationError(w, language)}
            </div>
          ))}
        </div>
      )}
      <div className="mt-2 space-y-2">
        {compileResult.topology.nodes.map((n) => (
          <details key={n.id} className="bg-[var(--control)] rounded p-2">
            <summary className="text-sm cursor-pointer text-[var(--accent)]">
              {n.name} ({n.overlay_ip})
            </summary>

            <div className="mt-2 space-y-2">
              {/* WireGuard per-peer interface configs */}
              {Object.entries(compileResult.wireguard_configs)
                .filter(([key]) => key.startsWith(n.id + ':'))
                .map(([key, config]) => {
                  const interfaceName = key.split(':').slice(1).join(':');
                  const portMatch = config?.match(/ListenPort\s*=\s*(\d+)/);
                  const portLabel = portMatch ? ` (port: ${portMatch[1]})` : '';
                  return (
                    <details key={key} className="bg-[var(--surface-sunken)] rounded p-2">
                      <summary className="text-xs cursor-pointer text-[var(--content)]">
                        wireguard/{interfaceName}.conf{portLabel}
                      </summary>
                      <pre className="text-xs text-[var(--content-muted)] mt-1 whitespace-pre-wrap">
                        {t(language, 'compilePreview.preview')}:{'\n'}{previewText(config)}
                      </pre>
                      <pre className="text-xs text-[var(--content)] mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                        {config || t(language, 'compilePreview.noContent')}
                      </pre>
                    </details>
                  );
                })}
              {Object.keys(compileResult.wireguard_configs)
                .filter((key) => key.startsWith(n.id + ':')).length === 0 && (
                <details className="bg-[var(--surface-sunken)] rounded p-2">
                  <summary className="text-xs cursor-pointer text-[var(--content)]">
                    wireguard/ ({t(language, 'compilePreview.noConfigs')})
                  </summary>
                </details>
              )}

              <details className="bg-[var(--surface-sunken)] rounded p-2">
                <summary className="text-xs cursor-pointer text-[var(--content)]">
                  babel/babeld.conf
                </summary>
                <pre className="text-xs text-[var(--content-muted)] mt-1 whitespace-pre-wrap">
                  {t(language, 'compilePreview.preview_2')}:{'\n'}{previewText(compileResult.babel_configs[n.id])}
                </pre>
                <pre className="text-xs text-[var(--content)] mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                  {compileResult.babel_configs[n.id] || t(language, 'compilePreview.noContent_2')}
                </pre>
              </details>

              <details className="bg-[var(--surface-sunken)] rounded p-2">
                <summary className="text-xs cursor-pointer text-[var(--content)]">
                  sysctl/99-overlay.conf
                </summary>
                <pre className="text-xs text-[var(--content-muted)] mt-1 whitespace-pre-wrap">
                  {t(language, 'compilePreview.preview_3')}:{'\n'}{previewText(compileResult.sysctl_configs[n.id])}
                </pre>
                <pre className="text-xs text-[var(--content)] mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                  {compileResult.sysctl_configs[n.id] || t(language, 'compilePreview.noContent_3')}
                </pre>
              </details>

              <details className="bg-[var(--surface-sunken)] rounded p-2">
                <summary className="text-xs cursor-pointer text-[var(--content)]">
                  scripts/install.sh
                </summary>
                <pre className="text-xs text-[var(--content-muted)] mt-1 whitespace-pre-wrap">
                  {t(language, 'compilePreview.preview_4')}:{'\n'}{previewText(compileResult.install_scripts[n.id])}
                </pre>
                <pre className="text-xs text-[var(--content)] mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                  {compileResult.install_scripts[n.id] || t(language, 'compilePreview.noContent_4')}
                </pre>
              </details>
            </div>
          </details>
        ))}
      </div>
      {/* Deploy Scripts (project-wide) */}
      {compileResult.deploy_scripts && Object.keys(compileResult.deploy_scripts).length > 0 && (
        <div className="mt-3 space-y-2">
          <h3 className="text-xs font-semibold text-[var(--content-muted)] uppercase tracking-wider">
            {t(language, 'compilePreview.autoDeployScripts')}
          </h3>
          {Object.entries(compileResult.deploy_scripts).map(([name, script]) => (
            <details key={name} className="bg-[var(--control)] rounded p-2">
              <summary className="text-sm cursor-pointer text-[var(--content)]">
                {name}
              </summary>
              <pre className="text-xs text-[var(--content)] mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                {script}
              </pre>
            </details>
          ))}
        </div>
      )}
    </section>
  );
}
