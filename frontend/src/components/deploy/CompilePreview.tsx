import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

function previewText(content: string | undefined, maxLines = 4, maxChars = 220): string {
  if (!content) return 'N/A';
  const lines = content.split('\n').slice(0, maxLines).join('\n');
  if (lines.length > maxChars) {
    return `${lines.slice(0, maxChars)}...`;
  }
  return lines;
}

// 编译结果预览（从 RightPanel 抽出，迁到 /deploy 作为稳定落点）。展示清单、编译告警、
// 每节点的 WireGuard / babel / sysctl / install 配置预览，以及项目级自动部署脚本。
// 仅在存在 compileResult 时渲染。
export function CompilePreview() {
  const language = useTopologyStore((s) => s.language);
  const compileResult = useTopologyStore((s) => s.compileResult);

  if (!compileResult) return null;

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg max-w-3xl">
      <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
        {t(language, 'compilePreview.compileResult')}
      </h2>
      <div className="text-xs text-gray-300 space-y-1">
        <p>{t(language, 'compilePreview.nodeCount')}: {compileResult.manifest.node_count}</p>
        <p>Checksum: {compileResult.manifest.checksum}</p>
        <p>{t(language, 'compilePreview.compiledAt')}: {compileResult.manifest.compiled_at}</p>
      </div>
      {/* 编译告警：语义校验产生的非致命提示（双重 NAT、缺少端点的边、孤立节点等），
          在编译成功后展示，避免操作员在一个“绿色”编译上发布事实上不可达的覆盖网络。 */}
      {compileResult.warnings && compileResult.warnings.length > 0 && (
        <div className="mt-2 space-y-1">
          <h3 className="text-xs font-semibold text-yellow-400 uppercase tracking-wider">
            {t(language, 'compilePreview.compileWarnings')}
          </h3>
          {compileResult.warnings.map((w, i) => (
            <div
              key={`compile-warn-${i}`}
              className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded"
            >
              ⚠️ [{w.field}] {w.message}
            </div>
          ))}
        </div>
      )}
      <div className="mt-2 space-y-2">
        {compileResult.topology.nodes.map((n) => (
          <details key={n.id} className="bg-gray-700 rounded p-2">
            <summary className="text-sm cursor-pointer text-blue-300">
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
                    <details key={key} className="bg-gray-800/70 rounded p-2">
                      <summary className="text-xs cursor-pointer text-cyan-300">
                        wireguard/{interfaceName}.conf{portLabel}
                      </summary>
                      <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                        {t(language, 'compilePreview.preview')}:{'\n'}{previewText(config)}
                      </pre>
                      <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                        {config || t(language, 'compilePreview.noContent')}
                      </pre>
                    </details>
                  );
                })}
              {Object.keys(compileResult.wireguard_configs)
                .filter((key) => key.startsWith(n.id + ':')).length === 0 && (
                <details className="bg-gray-800/70 rounded p-2">
                  <summary className="text-xs cursor-pointer text-cyan-300 text-gray-500">
                    wireguard/ ({t(language, 'compilePreview.noConfigs')})
                  </summary>
                </details>
              )}

              <details className="bg-gray-800/70 rounded p-2">
                <summary className="text-xs cursor-pointer text-amber-300">
                  babel/babeld.conf
                </summary>
                <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                  {t(language, 'compilePreview.preview_2')}:{'\n'}{previewText(compileResult.babel_configs[n.id])}
                </pre>
                <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                  {compileResult.babel_configs[n.id] || t(language, 'compilePreview.noContent_2')}
                </pre>
              </details>

              <details className="bg-gray-800/70 rounded p-2">
                <summary className="text-xs cursor-pointer text-lime-300">
                  sysctl/99-overlay.conf
                </summary>
                <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                  {t(language, 'compilePreview.preview_3')}:{'\n'}{previewText(compileResult.sysctl_configs[n.id])}
                </pre>
                <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                  {compileResult.sysctl_configs[n.id] || t(language, 'compilePreview.noContent_3')}
                </pre>
              </details>

              <details className="bg-gray-800/70 rounded p-2">
                <summary className="text-xs cursor-pointer text-fuchsia-300">
                  scripts/install.sh
                </summary>
                <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                  {t(language, 'compilePreview.preview_4')}:{'\n'}{previewText(compileResult.install_scripts[n.id])}
                </pre>
                <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
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
          <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider">
            {t(language, 'compilePreview.autoDeployScripts')}
          </h3>
          {Object.entries(compileResult.deploy_scripts).map(([name, script]) => (
            <details key={name} className="bg-gray-700 rounded p-2">
              <summary className="text-sm cursor-pointer text-orange-300">
                {name}
              </summary>
              <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                {script}
              </pre>
            </details>
          ))}
        </div>
      )}
    </section>
  );
}
