import { useTopologyStore } from '../../stores/topologyStore';

export function BottomBar() {
  const { validateResult, error, validate, isValidating, nodes, edges, domains } =
    useTopologyStore();

  return (
    <div className="p-3 h-full flex flex-col">
      <div className="flex items-center justify-between mb-2">
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider">
          校验 & 状态
        </h2>
        <div className="flex items-center gap-4">
          <span className="text-xs text-gray-500">
            域: {domains.length} | 节点: {nodes.length} | 边: {edges.length}
          </span>
          <button
            onClick={() => validate()}
            disabled={isValidating || nodes.length === 0}
            className="px-3 py-1 bg-yellow-600 hover:bg-yellow-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-xs"
          >
            {isValidating ? '校验中...' : '🔍 校验拓扑'}
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
                ✅ 拓扑校验通过
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
            点击"校验拓扑"检查配置是否正确
          </p>
        )}
      </div>
    </div>
  );
}
