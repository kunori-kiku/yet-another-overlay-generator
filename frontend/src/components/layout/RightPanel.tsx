import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

function previewText(content: string | undefined, maxLines = 4, maxChars = 220): string {
  if (!content) return 'N/A';
  const lines = content.split('\n').slice(0, maxLines).join('\n');
  if (lines.length > maxChars) {
    return `${lines.slice(0, maxChars)}...`;
  }
  return lines;
}

export function RightPanel() {
  const {
    selectedDomainId,
    selectedNodeId,
    selectedEdgeId,
    nodes,
    edges,
    domains,
    updateDomain,
    removeDomain,
    updateNode,
    removeNode,
    updateEdge,
    removeEdge,
    compileResult,
    compile,
    exportArtifacts,
    isCompiling,
    language,
  } = useTopologyStore();

  const selectedDomain = domains.find((d) => d.id === selectedDomainId);
  const selectedNode = nodes.find((n) => n.id === selectedNodeId);
  const selectedEdge = edges.find((e) => e.id === selectedEdgeId);
  const selectedEdgeTarget = selectedEdge
    ? nodes.find((n) => n.id === selectedEdge.to_node_id)
    : undefined;

  const targetEndpointOptions = selectedEdgeTarget?.public_endpoints || [];
  const matchedTargetEndpoint = targetEndpointOptions.find(
    (ep) =>
      ep.host === selectedEdge?.endpoint_host &&
      ep.port === selectedEdge?.endpoint_port
  );
  const selectedEdgeEndpointValue = matchedTargetEndpoint
    ? `ep:${matchedTargetEndpoint.id}`
    : '__manual__';

  const updateNodeEndpoint = (
    nodeId: string,
    endpointId: string,
    updates: { host?: string; port?: number; note?: string }
  ) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      public_endpoints: (node.public_endpoints || []).map((ep) =>
        ep.id === endpointId ? { ...ep, ...updates } : ep
      ),
    });
  };

  const addNodeEndpoint = (nodeId: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    const newEndpoint = {
      id: `${nodeId}-ep-${Date.now()}`,
      host: '',
      port: node.listen_port || 51820,
      note: '',
    };
    updateNode(nodeId, {
      public_endpoints: [...(node.public_endpoints || []), newEndpoint],
    });
  };

  const removeNodeEndpoint = (nodeId: string, endpointId: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      public_endpoints: (node.public_endpoints || []).filter((ep) => ep.id !== endpointId),
    });
  };

  return (
    <div className="p-3 space-y-4">
      {/* 操作按钮 */}
      <section>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
          {txt(language, '操作', 'Actions')}
        </h2>
        <div className="space-y-2">
          <button
            onClick={() => compile()}
            disabled={isCompiling || nodes.length === 0}
            className="w-full py-1.5 bg-green-600 hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {isCompiling ? txt(language, '编译中...', 'Compiling...') : txt(language, '🔨 编译', '🔨 Compile')}
          </button>
          <button
            onClick={() => exportArtifacts()}
            disabled={nodes.length === 0}
            className="w-full py-1.5 bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {txt(language, '📦 导出产物包', '📦 Export Artifacts')}
          </button>
        </div>
      </section>

      <hr className="border-gray-700" />

      {/* 选中域属性 */}
      {selectedDomain && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            {txt(language, '网络域属性', 'Domain Properties')}
          </h2>
          <div className="space-y-2">
            <div>
              <label className="text-xs text-gray-400">{txt(language, '名称', 'Name')}</label>
              <input
                type="text"
                value={selectedDomain.name}
                onChange={(e) => updateDomain(selectedDomain.id, { name: e.target.value })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">CIDR</label>
              <input
                type="text"
                value={selectedDomain.cidr}
                onChange={(e) => updateDomain(selectedDomain.id, { cidr: e.target.value })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '路由模式', 'Routing Mode')}</label>
              <select
                value={selectedDomain.routing_mode}
                onChange={(e) =>
                  updateDomain(selectedDomain.id, {
                    routing_mode: e.target.value as 'babel' | 'static' | 'none',
                  })
                }
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="babel">Babel</option>
                <option value="static">Static</option>
                <option value="none">None</option>
              </select>
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '分配模式', 'Allocation Mode')}</label>
              <select
                value={selectedDomain.allocation_mode}
                onChange={(e) =>
                  updateDomain(selectedDomain.id, {
                    allocation_mode: e.target.value as 'auto' | 'manual',
                  })
                }
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="auto">Auto</option>
                <option value="manual">Manual</option>
              </select>
            </div>
            <button
              onClick={() => removeDomain(selectedDomain.id)}
              className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
            >
              {txt(language, '删除网络域', 'Delete Domain')}
            </button>
          </div>
        </section>
      )}

      {/* 选中节点属性 */}
      {selectedNode && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            {txt(language, '节点属性', 'Node Properties')}
          </h2>
          <div className="space-y-2">
            <div>
              <label className="text-xs text-gray-400">{txt(language, '名称', 'Name')}</label>
              <input
                type="text"
                value={selectedNode.name}
                onChange={(e) => updateNode(selectedNode.id, { name: e.target.value })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '主机名 (可选)', 'Hostname (optional)')}</label>
              <input
                type="text"
                value={selectedNode.hostname || ''}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    hostname: e.target.value || undefined,
                  })
                }
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '角色', 'Role')}</label>
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
              <label className="text-xs text-gray-400">{txt(language, '网络域', 'Domain')}</label>
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
              <label className="text-xs text-gray-400">{txt(language, 'Overlay IP (留空自动分配)', 'Overlay IP (empty for auto)')}</label>
              <input
                type="text"
                value={selectedNode.overlay_ip || ''}
                onChange={(e) => updateNode(selectedNode.id, { overlay_ip: e.target.value || undefined })}
                placeholder={txt(language, '自动分配', 'Auto assigned')}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">{txt(language, '监听端口', 'Listen Port')}</label>
              <input
                type="number"
                value={selectedNode.listen_port || 51820}
                onChange={(e) => updateNode(selectedNode.id, { listen_port: parseInt(e.target.value) || 51820 })}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selectedNode.capabilities.has_public_ip}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    capabilities: {
                      ...selectedNode.capabilities,
                      has_public_ip: e.target.checked,
                      can_accept_inbound: e.target.checked,
                    },
                  })
                }
              />
              {txt(language, '公网可达', 'Publicly Reachable')}
            </label>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selectedNode.fixed_private_key || false}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    fixed_private_key: e.target.checked,
                    ...(e.target.checked
                      ? {}
                      : {
                          wireguard_private_key: undefined,
                          wireguard_public_key: undefined,
                        }),
                  })
                }
              />
              {txt(language, '固定私钥（编译后持久化）', 'Pin private key (persist after compile)')}
            </label>
            {selectedNode.fixed_private_key && (
              <div className="p-2 bg-gray-700 rounded space-y-1">
                <p className="text-xs text-gray-300">{txt(language, '固定密钥状态', 'Pinned key status')}</p>
                <p className="text-xs text-gray-400 break-all">
                  {txt(language, '公钥', 'Public key')}: {selectedNode.wireguard_public_key || txt(language, '将在下次编译后生成', 'Will be generated on next compile')}
                </p>
                <p className="text-xs text-gray-500 break-all">
                  {txt(language, '私钥', 'Private key')}: {selectedNode.wireguard_private_key ? txt(language, '已生成并持久化', 'Generated and persisted') : txt(language, '尚未生成', 'Not generated yet')}
                </p>
              </div>
            )}
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selectedNode.capabilities.can_forward}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    capabilities: {
                      ...selectedNode.capabilities,
                      can_forward: e.target.checked,
                    },
                  })
                }
              />
              {txt(language, '可转发流量', 'Can Forward Traffic')}
            </label>
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <label className="text-xs text-gray-400">{txt(language, '公网可达地址映射 (IP:端口)', 'Public endpoint mappings (IP:Port)')}</label>
                <button
                  onClick={() => addNodeEndpoint(selectedNode.id)}
                  className="text-xs px-2 py-0.5 rounded bg-blue-600 hover:bg-blue-500"
                >
                  + {txt(language, '添加', 'Add')}
                </button>
              </div>
              {(selectedNode.public_endpoints || []).length === 0 && (
                <p className="text-xs text-gray-500 italic">{txt(language, '尚未配置映射地址', 'No endpoint mappings configured')}</p>
              )}
              {(selectedNode.public_endpoints || []).map((ep) => (
                <div key={ep.id} className="p-2 bg-gray-700 rounded space-y-1">
                  <div className="grid grid-cols-3 gap-1">
                    <input
                      type="text"
                      value={ep.host}
                      onChange={(e) =>
                        updateNodeEndpoint(selectedNode.id, ep.id, { host: e.target.value })
                      }
                      placeholder={txt(language, 'IP/域名', 'IP/Domain')}
                      className="col-span-2 px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500"
                    />
                    <input
                      type="number"
                      value={ep.port}
                      onChange={(e) =>
                        updateNodeEndpoint(selectedNode.id, ep.id, {
                          port: parseInt(e.target.value, 10) || 51820,
                        })
                      }
                      placeholder={txt(language, '端口', 'Port')}
                      className="px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500"
                    />
                  </div>
                  <div className="flex gap-1">
                    <input
                      type="text"
                      value={ep.note || ''}
                      onChange={(e) =>
                        updateNodeEndpoint(selectedNode.id, ep.id, { note: e.target.value })
                      }
                      placeholder={txt(language, '备注 (可选)', 'Note (optional)')}
                      className="flex-1 px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500"
                    />
                    <button
                      onClick={() => removeNodeEndpoint(selectedNode.id, ep.id)}
                      className="px-2 py-1 text-xs bg-red-600 hover:bg-red-500 rounded"
                    >
                      {txt(language, '删除', 'Delete')}
                    </button>
                  </div>
                </div>
              ))}
            </div>
            <button
              onClick={() => removeNode(selectedNode.id)}
              className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
            >
              {txt(language, '删除节点', 'Delete Node')}
            </button>
          </div>
        </section>
      )}

      {/* 选中边属性 */}
      {selectedEdge && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            {txt(language, '连接属性', 'Edge Properties')}
          </h2>
          <div className="space-y-2">
            <div>
              <label className="text-xs text-gray-400">{txt(language, '类型', 'Type')}</label>
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
              <label className="text-xs text-gray-400">{txt(language, '目标节点公网映射', 'Target node endpoint mapping')}</label>
              <select
                value={selectedEdgeEndpointValue}
                onChange={(e) => {
                  const value = e.target.value;
                  if (value === '__manual__') {
                    return;
                  }
                  if (value === '__none__') {
                    updateEdge(selectedEdge.id, {
                      endpoint_host: undefined,
                      endpoint_port: undefined,
                    });
                    return;
                  }
                  const endpointId = value.replace('ep:', '');
                  const endpoint = targetEndpointOptions.find((ep) => ep.id === endpointId);
                  if (!endpoint) {
                    return;
                  }
                  updateEdge(selectedEdge.id, {
                    endpoint_host: endpoint.host,
                    endpoint_port: endpoint.port,
                  });
                }}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="__manual__">{txt(language, '手动输入', 'Manual input')}</option>
                <option value="__none__">{txt(language, '不设置 endpoint', 'Unset endpoint')}</option>
                {targetEndpointOptions.map((ep) => (
                  <option key={ep.id} value={`ep:${ep.id}`}>
                    {ep.host}:{ep.port} {ep.note ? `(${ep.note})` : ''}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label className="text-xs text-gray-400">Endpoint Host</label>
              <input
                type="text"
                value={selectedEdge.endpoint_host || ''}
                onChange={(e) => updateEdge(selectedEdge.id, { endpoint_host: e.target.value || undefined })}
                placeholder={txt(language, 'IP 或域名', 'IP or domain')}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div>
              <label className="text-xs text-gray-400">Endpoint Port</label>
              <input
                type="number"
                value={selectedEdge.endpoint_port || ''}
                onChange={(e) => updateEdge(selectedEdge.id, { endpoint_port: parseInt(e.target.value) || undefined })}
                placeholder={txt(language, '端口', 'Port')}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selectedEdge.is_enabled}
                onChange={(e) => updateEdge(selectedEdge.id, { is_enabled: e.target.checked })}
              />
              {txt(language, '启用', 'Enabled')}
            </label>
            <button
              onClick={() => removeEdge(selectedEdge.id)}
              className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
            >
              {txt(language, '删除连接', 'Delete Edge')}
            </button>
          </div>
        </section>
      )}

      {/* 配置预览 */}
      {compileResult && !selectedDomain && !selectedNode && !selectedEdge && (
        <section>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
            {txt(language, '编译结果', 'Compile Result')}
          </h2>
          <div className="text-xs text-gray-300 space-y-1">
            <p>{txt(language, '节点数', 'Node count')}: {compileResult.manifest.node_count}</p>
            <p>Checksum: {compileResult.manifest.checksum}</p>
            <p>{txt(language, '编译时间', 'Compiled at')}: {compileResult.manifest.compiled_at}</p>
          </div>
          <div className="mt-2 space-y-2">
            {compileResult.topology.nodes.map((n) => (
              <details key={n.id} className="bg-gray-700 rounded p-2">
                <summary className="text-sm cursor-pointer text-blue-300">
                  {n.name} ({n.overlay_ip})
                </summary>

                <div className="mt-2 space-y-2">
                  <details className="bg-gray-800/70 rounded p-2">
                    <summary className="text-xs cursor-pointer text-cyan-300">
                      wireguard/wg0.conf
                    </summary>
                    <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                      {txt(language, '预览', 'Preview')}:\n{previewText(compileResult.wireguard_configs[n.id])}
                    </pre>
                    <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                      {compileResult.wireguard_configs[n.id] || txt(language, '无内容', 'No content')}
                    </pre>
                  </details>

                  <details className="bg-gray-800/70 rounded p-2">
                    <summary className="text-xs cursor-pointer text-amber-300">
                      babel/babeld.conf
                    </summary>
                    <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                      {txt(language, '预览', 'Preview')}:\n{previewText(compileResult.babel_configs[n.id])}
                    </pre>
                    <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                      {compileResult.babel_configs[n.id] || txt(language, '无内容', 'No content')}
                    </pre>
                  </details>

                  <details className="bg-gray-800/70 rounded p-2">
                    <summary className="text-xs cursor-pointer text-lime-300">
                      sysctl/99-overlay.conf
                    </summary>
                    <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                      {txt(language, '预览', 'Preview')}:\n{previewText(compileResult.sysctl_configs[n.id])}
                    </pre>
                    <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                      {compileResult.sysctl_configs[n.id] || txt(language, '无内容', 'No content')}
                    </pre>
                  </details>

                  <details className="bg-gray-800/70 rounded p-2">
                    <summary className="text-xs cursor-pointer text-fuchsia-300">
                      scripts/install.sh
                    </summary>
                    <pre className="text-xs text-gray-400 mt-1 whitespace-pre-wrap">
                      {txt(language, '预览', 'Preview')}:\n{previewText(compileResult.install_scripts[n.id])}
                    </pre>
                    <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                      {compileResult.install_scripts[n.id] || txt(language, '无内容', 'No content')}
                    </pre>
                  </details>
                </div>
              </details>
            ))}
          </div>
        </section>
      )}

      {/* 无选中时提示 */}
      {!selectedDomain && !selectedNode && !selectedEdge && !compileResult && (
        <p className="text-xs text-gray-500 italic">
          {txt(language, '点击左侧列表或画布元素查看并编辑属性', 'Click items in the left list or canvas to view/edit properties')}
        </p>
      )}
    </div>
  );
}
