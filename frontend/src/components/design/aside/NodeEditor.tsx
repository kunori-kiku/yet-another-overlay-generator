import { useTopologyStore } from '../../../stores/topologyStore';
import { useControllerStore } from '../../../stores/controllerStore';
import { t } from '../../../i18n';
import { deriveCapabilitiesFromRole, type NodeRole } from '../../../lib/roleCapabilities';
import { uuid } from '../../../lib/uuid';

// 节点属性编辑器（从 RightPanel 的选中节点区块原样抽出，含公网地址 / 通告网段 / SSH 配置，
// 以及对 reconcileEdgeEndpoints 的耦合）。供选择驱动的 Design 右侧 aside 使用。
export function NodeEditor() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
  const domains = useTopologyStore((s) => s.domains);
  const selectedNodeId = useTopologyStore((s) => s.selectedNodeId);
  const updateNode = useTopologyStore((s) => s.updateNode);
  const removeNode = useTopologyStore((s) => s.removeNode);
  const reconcileEdgeEndpoints = useTopologyStore((s) => s.reconcileEdgeEndpoints);
  // fixed_private_key is a LOCAL/air-gap custody primitive: it tells the client-side compiler to
  // generate-and-persist a node's WireGuard private key into the design (+ localStorage).
  // Controller mode is zero-knowledge — the agent owns the private key, the server never sees it,
  // deploy() strips any private value — so the pin-key control is a dead, misleading affordance
  // there (and would write a meaningless fixed_private_key flag into the server design). Gate it
  // to local mode (plan-11 / T4), mirroring the Compile/export/deploy-script gates.
  const mode = useControllerStore((s) => s.mode);

  const selectedNode = nodes.find((n) => n.id === selectedNodeId);

  const updateNodeEndpoint = (
    nodeId: string,
    endpointId: string,
    updates: { host?: string; port?: number; note?: string },
  ) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    const previous = (node.public_endpoints || []).find((ep) => ep.id === endpointId);
    updateNode(nodeId, {
      public_endpoints: (node.public_endpoints || []).map((ep) =>
        ep.id === endpointId ? { ...ep, ...updates } : ep,
      ),
    });
    // 主机被改名时，同步指向该节点、且快照了旧主机的 edge，避免拨向陈旧目标
    if (updates.host !== undefined && previous?.host && updates.host !== previous.host) {
      reconcileEdgeEndpoints(nodeId, previous.host, updates.host);
    }
  };

  const addNodeEndpoint = (nodeId: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    const newEndpoint = {
      id: `${nodeId}-ep-${uuid()}`,
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
    const removed = (node.public_endpoints || []).find((ep) => ep.id === endpointId);
    updateNode(nodeId, {
      public_endpoints: (node.public_endpoints || []).filter((ep) => ep.id !== endpointId),
    });
    // 该主机被移除时，清空指向它的 edge 的 endpoint，让连接退回后端自动解析
    if (removed?.host) {
      reconcileEdgeEndpoints(nodeId, removed.host, null);
    }
  };

  // extra_prefixes 是纯字符串数组（无稳定 id），因此按下标增删改，
  // 与上面的公网地址列表（对象数组、按 id 操作）形成对照。
  const addExtraPrefix = (nodeId: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      extra_prefixes: [...(node.extra_prefixes || []), ''],
    });
  };

  const updateExtraPrefix = (nodeId: string, index: number, value: string) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      extra_prefixes: (node.extra_prefixes || []).map((prefix, i) => (i === index ? value : prefix)),
    });
  };

  const removeExtraPrefix = (nodeId: string, index: number) => {
    const node = nodes.find((n) => n.id === nodeId);
    if (!node) return;
    updateNode(nodeId, {
      extra_prefixes: (node.extra_prefixes || []).filter((_, i) => i !== index),
    });
  };

  if (!selectedNode) return null;

  return (
    <section>
      <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-2">
        {t(language, 'nodeEditor.nodeProperties')}
      </h2>
      <div className="space-y-2">
        <div>
          <label className="text-xs text-gray-400">{t(language, 'nodeEditor.name')}</label>
          <input
            type="text"
            value={selectedNode.name}
            onChange={(e) => updateNode(selectedNode.id, { name: e.target.value })}
            pattern="^[A-Za-z0-9 ._-]+$"
            title={t(language, 'nodeEditor.onlyLettersDigitsSpace')}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-gray-400">{t(language, 'nodeEditor.hostnameOptional')}</label>
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
          <label className="text-xs text-gray-400">{t(language, 'nodeEditor.role')}</label>
          <select
            value={selectedNode.role}
            onChange={(e) => {
              const newRole = e.target.value as NodeRole;
              // 每次角色变更都重新推导 can_forward/can_relay/can_accept_inbound（D54），
              // 与 NodeForm/roles.go 的推断保持一致；client 角色强制 has_public_ip=false，
              // 其余角色保留操作员已设置的 has_public_ip。
              const operatorHasPublicIP =
                newRole === 'client' ? false : selectedNode.capabilities.has_public_ip;
              updateNode(selectedNode.id, {
                role: newRole,
                capabilities: deriveCapabilitiesFromRole(newRole, operatorHasPublicIP),
              });
            }}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
          >
            <option value="peer">Peer</option>
            <option value="router">Router</option>
            <option value="relay">Relay</option>
            <option value="gateway">Gateway</option>
            <option value="client">Client</option>
          </select>
        </div>
        <div>
          <label className="text-xs text-gray-400">{t(language, 'nodeEditor.domain')}</label>
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
          <label className="text-xs text-gray-400">{t(language, 'nodeEditor.overlayIPEmptyFor')}</label>
          <input
            type="text"
            value={selectedNode.overlay_ip || ''}
            onChange={(e) => updateNode(selectedNode.id, { overlay_ip: e.target.value || undefined })}
            placeholder={t(language, 'nodeEditor.autoAssigned')}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-gray-400">{t(language, 'nodeEditor.listenPort')}</label>
          <input
            type="number"
            value={selectedNode.listen_port || 51820}
            onChange={(e) => updateNode(selectedNode.id, { listen_port: parseInt(e.target.value) || 51820 })}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-gray-400">{t(language, 'nodeEditor.mtuEmptyForDefault')}</label>
          <input
            type="number"
            min={576}
            max={65535}
            value={selectedNode.mtu || ''}
            onChange={(e) => updateNode(selectedNode.id, { mtu: parseInt(e.target.value) || undefined })}
            placeholder="1420"
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        {/* mimic XDP 模式：仅当该节点有 transport=tcp 的链路时才起作用。默认 skb（通用，
            兼容不支持 native 的 VPS 网卡）；操作员确认网卡支持时可选 native 以提升性能。 */}
        <div>
          <label className="text-xs text-gray-400">{t(language, 'xdpModeLabel')}</label>
          <select
            value={selectedNode.xdp_mode || 'skb'}
            onChange={(e) => updateNode(selectedNode.id, { xdp_mode: e.target.value === 'native' ? 'native' : undefined })}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
          >
            <option value="skb">{t(language, 'nodeEditor.skbGenericDefault')}</option>
            <option value="native">{t(language, 'nodeEditor.nativeFasterNeedsNIC')}</option>
          </select>
          <p className="mt-1 text-xs text-gray-500">{t(language, 'xdpModeHint')}</p>
        </div>
        {selectedNode.role !== 'client' && (
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
            {t(language, 'nodeEditor.publiclyReachable')}
          </label>
        )}
        {/* Pin-key is a LOCAL/air-gap custody control (see the mode note above): hidden in
            controller mode, where the agent holds the key and the server is zero-knowledge. */}
        {mode === 'local' && (
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
            {t(language, 'nodeEditor.pinPrivateKeyPersist')}
          </label>
        )}
        {mode === 'local' && selectedNode.fixed_private_key && (
          <div className="p-2 bg-gray-700 rounded space-y-1">
            <p className="text-xs text-gray-300">{t(language, 'nodeEditor.pinnedKeyStatus')}</p>
            <p className="text-xs text-gray-400 break-all">
              {t(language, 'nodeEditor.publicKey')}: {selectedNode.wireguard_public_key || t(language, 'nodeEditor.willBeGeneratedOn')}
            </p>
            <p className="text-xs text-gray-500 break-all">
              {t(language, 'nodeEditor.privateKey')}: {selectedNode.wireguard_private_key ? t(language, 'nodeEditor.generatedAndPersisted') : t(language, 'nodeEditor.notGeneratedYet')}
            </p>
          </div>
        )}
        {selectedNode.role !== 'client' && (
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
            {t(language, 'nodeEditor.canForwardTraffic')}
          </label>
        )}
        {selectedNode.role !== 'client' && (
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <label className="text-xs text-gray-400">{t(language, 'nodeEditor.publicAddressesHowPeers')}</label>
              <button
                onClick={() => addNodeEndpoint(selectedNode.id)}
                className="text-xs px-2 py-0.5 rounded bg-blue-600 hover:bg-blue-500"
              >
                + {t(language, 'nodeEditor.add')}
              </button>
            </div>
            {(selectedNode.public_endpoints || []).length === 0 && (
              <p className="text-xs text-gray-500 italic">{t(language, 'nodeEditor.noPublicAddressesConfigured')}</p>
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
                    placeholder={t(language, 'nodeEditor.ipDomain')}
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
                    placeholder={t(language, 'nodeEditor.port')}
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
                    placeholder={t(language, 'nodeEditor.noteOptional')}
                    className="flex-1 px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500"
                  />
                  <button
                    onClick={() => removeNodeEndpoint(selectedNode.id, ep.id)}
                    className="px-2 py-1 text-xs bg-red-600 hover:bg-red-500 rounded"
                  >
                    {t(language, 'nodeEditor.delete')}
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
        {/* 对外通告的局域网网段（extra_prefixes）。角色门控严格对应 roles.go 的
            BabelAnnounce.AnnounceExtraPrefixes：gateway 恒为 true（始终展示，无提示）；
            router/relay 在设置后才通告（展示并附提示）；peer/client 为 no-op（不展示）。 */}
        {(selectedNode.role === 'gateway' ||
          selectedNode.role === 'router' ||
          selectedNode.role === 'relay') && (
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <label className="text-xs text-gray-400">{t(language, 'nodeEditor.advertisedLANPrefixes')}</label>
              <button
                onClick={() => addExtraPrefix(selectedNode.id)}
                className="text-xs px-2 py-0.5 rounded bg-blue-600 hover:bg-blue-500"
              >
                + {t(language, 'nodeEditor.add_2')}
              </button>
            </div>
            {(selectedNode.role === 'router' || selectedNode.role === 'relay') && (
              <p className="text-[10px] text-gray-500">
                {t(language, 'nodeEditor.whenSetThisNode')}
              </p>
            )}
            {(selectedNode.extra_prefixes || []).length === 0 && (
              <p className="text-xs text-gray-500 italic">{t(language, 'nodeEditor.noLANPrefixesConfigured')}</p>
            )}
            {(selectedNode.extra_prefixes || []).map((prefix, index) => (
              <div key={`extra-prefix-${index}`} className="flex gap-1">
                <input
                  type="text"
                  value={prefix}
                  onChange={(e) => updateExtraPrefix(selectedNode.id, index, e.target.value)}
                  pattern="^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$"
                  title={t(language, 'nodeEditor.ipv4CIDRFormatE')}
                  placeholder="192.168.1.0/24"
                  className="flex-1 px-2 py-1 bg-gray-600 rounded text-xs border border-gray-500 focus:border-blue-400 outline-none"
                />
                <button
                  onClick={() => removeExtraPrefix(selectedNode.id, index)}
                  className="px-2 py-1 text-xs bg-red-600 hover:bg-red-500 rounded"
                >
                  {t(language, 'nodeEditor.delete_2')}
                </button>
              </div>
            ))}
          </div>
        )}
        {/* SSH Connection Details (collapsible) */}
        <details className="bg-gray-700/50 rounded p-2">
          <summary className="text-xs cursor-pointer text-gray-400 font-semibold">
            {t(language, 'nodeEditor.sshConnectionAutoDeploy')}
          </summary>
          <div className="mt-2 space-y-2">
            <div>
              <label className="text-xs text-gray-400">{t(language, 'nodeEditor.sshAliasSshConfig')}</label>
              <input
                type="text"
                value={selectedNode.ssh_alias || ''}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    ssh_alias: e.target.value || undefined,
                  })
                }
                pattern="^[A-Za-z0-9._:@-]+$"
                title={t(language, 'nodeEditor.onlyLettersDigitsAre')}
                placeholder={t(language, 'nodeEditor.eGMyServer')}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
              <p className="text-[10px] text-gray-500 mt-0.5">
                {t(language, 'nodeEditor.ifSetOverridesManual')}
              </p>
            </div>
            <div>
              <label className="text-xs text-gray-400">{t(language, 'nodeEditor.sshHost')}</label>
              <input
                type="text"
                value={selectedNode.ssh_host || ''}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    ssh_host: e.target.value || undefined,
                  })
                }
                pattern="^[A-Za-z0-9._:@-]+$"
                title={t(language, 'nodeEditor.onlyLettersDigitsAre_2')}
                placeholder={t(language, 'nodeEditor.ipOrHostname')}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
            <div className="grid grid-cols-2 gap-2">
              <div>
                <label className="text-xs text-gray-400">{t(language, 'nodeEditor.sshPort')}</label>
                <input
                  type="number"
                  min={1}
                  max={65535}
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
                <label className="text-xs text-gray-400">{t(language, 'nodeEditor.sshUser')}</label>
                <input
                  type="text"
                  value={selectedNode.ssh_user || ''}
                  onChange={(e) =>
                    updateNode(selectedNode.id, {
                      ssh_user: e.target.value || undefined,
                    })
                  }
                  pattern="^[A-Za-z0-9._:@-]+$"
                  title={t(language, 'nodeEditor.onlyLettersDigitsAre_3')}
                  placeholder="root"
                  className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                />
              </div>
            </div>
            <div>
              <label className="text-xs text-gray-400">{t(language, 'nodeEditor.sshKeyPath')}</label>
              <input
                type="text"
                value={selectedNode.ssh_key_path || ''}
                onChange={(e) =>
                  updateNode(selectedNode.id, {
                    ssh_key_path: e.target.value || undefined,
                  })
                }
                placeholder={t(language, 'nodeEditor.eGSshId')}
                className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
              />
            </div>
          </div>
        </details>
        <button
          onClick={() => removeNode(selectedNode.id)}
          className="w-full py-1 bg-red-600 hover:bg-red-500 rounded text-sm"
        >
          {t(language, 'nodeEditor.deleteNode')}
        </button>
      </div>
    </section>
  );
}
