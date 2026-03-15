import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

export function BottomBar() {
  const { validateResult, error, validate, isValidating, nodes, edges, domains, language } =
    useTopologyStore();

  return (
    <div className="p-3 h-full flex flex-col">
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider">
          {txt(language, '校验与状态', 'Validation & Status')}
        </h2>
        <div className="flex items-center gap-4">
          <span className="text-xs text-gray-500">
            {txt(language, '域', 'Domains')}: {domains.length} | {txt(language, '节点', 'Nodes')}: {nodes.length} | {txt(language, '边', 'Edges')}: {edges.length}
          </span>
          <button
            onClick={() => validate()}
            disabled={isValidating || nodes.length === 0}
            className="px-3 py-1 bg-yellow-600 hover:bg-yellow-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-xs"
          >
            {isValidating ? txt(language, '校验中...', 'Validating...') : txt(language, '🔍 校验拓扑', '🔍 Validate Topology')}
          </button>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto space-y-1">
        {/* 全局错误 */}
        {error && (
          <div className="text-sm text-red-400 bg-red-900/30 px-2 py-1 rounded">
            ❌ {error}
          </div>
        )}

        {/* 校验结果 */}
        {validateResult && (
          <>
            {validateResult.valid && (
              <div className="text-sm text-green-400 bg-green-900/30 px-2 py-1 rounded">
                {txt(language, '✅ 拓扑校验通过', '✅ Topology validation passed')}
              </div>
            )}

            {validateResult.errors?.map((e, i) => (
              <div
                key={`err-${i}`}
                className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded"
              >
                ❌ [{e.field}] {e.message}
              </div>
            ))}

            {validateResult.warnings?.map((w, i) => (
              <div
                key={`warn-${i}`}
                className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded"
              >
                ⚠️ [{w.field}] {w.message}
              </div>
            ))}
          </>
        )}

        {!validateResult && !error && (
          <p className="text-xs text-gray-500 italic">
            {txt(language, '点击“校验拓扑”检查配置是否正确', 'Click "Validate Topology" to check configuration')}
          </p>
        )}
      </div>
    </div>
  );
}
