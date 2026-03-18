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
  // Deduplicate hosts from target's public endpoints for IP picker
  const targetHostOptions = Array.from(
    new Set(targetEndpointOptions.map((ep) => ep.host).filter(Boolean))
  );
  const matchedTargetHost = selectedEdge?.endpoint_host
    ? targetHostOptions.includes(selectedEdge.endpoint_host)
      ? `host:${selectedEdge.endpoint_host}`
      : '__manual__'
    : '__none__';

  // Compute the auto-assigned port for this edge's peer connection
  const computedEdgePort = (() => {
    if (!compileResult || !selectedEdge) return undefined;
    const sourceNode = nodes.find((n) => n.id === selectedEdge.from_node_id);
    const targetNode = nodes.find((n) => n.id === selectedEdge.to_node_id);
    if (!sourceNode || !targetNode) return undefined;
    // The target's interface for this source is named wg-<sourceName>
    const ifaceName = `wg-${sourceNode.name.toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 15);
    const configKey = `${targetNode.id}:${ifaceName}`;
    const config = compileResult.wireguard_configs[configKey];
    const portMatch = config?.match(/ListenPort\s*=\s*(\d+)/);
    return portMatch ? parseInt(portMatch[1], 10) : undefined;
  })();

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
            <div>
              <label className="text-xs text-gray-400">{txt(language, 'MTU (留空使用默认值 1420)', 'MTU (empty for default 1420)')}</label>
              <input
                type="number"
                value={selectedNode.mtu || ''}
                onChange={(e) => updateNode(selectedNode.id, { mtu: parseInt(e.target.value) || undefined })}
                placeholder="1420"
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
            {/* SSH Connection Details (collapsible) */}
            <details className="bg-gray-700/50 rounded p-2">
              <summary className="text-xs cursor-pointer text-gray-400 font-semibold">
                {txt(language, 'SSH 连接配置 (自动部署)', 'SSH Connection (Auto-Deploy)')}
              </summary>
              <div className="mt-2 space-y-2">
                <div>
                  <label className="text-xs text-gray-400">{txt(language, 'SSH 别名 (ssh_config Host)', 'SSH Alias (ssh_config Host)')}</label>
                  <input
                    type="text"
                    value={selectedNode.ssh_alias || ''}
                    onChange={(e) =>
                      updateNode(selectedNode.id, {
                        ssh_alias: e.target.value || undefined,
                      })
                    }
                    placeholder={txt(language, '如 my-server', 'e.g. my-server')}
                    className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                  />
                  <p className="text-[10px] text-gray-500 mt-0.5">
                    {txt(language, '设置别名后将忽略下方手动配置', 'If set, overrides manual host/port/user/key below')}
                  </p>
                </div>
                <div>
                  <label className="text-xs text-gray-400">{txt(language, 'SSH 主机', 'SSH Host')}</label>
                  <input
                    type="text"
                    value={selectedNode.ssh_host || ''}
                    onChange={(e) =>
                      updateNode(selectedNode.id, {
                        ssh_host: e.target.value || undefined,
                      })
                    }
                    placeholder={txt(language, 'IP 或域名', 'IP or hostname')}
                    className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                  />
                </div>
                <div className="grid grid-cols-2 gap-2">
                  <div>
                    <label className="text-xs text-gray-400">{txt(language, 'SSH 端口', 'SSH Port')}</label>
                    <input
                      type="number"
                      value={selectedNode.ssh_port || ''}
                      onChange={(e) =>
                        updateNode(selectedNode.id, {
                          ssh_port: parseInt(e.target.value) || undefined,
                        })
                      }
                      placeholder="22"
                      className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                    />
                  </div>
                  <div>
                    <label className="text-xs text-gray-400">{txt(language, 'SSH 用户', 'SSH User')}</label>
                    <input
                      type="text"
                      value={selectedNode.ssh_user || ''}
                      onChange={(e) =>
                        updateNode(selectedNode.id, {
                          ssh_user: e.target.value || undefined,
                        })
                      }
                      placeholder="root"
                      className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                    />
                  </div>
                </div>
                <div>
                  <label className="text-xs text-gray-400">{txt(language, 'SSH 密钥路径', 'SSH Key Path')}</label>
                  <input
                    type="text"
                    value={selectedNode.ssh_key_path || ''}
                    onChange={(e) =>
                      updateNode(selectedNode.id, {
                        ssh_key_path: e.target.value || undefined,
                      })
                    }
                    placeholder={txt(language, '如 ~/.ssh/id_ed25519', 'e.g. ~/.ssh/id_ed25519')}
                    className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                  />
                </div>
              </div>
            </details>
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
            {/* Endpoint IP — pick from target's public IPs or manual */}
            <div>
              <label className="text-xs text-gray-400">{txt(language, '目标 IP (从目标节点公网地址选择)', 'Endpoint IP (from target public IPs)')}</label>
              <select
                value={matchedTargetHost}
                onChange={(e) => {
                  const value = e.target.value;
                  if (value === '__manual__') {
                    return;
                  }
                  if (value === '__none__') {
                    updateEdge(selectedEdge.id, {
                      endpoint_host: undefined,
                    });
                    return;
                  }
                  const host = value.replace('host:', '');
                  updateEdge(selectedEdge.id, {
                    endpoint_host: host,
                  });
                }}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
              >
                <option value="__manual__">{txt(language, '手动输入', 'Manual input')}</option>
                <option value="__none__">{txt(language, '不设置', 'Unset')}</option>
                {targetHostOptions.map((host) => (
                  <option key={host} value={`host:${host}`}>
                    {host}
                  </option>
                ))}
              </select>
              <input
                type="text"
                value={selectedEdge.endpoint_host || ''}
                onChange={(e) => updateEdge(selectedEdge.id, { endpoint_host: e.target.value || undefined })}
                placeholder={txt(language, 'IP 或域名', 'IP or hostname')}
                className="w-full mt-1 px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            {/* Endpoint Port — auto from compiled interface or manual */}
            <div>
              <label className="text-xs text-gray-400">{txt(language, '目标端口 (WG 接口监听端口)', 'Endpoint Port (WG interface listen port)')}</label>
              <div className="flex gap-1 items-center">
                <input
                  type="number"
                  value={selectedEdge.endpoint_port || ''}
                  onChange={(e) => updateEdge(selectedEdge.id, { endpoint_port: parseInt(e.target.value) || undefined })}
                  placeholder={computedEdgePort ? String(computedEdgePort) : txt(language, '端口', 'Port')}
                  className="flex-1 px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                />
                {computedEdgePort && (
                  <button
                    onClick={() => updateEdge(selectedEdge.id, { endpoint_port: computedEdgePort })}
                    className="text-[10px] px-1.5 py-1 rounded bg-cyan-700 hover:bg-cyan-600 whitespace-nowrap"
                    title={txt(language, '使用编译后的自动端口', 'Use compiled auto-port')}
                  >
                    Auto:{computedEdgePort}
                  </button>
                )}
              </div>
              {computedEdgePort && selectedEdge.endpoint_port && selectedEdge.endpoint_port !== computedEdgePort && (
                <p className="text-[10px] text-yellow-400 mt-0.5">
                  {txt(language, '手动端口与编译端口不一致', 'Manual port differs from compiled port')} ({computedEdgePort})
                </p>
              )}
            </div>
            {compileResult && (() => {
              const toNode = nodes.find(n => n.id === selectedEdge.to_node_id);
              if (!toNode) return null;
              const ifaceName = `wg-${toNode.name.toLowerCase().replace(/[^a-z0-9-]/g, '-')}`.slice(0, 15);
              const configKey = `${selectedEdge.from_node_id}:${ifaceName}`;
              const config = compileResult.wireguard_configs[configKey];
              const endpointMatch = config?.match(/Endpoint\s*=\s*(.+)/);
              const listenMatch = config?.match(/ListenPort\s*=\s*(\d+)/);
              if (!endpointMatch && !listenMatch) return null;
              return (
                <div className="p-2 bg-gray-700/50 rounded space-y-1">
                  <p className="text-xs text-gray-400 font-semibold">{txt(language, '编译后实际值', 'Compiled values')}</p>
                  {endpointMatch && (
                    <p className="text-xs text-cyan-300 font-mono">{txt(language, '实际 Endpoint', 'Endpoint')}: {endpointMatch[1]}</p>
                  )}
                  {listenMatch && (
                    <p className="text-xs text-cyan-300 font-mono">{txt(language, '本端 ListenPort', 'Local ListenPort')}: {listenMatch[1]}</p>
                  )}
                </div>
              );
            })()}
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
                            {txt(language, '预览', 'Preview')}:\n{previewText(config)}
                          </pre>
                          <pre className="text-xs text-gray-300 mt-2 overflow-x-auto whitespace-pre-wrap max-h-72">
                            {config || txt(language, '无内容', 'No content')}
                          </pre>
                        </details>
                      );
                    })}
                  {Object.keys(compileResult.wireguard_configs)
                    .filter((key) => key.startsWith(n.id + ':')).length === 0 && (
                    <details className="bg-gray-800/70 rounded p-2">
                      <summary className="text-xs cursor-pointer text-cyan-300 text-gray-500">
                        wireguard/ ({txt(language, '无配置', 'No configs')})
                      </summary>
                    </details>
                  )}

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
          {/* Deploy Scripts (project-wide) */}
          {compileResult.deploy_scripts && Object.keys(compileResult.deploy_scripts).length > 0 && (
            <div className="mt-3 space-y-2">
              <h3 className="text-xs font-semibold text-gray-400 uppercase tracking-wider">
                {txt(language, '自动部署脚本', 'Auto-Deploy Scripts')}
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
