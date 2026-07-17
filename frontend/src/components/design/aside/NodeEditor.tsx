import { useTopologyStore } from '../../../stores/topologyStore';
import { useControllerStore } from '../../../stores/controllerStore';
import { t, type MessageKey } from '../../../i18n';
import { deriveCapabilitiesFromRole, type NodeRole } from '../../../lib/roleCapabilities';
import { nodeDeploymentModeUpdate } from '../../../lib/nodeDeploymentMode';
import { uuid } from '../../../lib/uuid';
import { Field, FIELD_SELECT_CLASS } from '../../../ui/Field';

// NATIVE_XDP_KEY maps the fleet node's reported native-XDP capability (plan-4) to a localized hint
// shown as an ALWAYS-VISIBLE per-node indicator (a pre-decision aid: you can see whether the NIC
// supports native BEFORE selecting it, not only after) — closed set, so a garbled value renders nothing.
const NATIVE_XDP_KEY: Record<'supported' | 'conditional' | 'unsupported' | 'unknown', MessageKey> = {
  supported: 'nodeEditor.nativeXdp.supported',
  conditional: 'nodeEditor.nativeXdp.conditional',
  unsupported: 'nodeEditor.nativeXdp.unsupported',
  unknown: 'nodeEditor.nativeXdp.unknown',
};

// Node property editor (extracted verbatim from RightPanel's selected-node block; covers public
// addresses / advertised prefixes / SSH config, plus the coupling to reconcileEdgeEndpoints).
// Used by the selection-driven Design right-side aside.
export function NodeEditor() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
  const edges = useTopologyStore((s) => s.edges);
  const domains = useTopologyStore((s) => s.domains);
  const selectedNodeId = useTopologyStore((s) => s.selectedNodeId);
  const updateNode = useTopologyStore((s) => s.updateNode);
  const removeNode = useTopologyStore((s) => s.removeNode);
  const reconcileEdgeEndpoints = useTopologyStore((s) => s.reconcileEdgeEndpoints);
  // plan-4: the fleet node's PRE-DEPLOY native-XDP capability heuristic (controller mode only; the
  // agent reports it via /telemetry). Drives the warning below when native is selected on a NIC that
  // reports it unsupported/conditional. Undefined in local mode / before the first heartbeat.
  const nativeXDP = useControllerStore((s) => s.nodes.find((n) => n.nodeId === selectedNodeId)?.nativeXDP);
  // mimicCapability (plan-3): can this node build/load the mimic kernel module. "unbuildable" warns
  // that a transport=tcp link here will fall back per policy (the stale-kernel case). Live-only.
  const mimicCapability = useControllerStore((s) => s.nodes.find((n) => n.nodeId === selectedNodeId)?.mimicCapability);
  // Only warn about mimic buildability on a node that actually has a transport=tcp link — mimic is
  // irrelevant otherwise, so a bare "unbuildable" would nag every pending-reboot fleet node.
  const nodeHasTcpLink = edges.some(
    (e) => (e.from_node_id === selectedNodeId || e.to_node_id === selectedNodeId) && e.transport === 'tcp',
  );
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
    // When a host is renamed, reconcile edges that point at this node and snapshotted the old host, to avoid dialing a stale target
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
      port: 51820,
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
    // When this host is removed, clear the endpoint of edges that point at it so the connection falls back to backend auto-resolution
    if (removed?.host) {
      reconcileEdgeEndpoints(nodeId, removed.host, null);
    }
  };

  // extra_prefixes is a plain string array (no stable id), so add/remove/edit happen by index --
  // in contrast to the public-address list above (an object array operated on by id).
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
      <h2 className="text-sm font-semibold text-[var(--content-muted)] uppercase tracking-wider mb-2">
        {t(language, 'nodeEditor.nodeProperties')}
      </h2>
      <div className="space-y-2">
        <Field
          label={t(language, 'nodeEditor.name')}
          type="text"
          value={selectedNode.name}
          onChange={(e) => updateNode(selectedNode.id, { name: e.target.value })}
          pattern="^[A-Za-z0-9 ._-]+$"
          title={t(language, 'nodeEditor.onlyLettersDigitsSpace')}
        />
        <Field
          label={t(language, 'nodeEditor.hostnameOptional')}
          type="text"
          value={selectedNode.hostname || ''}
          onChange={(e) =>
            updateNode(selectedNode.id, {
              hostname: e.target.value || undefined,
            })
          }
        />
        <Field label={t(language, 'nodeEditor.role')}>
          <select
            value={selectedNode.role}
            onChange={(e) => {
              const newRole = e.target.value as NodeRole;
              // Re-derive can_forward/can_relay/can_accept_inbound on every role change (D54),
              // matching the inference in NodeForm/roles.go; the client role forces
              // has_public_ip=false, while other roles keep the operator's set has_public_ip.
              const operatorHasPublicIP =
                newRole === 'client' ? false : selectedNode.capabilities.has_public_ip;
              updateNode(selectedNode.id, {
                role: newRole,
                capabilities: deriveCapabilitiesFromRole(newRole, operatorHasPublicIP),
              });
            }}
            className={FIELD_SELECT_CLASS}
          >
            <option value="peer">Peer</option>
            <option value="router">Router</option>
            <option value="relay">Relay</option>
            <option value="gateway">Gateway</option>
            <option value="client">Client</option>
          </select>
        </Field>
        {/* Deployment mode (controller only): a MANUAL node is hand-deployed (no agent). It carries its
            own pre-known public key + endpoint in the design (mixed-controller-local-mode plan-6). */}
        {mode === 'controller' && (
          <div>
            <label className="text-xs text-[var(--content-muted)]">{t(language, 'nodeEditor.deploymentMode')}</label>
            <select
              value={selectedNode.deployment_mode === 'manual' ? 'manual' : 'managed'}
              onChange={(e) => updateNode(
                selectedNode.id,
                nodeDeploymentModeUpdate(
                  selectedNode,
                  e.target.value === 'manual' ? 'manual' : 'managed',
                ),
              )}
              className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
            >
              <option value="managed">Managed (agent)</option>
              <option value="manual">Manual (no agent)</option>
            </select>
            {selectedNode.deployment_mode === 'manual' && (
              <div className="mt-1 space-y-1">
                <label className="text-xs text-[var(--content-muted)]">{t(language, 'nodeEditor.manualPublicKey')}</label>
                <input
                  type="text"
                  value={selectedNode.wireguard_public_key || ''}
                  onChange={(e) =>
                    updateNode(selectedNode.id, { wireguard_public_key: e.target.value || undefined })
                  }
                  placeholder={t(language, 'nodeEditor.manualPublicKeyHint')}
                  className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm font-mono border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
                />
                <p className="text-xs text-[var(--content-muted)]">{t(language, 'nodeEditor.manualHint')}</p>
              </div>
            )}
          </div>
        )}
        <Field label={t(language, 'nodeEditor.domain')}>
          <select
            value={selectedNode.domain_id}
            onChange={(e) => updateNode(selectedNode.id, { domain_id: e.target.value })}
            className={FIELD_SELECT_CLASS}
          >
            {domains.map((d) => (
              <option key={d.id} value={d.id}>{d.name}</option>
            ))}
          </select>
        </Field>
        <Field
          label={t(language, 'nodeEditor.overlayIPEmptyFor')}
          type="text"
          value={selectedNode.overlay_ip || ''}
          onChange={(e) => updateNode(selectedNode.id, { overlay_ip: e.target.value || undefined })}
          placeholder={t(language, 'nodeEditor.autoAssigned')}
        />
        <Field
          label={t(language, 'nodeEditor.mtuEmptyForDefault')}
          type="number"
          min={576}
          max={65535}
          value={selectedNode.mtu || ''}
          onChange={(e) => updateNode(selectedNode.id, { mtu: parseInt(e.target.value) || undefined })}
          placeholder="1420"
        />
        {/* mimic XDP mode: only takes effect when this node has a transport=tcp link. Defaults to
            skb (generic, compatible with VPS NICs that do not support native); the operator can
            pick native for better performance once they confirm the NIC supports it. */}
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'xdpModeLabel')}</label>
          <select
            value={selectedNode.xdp_mode || 'skb'}
            onChange={(e) => updateNode(selectedNode.id, { xdp_mode: e.target.value === 'native' ? 'native' : undefined })}
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
          >
            <option value="skb">{t(language, 'nodeEditor.skbGenericDefault')}</option>
            <option value="native">{t(language, 'nodeEditor.nativeFasterNeedsNIC')}</option>
          </select>
          <p className="mt-1 text-xs text-[var(--content-muted)]">{t(language, 'xdpModeHint')}</p>
          {nativeXDP && (
            <p
              data-testid="node-native-xdp-hint"
              className={`mt-1 text-xs ${
                nativeXDP.capability === 'unsupported'
                  ? 'text-[var(--warning)]'
                  : nativeXDP.capability === 'supported'
                    ? 'text-[var(--success)]'
                    : 'text-[var(--content-muted)]'
              }`}
            >
              {t(language, NATIVE_XDP_KEY[nativeXDP.capability], { driver: nativeXDP.driver || '?' })}
            </p>
          )}
          {nodeHasTcpLink && mimicCapability?.capability === 'unbuildable' && (
            <p data-testid="node-mimic-capability-hint" className="mt-1 text-xs text-[var(--warning)]">
              {t(language, 'nodeEditor.mimicCapability.unbuildable', { kernel: mimicCapability.kernel || '?' })}
            </p>
          )}
        </div>
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'nodeEditor.mimicEgressLabel')}</label>
          <input
            type="text"
            value={selectedNode.mimic_egress_interface || ''}
            onChange={(e) => updateNode(selectedNode.id, { mimic_egress_interface: e.target.value.trim() || undefined })}
            placeholder={t(language, 'nodeEditor.mimicEgressPlaceholder')}
            data-testid="node-mimic-egress-interface"
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
          />
          <p className="mt-1 text-xs text-[var(--content-muted)]">{t(language, 'nodeEditor.mimicEgressHint')}</p>
        </div>
        {selectedNode.role !== 'client' && (
          <Field
            label={t(language, 'nodeEditor.routerIdLabel')}
            type="text"
            value={selectedNode.router_id || ''}
            onChange={(e) => updateNode(selectedNode.id, { router_id: e.target.value || undefined })}
            placeholder={t(language, 'nodeEditor.routerIdPlaceholder')}
            pattern="^(([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}|(\d{1,3}\.){3}\d{1,3})$"
            title={t(language, 'nodeEditor.routerIdHint')}
          />
        )}
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
          <div className="p-2 bg-[var(--control)] rounded space-y-1">
            <p className="text-xs text-[var(--content)]">{t(language, 'nodeEditor.pinnedKeyStatus')}</p>
            <p className="text-xs text-[var(--content-muted)] break-all">
              {t(language, 'nodeEditor.publicKey')}: {selectedNode.wireguard_public_key || t(language, 'nodeEditor.willBeGeneratedOn')}
            </p>
            <p className="text-xs text-[var(--content-muted)] break-all">
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
              <label className="text-xs text-[var(--content-muted)]">{t(language, 'nodeEditor.publicAddressesHowPeers')}</label>
              <button
                onClick={() => addNodeEndpoint(selectedNode.id)}
                className="text-xs px-2 py-0.5 rounded bg-[var(--accent)] hover:bg-[var(--accent-hover)] text-[var(--accent-fg)]"
              >
                + {t(language, 'nodeEditor.add')}
              </button>
            </div>
            {(selectedNode.public_endpoints || []).length === 0 && (
              <p className="text-xs text-[var(--content-muted)] italic">{t(language, 'nodeEditor.noPublicAddressesConfigured')}</p>
            )}
            {(selectedNode.public_endpoints || []).map((ep) => (
              <div key={ep.id} className="p-2 bg-[var(--control)] rounded space-y-1">
                <div className="grid grid-cols-3 gap-1">
                  <input
                    type="text"
                    value={ep.host}
                    onChange={(e) =>
                      updateNodeEndpoint(selectedNode.id, ep.id, { host: e.target.value })
                    }
                    placeholder={t(language, 'nodeEditor.ipDomain')}
                    className="col-span-2 px-2 py-1 bg-[var(--control)] rounded text-xs border border-[var(--hairline)]"
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
                    className="px-2 py-1 bg-[var(--control)] rounded text-xs border border-[var(--hairline)]"
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
                    className="flex-1 px-2 py-1 bg-[var(--control)] rounded text-xs border border-[var(--hairline)]"
                  />
                  <button
                    onClick={() => removeNodeEndpoint(selectedNode.id, ep.id)}
                    className="px-2 py-1 text-xs bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] text-[var(--danger-solid-fg)] rounded"
                  >
                    {t(language, 'nodeEditor.delete')}
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
        {/* Advertised LAN prefixes (extra_prefixes). The role gating maps strictly to roles.go's
            BabelAnnounce.AnnounceExtraPrefixes: gateway is always true (always shown, no hint);
            router/relay only advertise once set (shown with a hint); peer/client are a no-op (not shown). */}
        {(selectedNode.role === 'gateway' ||
          selectedNode.role === 'router' ||
          selectedNode.role === 'relay') && (
          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <label className="text-xs text-[var(--content-muted)]">{t(language, 'nodeEditor.advertisedLANPrefixes')}</label>
              <button
                onClick={() => addExtraPrefix(selectedNode.id)}
                className="text-xs px-2 py-0.5 rounded bg-[var(--accent)] hover:bg-[var(--accent-hover)] text-[var(--accent-fg)]"
              >
                + {t(language, 'nodeEditor.add_2')}
              </button>
            </div>
            {(selectedNode.role === 'router' || selectedNode.role === 'relay') && (
              <p className="text-[10px] text-[var(--content-muted)]">
                {t(language, 'nodeEditor.whenSetThisNode')}
              </p>
            )}
            {(selectedNode.extra_prefixes || []).length === 0 && (
              <p className="text-xs text-[var(--content-muted)] italic">{t(language, 'nodeEditor.noLANPrefixesConfigured')}</p>
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
                  className="flex-1 px-2 py-1 bg-[var(--control)] rounded text-xs border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
                />
                <button
                  onClick={() => removeExtraPrefix(selectedNode.id, index)}
                  className="px-2 py-1 text-xs bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] text-[var(--danger-solid-fg)] rounded"
                >
                  {t(language, 'nodeEditor.delete_2')}
                </button>
              </div>
            ))}
          </div>
        )}
        {/* SSH Connection / Auto-Deploy is LOCAL/air-gap deploy-script metadata: the ssh_* fields
            feed downloadDeployScript/exportArtifacts (both local-only); the controller agent-pull
            model never pushes over SSH. Hide the EDITOR in controller mode (plan-11 / T4 review),
            where it is a dead, misleading affordance. Do NOT strip the ssh_* DATA — custody.ts
            deliberately preserves it so a controller→local switch retains the operator's SSH config. */}
        {mode === 'local' && (
        <details className="bg-[var(--control)] rounded p-2">
          <summary className="text-xs cursor-pointer text-[var(--content-muted)] font-semibold">
            {t(language, 'nodeEditor.sshConnectionAutoDeploy')}
          </summary>
          <div className="mt-2 space-y-2">
            <Field
              label={t(language, 'nodeEditor.sshAliasSshConfig')}
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
              hint={t(language, 'nodeEditor.ifSetOverridesManual')}
            />
            <Field
              label={t(language, 'nodeEditor.sshHost')}
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
            />
            <div className="grid grid-cols-2 gap-2">
              <Field
                label={t(language, 'nodeEditor.sshPort')}
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
              />
              <Field
                label={t(language, 'nodeEditor.sshUser')}
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
              />
            </div>
            <Field
              label={t(language, 'nodeEditor.sshKeyPath')}
              type="text"
              value={selectedNode.ssh_key_path || ''}
              onChange={(e) =>
                updateNode(selectedNode.id, {
                  ssh_key_path: e.target.value || undefined,
                })
              }
              placeholder={t(language, 'nodeEditor.eGSshId')}
            />
          </div>
        </details>
        )}
        <button
          onClick={() => removeNode(selectedNode.id)}
          className="w-full py-1 bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] text-[var(--danger-solid-fg)] rounded text-sm"
        >
          {t(language, 'nodeEditor.deleteNode')}
        </button>
      </div>
    </section>
  );
}
