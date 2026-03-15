import { useTopologyStore } from '../../stores/topologyStore';

export function RightPanel() {
  const {
    selectedNodeId,
    selectedEdgeId,
    nodes,
    edges,
    domains,
    updateNode,
    removeNode,
    updateEdge,
    removeEdge,
    compileResult,
    compile,
    exportArtifacts,
    isCompiling,
  } = useTopologyStore();

  const selectedNode = nodes.find((n) => n.id === selectedNodeId);
  const selectedEdge = edges.find((e) => e.id === selectedEdgeId);

  return (
    <div className="p-3 space-y-4">
      {/* 操作按钮 */}
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          操作
        </h2>
        <div className="space-y-2">
          <button
            onClick={() => compile()}
            disabled={isCompiling || nodes.length === 0}
            className="w-full py-1.5 bg-green-600 hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {isCompiling ? '编译中...' : '🔨 编译'}
          </button>
          <button
            onClick={() => exportArtifacts()}
            disabled={nodes.length === 0}
            className="w-full py-1.5 bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            📦 导出产物包
          </button>
        </div>
      </section>

      <hr className="border-gray-700" />

      {/* 选中节点属性 */}
      {selectedNode && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            节点属性
          </h2>
          <div className="space-y-2">
            <div>
              <label className="text-xs text-gray-400">名称</label>
              <input
                type="text"
                value={selectedNode.name}
                onChange={(e) => updateNode(selectedNode.id, { name: e.target.value })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">角色</label>
              <select
                value={selectedNode.role}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    role: e.target.value as 'peer' | 'router' | 'relay' | 'gateway',
                  })
                }
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="peer">Peer</option>
                <option value="router">Router</option>
                <option value="relay">Relay</option>
                <option value="gateway">Gateway</option>
              </select>
            </div>
            <div>
              <label className="text-xs text-gray-400">网络域</label>
              <select
                value={selectedNode.domain_id}
                onChange={(e) => updateNode(selectedNode.id, { domain_id: e.target.value })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                {domains.map((d) => (
                  <option key={d.id} value={d.id}>{d.name}</option>
                ))}
              </select>
            </div>
            <div>
              <label className="text-xs text-gray-400">Overlay IP (留空自动分配)</label>
              <input
                type="text"
                value={selectedNode.overlay_ip || ''}
                onChange={(e) => updateNode(selectedNode.id, { overlay_ip: e.target.value || undefined })}
                placeholder="自动分配"
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">监听端口</label>
              <input
                type="number"
                value={selectedNode.listen_port || 51820}
                onChange={(e) => updateNode(selectedNode.id, { listen_port: parseInt(e.target.value) || 51820 })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <button
              onClick={() => removeNode(selectedNode.id)}
              className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
            >
              删除节点
            </button>
          </div>
        </section>
      )}

      {/* 选中边属性 */}
      {selectedEdge && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            连接属性
          </h2>
          <div className="space-y-2">
            <div>
              <label className="text-xs text-gray-400">类型</label>
              <select
                value={selectedEdge.type}
                onChange={(e) =>
                  updateEdge(selectedEdge.id, {
                    type: e.target.value as 'direct' | 'public-endpoint' | 'relay-path' | 'candidate',
                  })
                }
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="direct">Direct</option>
                <option value="public-endpoint">Public Endpoint</option>
                <option value="relay-path">Relay Path</option>
                <option value="candidate">Candidate</option>
              </select>
            </div>
            <div>
              <label className="text-xs text-gray-400">Endpoint Host</label>
              <input
                type="text"
                value={selectedEdge.endpoint_host || ''}
                onChange={(e) => updateEdge(selectedEdge.id, { endpoint_host: e.target.value || undefined })}
                placeholder="IP 或域名"
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">Endpoint Port</label>
              <input
                type="number"
                value={selectedEdge.endpoint_port || ''}
                onChange={(e) => updateEdge(selectedEdge.id, { endpoint_port: parseInt(e.target.value) || undefined })}
                placeholder="51820"
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selectedEdge.is_enabled}
                onChange={(e) => updateEdge(selectedEdge.id, { is_enabled: e.target.checked })}
              />
              启用
            </label>
            <button
              onClick={() => removeEdge(selectedEdge.id)}
              className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
            >
              删除连接
            </button>
          </div>
        </section>
      )}

      {/* 配置预览 */}
      {compileResult && !selectedNode && !selectedEdge && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            编译结果
          </h2>
          <div className="text-xs text-gray-300 space-y-1">
            <p>节点数: {compileResult.manifest.node_count}</p>
            <p>Checksum: {compileResult.manifest.checksum}</p>
            <p>编译时间: {compileResult.manifest.compiled_at}</p>
          </div>
          <div className="mt-2 space-y-2">
            {compileResult.topology.nodes.map((n) => (
              <details key={n.id} className="bg-gray-700 rounded p-2">
                <summary className="text-sm cursor-pointer text-blue-300">
                  {n.name} ({n.overlay_ip})
                </summary>
                <pre className="text-xs text-gray-300 mt-1 overflow-x-auto whitespace-pre-wrap">
                  {compileResult.wireguard_configs[n.id]?.substring(0, 300)}...
                </pre>
              </details>
            ))}
          </div>
        </section>
      )}

      {/* 无选中时提示 */}
      {!selectedNode && !selectedEdge && !compileResult && (
        <p className="text-xs text-gray-500 italic">
          点击画布上的节点或边查看属性
        </p>
      )}
    </div>
  );
}
