import { useTopologyStore } from '../../../stores/topologyStore';
import { t } from '../../../i18n';
import { resolveEdgeInterface } from '../../../lib/compiledInterfaces';
import { flipEdge, reverseDialSource } from '../../../lib/edgeDirection';
import { clearedPinFields } from '../../../lib/normalizeEdges';
import { Field, FIELD_SELECT_CLASS } from '../../../ui/Field';

// MIN_PINNED_PORT mirrors the backend's minPinnedPort (validator) — the lower bound for an
// operator-chosen pinned listen port. Auto-allocation still starts at 51820, but a port-
// restricted NAT VPS may forward a fixed range below it, so manual pins go down to 1024.
const MIN_PINNED_PORT = 1024;

// Default transit pool when a domain leaves transit_cidr empty (mirrors the backend default).
const DEFAULT_TRANSIT_CIDR = '10.10.0.0/24';

// ipv4ToInt parses an IPv4 dotted-quad to a uint32, or null when malformed.
function ipv4ToInt(ip: string): number | null {
  const parts = ip.trim().split('.');
  if (parts.length !== 4) return null;
  let n = 0;
  for (const p of parts) {
    if (!/^\d+$/.test(p)) return null;
    const o = parseInt(p, 10);
    if (o < 0 || o > 255) return null;
    n = (n << 8) | o;
  }
  return n >>> 0;
}

// ipv4InCidr reports whether an IPv4 address falls within an IPv4 CIDR. IPv4-only (transit pools
// are IPv4); returns false on malformed input so the UI flags it (the backend validator is the
// authoritative check — this is just early, inline feedback before Save).
function ipv4InCidr(ip: string, cidr: string): boolean {
  const [net, bitsStr] = cidr.split('/');
  const bits = parseInt(bitsStr, 10);
  if (isNaN(bits) || bits < 0 || bits > 32) return false;
  const ipInt = ipv4ToInt(ip);
  const netInt = ipv4ToInt(net);
  if (ipInt === null || netInt === null) return false;
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  return (ipInt & mask) === (netInt & mask);
}

// Connection (edge) property editor (extracted verbatim from RightPanel's selected-edge block;
// covers target-endpoint selection / transport / link role / priority / weight / backup link /
// pinned allocation / post-compile actual values). Used by the Design right-side aside.
export function EdgeEditor() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
  const domains = useTopologyStore((s) => s.domains);
  const edges = useTopologyStore((s) => s.edges);
  const selectedEdgeId = useTopologyStore((s) => s.selectedEdgeId);
  const updateEdge = useTopologyStore((s) => s.updateEdge);
  const removeEdge = useTopologyStore((s) => s.removeEdge);
  const addBackupEdge = useTopologyStore((s) => s.addBackupEdge);
  const compileResult = useTopologyStore((s) => s.compileResult);

  const selectedEdge = edges.find((e) => e.id === selectedEdgeId);
  const selectedEdgeTarget = selectedEdge
    ? nodes.find((n) => n.id === selectedEdge.to_node_id)
    : undefined;

  const targetEndpointOptions = selectedEdgeTarget?.public_endpoints || [];
  // Deduplicate hosts from target's public endpoints for IP picker
  const targetHostOptions = Array.from(
    new Set(targetEndpointOptions.map((ep) => ep.host).filter(Boolean)),
  );
  const matchedTargetHost = selectedEdge?.endpoint_host
    ? targetHostOptions.includes(selectedEdge.endpoint_host)
      ? `host:${selectedEdge.endpoint_host}`
      : '__manual__'
    : '__none__';

  // Get the compiled port for the selected edge from the compiled topology
  const compiledEdgePort = (() => {
    if (!compileResult || !selectedEdge) return undefined;
    const compiledEdge = compileResult.topology.edges?.find((e) => e.id === selectedEdge.id);
    return compiledEdge?.compiled_port || undefined;
  })();

  // Parallel links (edge.md): a backup link is derived from the primary link.
  // The selected edge's source node (the client role gates the backup button: the backend
  // rejects backup links on a client).
  const selectedEdgeFrom = selectedEdge
    ? nodes.find((n) => n.id === selectedEdge.from_node_id)
    : undefined;
  const selectedEdgeIsBackup = selectedEdge?.role === 'backup';
  const selectedEdgeTouchesClient =
    selectedEdgeFrom?.role === 'client' || selectedEdgeTarget?.role === 'client';
  // Backup button: hidden when either source/target is a client (the backend rejects it), and
  // hidden when the selected edge is already a backup (a backup is added from the primary link,
  // not derived from another backup).
  const showAddBackupButton = !!selectedEdge && !selectedEdgeIsBackup && !selectedEdgeTouchesClient;
  // Path-diversity nudge: the selected backup link shares a public address with another edge of
  // the same node pair, meaning the backup does not point at an independent path (addBackupEdge
  // copied the primary link's endpoint_host); nudge the operator to point it elsewhere.
  const showBackupEndpointNudge =
    !!selectedEdge &&
    selectedEdgeIsBackup &&
    !!selectedEdge.endpoint_host &&
    edges.some(
      (e) =>
        e.id !== selectedEdge.id &&
        e.from_node_id === selectedEdge.from_node_id &&
        e.to_node_id === selectedEdge.to_node_id &&
        e.endpoint_host === selectedEdge.endpoint_host,
    );

  if (!selectedEdge) return null;

  // Directional NAT target (PR2): the internal listen port a NAT forward must hit. The
  // compiler renders a forward edge's endpoint UNCONDITIONALLY at the to-side port —
  // formatEndpoint(edge.EndpointHost, alloc.toPort), written back to pinned_to_port and
  // echoed as compiled_port (compiler peers.go / compiler.go); it never branches on which
  // node owns the host string. endpoint_host on the canvas is likewise always a snapshot of
  // the TO node (reconcileEdgeEndpoints only writes it for the edge's target). So a forward
  // edge always dials the to-node at pinned_to_port — mirror that here. Sourced from the
  // edge's own fields — independent of the controller-null compileResult.
  const natTargetPort = selectedEdge.pinned_to_port;
  const natTargetNode = selectedEdgeTarget;
  // External dial port: the NAT-override endpoint_port when set, else the compiled echo (or the
  // internal listen port when nothing else is known). When it differs from the internal listen
  // port an external→internal forward is required — surface the hint then.
  const natDialPort =
    selectedEdge.endpoint_port && selectedEdge.endpoint_port > 0
      ? selectedEdge.endpoint_port
      : selectedEdge.compiled_port ?? natTargetPort;
  const natForwardActive = natTargetPort !== undefined && natDialPort !== natTargetPort;
  const hasPinnedPort =
    selectedEdge.pinned_from_port !== undefined || selectedEdge.pinned_to_port !== undefined;

  // PR7 — operator-settable pin validation (inline early feedback; the backend validator is the
  // authoritative gate at Validate/Compile/Deploy). The transit pool is resolved from the edge's
  // from-node domain (default 10.10.0.0/24), matching the backend's edgeTransitCIDR resolution.
  const edgeTransitCidr =
    (selectedEdgeFrom && domains.find((d) => d.id === selectedEdgeFrom.domain_id)?.transit_cidr) ||
    DEFAULT_TRANSIT_CIDR;
  const portPairIncomplete =
    (selectedEdge.pinned_from_port !== undefined) !== (selectedEdge.pinned_to_port !== undefined);
  const portOutOfRange = [selectedEdge.pinned_from_port, selectedEdge.pinned_to_port].some(
    (p) => p !== undefined && (p < MIN_PINNED_PORT || p > 65535),
  );
  const transitPairIncomplete =
    !!selectedEdge.pinned_from_transit_ip !== !!selectedEdge.pinned_to_transit_ip;
  const transitOutOfPool = [
    selectedEdge.pinned_from_transit_ip,
    selectedEdge.pinned_to_transit_ip,
  ].some((ip) => !!ip && !ipv4InCidr(ip, edgeTransitCidr));
  const hasLinkLocalPin =
    selectedEdge.pinned_from_link_local !== undefined ||
    selectedEdge.pinned_to_link_local !== undefined;
  // The pinned-allocation editor shows once the edge carries ANY pin (the common post-Compile /
  // post-Deploy state) so the operator can adjust the NAT-relevant values, then Save.
  const hasAnyPin =
    hasPinnedPort ||
    selectedEdge.pinned_from_transit_ip !== undefined ||
    selectedEdge.pinned_to_transit_ip !== undefined ||
    hasLinkLocalPin;

  // setPinPort maps a number input's raw value to a pin field value: '' clears the pin
  // (undefined); a valid integer sets it; anything else is ignored (keeps the prior value).
  const setPinPort = (field: 'pinned_from_port' | 'pinned_to_port', raw: string) => {
    if (raw === '') {
      updateEdge(selectedEdge.id, { [field]: undefined });
      return;
    }
    const parsed = parseInt(raw, 10);
    if (!isNaN(parsed)) updateEdge(selectedEdge.id, { [field]: parsed });
  };
  const setPinTransit = (
    field: 'pinned_from_transit_ip' | 'pinned_to_transit_ip',
    raw: string,
  ) => updateEdge(selectedEdge.id, { [field]: raw || undefined });

  return (
    <section>
      <h2 className="text-sm font-semibold text-[var(--content-muted)] uppercase tracking-wider mb-2">
        {t(language, 'edgeEditor.edgeProperties')}
      </h2>
      <div className="space-y-2">
        <Field label={t(language, 'edgeEditor.type')}>
          <select
            value={selectedEdge.type}
            onChange={(e) =>
              updateEdge(selectedEdge.id, {
                type: e.target.value as 'direct' | 'public-endpoint' | 'relay-path' | 'candidate',
                // Clear the stale compiled port so the canvas label immediately reflects the latest intent (until recompile)
                compiled_port: undefined,
              })
            }
            className={FIELD_SELECT_CLASS}
          >
            <option value="direct">{t(language, 'edgeEditor.typeDirect')}</option>
            <option value="public-endpoint">{t(language, 'edgeEditor.typePublicEndpoint')}</option>
            <option value="relay-path">{t(language, 'edgeEditor.typeRelayPath')}</option>
            <option value="candidate">{t(language, 'edgeEditor.typeCandidate')}</option>
          </select>
        </Field>
        {/* Endpoint IP — pick from target's public IPs or manual */}
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'edgeEditor.endpointIPFromTarget')}</label>
          {targetHostOptions.length > 0 && (
            <select
              value={matchedTargetHost}
              onChange={(e) => {
                const value = e.target.value;
                if (value === '__none__') {
                  // Clear the port WITH the host (require-explicit-host coupling): a port override with
                  // no host is invalid + rejected by the validator, so unsetting the host must not
                  // orphan the port into that inconsistent state.
                  updateEdge(selectedEdge.id, {
                    endpoint_host: undefined,
                    endpoint_port: undefined,
                    compiled_port: undefined,
                  });
                  return;
                }
                if (value === '__manual__') {
                  // Keep the current value — user will type in the text input below
                  return;
                }
                const host = value.replace('host:', '');
                updateEdge(selectedEdge.id, {
                  endpoint_host: host,
                  compiled_port: undefined,
                });
              }}
              className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
            >
              <option value="__none__">{t(language, 'edgeEditor.unset')}</option>
              {targetHostOptions.map((host) => (
                <option key={host} value={`host:${host}`}>
                  {host}
                </option>
              ))}
              <option value="__manual__">{t(language, 'edgeEditor.manualInput')}</option>
            </select>
          )}
          <input
            key={`ep-host-${selectedEdge.id}`}
            data-testid="edge-endpoint-host-input"
            type="text"
            value={selectedEdge.endpoint_host || ''}
            onChange={(e) => updateEdge(selectedEdge.id, { endpoint_host: e.target.value || undefined, compiled_port: undefined })}
            placeholder={t(language, 'edgeEditor.ipOrHostname')}
            className="w-full mt-1 px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
          />
        </div>
        {/* Endpoint Port — 0 or empty = auto, nonzero = NAT/port-forward override */}
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'edgeEditor.endpointPort0Auto')}</label>
          <div className="flex gap-1 items-center">
            <input
              key={`ep-port-${selectedEdge.id}`}
              type="number"
              value={selectedEdge.endpoint_port ?? ''}
              onChange={(e) => {
                const raw = e.target.value;
                if (raw === '') {
                  updateEdge(selectedEdge.id, { endpoint_port: undefined, compiled_port: undefined });
                } else {
                  const parsed = parseInt(raw, 10);
                  if (!isNaN(parsed)) {
                    updateEdge(selectedEdge.id, { endpoint_port: parsed, compiled_port: undefined });
                  }
                }
              }}
              placeholder={t(language, 'edgeEditor.0Auto')}
              className="flex-1 px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
            />
          </div>
          {compiledEdgePort && (
            <p className="text-[10px] text-[var(--info)] mt-0.5 font-mono">
              {t(language, 'edgeEditor.compiledPort')}: {compiledEdgePort}
              {/* Require an endpoint_host: a port-only override is invalid (require-explicit-host) and
                  the compiler ignores it, so the badge must not claim it is active without a host. */}
              {selectedEdge.endpoint_host && selectedEdge.endpoint_port && selectedEdge.endpoint_port > 0 && selectedEdge.endpoint_port !== compiledEdgePort && (
                <span className="text-[var(--warning)] ml-1">
                  ({t(language, 'edgeEditor.natOverrideActive')})
                </span>
              )}
            </p>
          )}
        </div>
        {/* Link direction (edge.md §Link direction, D11): the model stores only ''≡both / 'forward'
            — one spelling. The "to(A)" option is an explicit edge FLIP (swap from/to, mirror the
            pin pairs — allocation-stable — clear the stale dial fields, prefill the new target's
            public host), so the drawn arrow always equals the dial direction. Hidden on client
            edges (the validator forbids a direction there: client dial semantics are fixed). */}
        {!selectedEdgeTouchesClient && selectedEdgeFrom && selectedEdgeTarget && (
          <div>
            <label className="text-xs text-[var(--content-muted)]">{t(language, 'edgeEditor.linkDirection')}</label>
            <select
              data-testid="link-direction-select"
              value={selectedEdge.link_direction === 'forward' ? 'forward' : 'both'}
              onChange={(e) => {
                const v = e.target.value;
                if (v === 'both') {
                  updateEdge(selectedEdge.id, { link_direction: undefined });
                } else if (v === 'forward') {
                  updateEdge(selectedEdge.id, { link_direction: 'forward' });
                } else {
                  // 'flip' — single-link toward the current FROM node: redraw the edge in the
                  // opposite direction (one atomic store write) and prefill the dial host from
                  // the NEW target's (the old from-node's) public endpoints when it has one.
                  const flipped = flipEdge(selectedEdge);
                  updateEdge(selectedEdge.id, {
                    ...flipped,
                    endpoint_host: selectedEdgeFrom.public_endpoints?.[0]?.host,
                    link_direction: 'forward',
                  });
                }
              }}
              className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
            >
              <option value="both">
                {t(language, 'edgeEditor.linkDirectionBoth', { from: selectedEdgeFrom.name, to: selectedEdgeTarget.name })}
              </option>
              <option value="forward">
                {t(language, 'edgeEditor.linkDirectionForward', { from: selectedEdgeFrom.name, to: selectedEdgeTarget.name })}
              </option>
              <option value="flip">
                {t(language, 'edgeEditor.linkDirectionFlip', { from: selectedEdgeFrom.name, to: selectedEdgeTarget.name })}
              </option>
            </select>
            <p className="mt-0.5 text-[10px] text-[var(--content-muted)]">
              {t(language, 'edgeEditor.linkDirectionHint')}
            </p>
            {selectedEdge.link_direction === 'forward' && !selectedEdge.endpoint_host && (
              <p className="mt-0.5 text-[10px] text-[var(--warning)]">
                {t(language, 'edgeEditor.linkDirectionForwardNeedsHost')}
              </p>
            )}
            {selectedEdge.link_direction !== 'forward' && (() => {
              // Both-mode readout: where the REVERSE dial (to→from) resolves from at compile time
              // — the asymmetry (one configurable forward dial, one derived reverse dial) should
              // be visible, not tribal knowledge.
              const src = reverseDialSource(selectedEdge, selectedEdgeFrom, edges);
              return (
                <p className="mt-0.5 text-[10px] text-[var(--content-muted)] font-mono" data-testid="reverse-dial-readout">
                  {t(language, 'edgeEditor.linkDirectionReverseDial', {
                    to: selectedEdgeTarget.name,
                    from: selectedEdgeFrom.name,
                    source: src
                      ? `${src.host}${src.kind === 'reverse-edge' ? ` (${t(language, 'edgeEditor.linkDirectionViaReverseEdge')})` : ''}`
                      : t(language, 'edgeEditor.linkDirectionReverseDialNone'),
                  })}
                </p>
              );
            })()}
          </div>
        )}
        {compileResult && (() => {
          // Spec (naming.md / Decisions #12) forbids the frontend from rebuilding interface names
          // (above 12 chars the backend takes a hash-suffix branch, and for parallel links a backup
          // also folds edge.ID into the hash, which the frontend cannot reproduce). Instead the
          // shared resolver resolveEdgeInterface looks the backend's actual generated interface up
          // by the pinned port (ports are unique within a single node => deterministic match), then
          // uses the resolved interface name to pull this end's config body from wireguard_configs
          // (key format "<nodeID>:<interfaceName>") and read its Endpoint line. Takes this edge's
          // from-side interface (from_node_id + pinned_from_port).
          const fromIface = resolveEdgeInterface(
            selectedEdge,
            true,
            compileResult.wireguard_configs,
          );
          if (!fromIface) return null;
          const config =
            compileResult.wireguard_configs[`${selectedEdge.from_node_id}:${fromIface.interfaceName}`];
          const endpointMatch = config?.match(/Endpoint\s*=\s*(.+)/);
          return (
            <div className="p-2 bg-[var(--surface-sunken)] rounded space-y-1">
              <p className="text-xs text-[var(--content-muted)] font-semibold">{t(language, 'edgeEditor.compiledValues')}</p>
              <p className="text-xs text-[var(--info)] font-mono break-all">{t(language, 'edgeEditor.localInterface')}: {fromIface.interfaceName}</p>
              {endpointMatch && (
                <p className="text-xs text-[var(--info)] font-mono break-all">{t(language, 'edgeEditor.endpoint')}: {endpointMatch[1]}</p>
              )}
              {/* Name the node on the local listen port so it does NOT read as a contradiction with
                  the NAT-forward line below: per-peer links have a DISTINCT listen port per end, so
                  this (the from node's, e.g. 51822) and the forward's "→ <to-node> <port>" (the to
                  node's, e.g. 51821) are two different ends, both correct — not a mismatch. */}
              <p className="text-xs text-[var(--info)] font-mono">
                {t(language, 'edgeEditor.localListenPort')}
                {selectedEdgeFrom ? ` (${selectedEdgeFrom.name})` : ''}: {fromIface.listenPort}
              </p>
            </div>
          );
        })()}
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={selectedEdge.is_enabled}
            onChange={(e) => updateEdge(selectedEdge.id, { is_enabled: e.target.checked })}
          />
          {t(language, 'edgeEditor.enabled')}
        </label>
        {/* Transport / priority / weight / notes (D68). priority and weight affect Babel's link cost. */}
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'edgeEditor.transport')}</label>
          <select
            value={selectedEdge.transport || 'udp'}
            onChange={(e) =>
              updateEdge(selectedEdge.id, {
                transport: e.target.value as 'udp' | 'tcp',
              })
            }
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
          >
            <option value="udp">UDP</option>
            <option value="tcp">{t(language, 'edgeEditor.tcpMimic')}</option>
          </select>
          {selectedEdge.transport === 'tcp' && (
            <p className="mt-1 text-xs text-[var(--content-muted)]">
              {t(language, 'mimicHint')}
            </p>
          )}
          {/* Per-link mimic UDP-fallback policy (plan-6), shown only for a tcp (mimic) link. '' clears
              back to inherit (omit from JSON ⇒ back-compat); 'udp'/'none' are pure renderer policy and
              touch no allocation pin. The Subject-2 small-screen gate is structural: EdgeEditor is
              never mounted below lg (DesignPage), so a gated read-only canvas cannot reach updateEdge. */}
          {selectedEdge.transport === 'tcp' && (
            <div className="mt-2">
              <label className="text-xs text-[var(--content-muted)]">{t(language, 'edgeEditor.mimicFallback')}</label>
              <select
                value={selectedEdge.mimic_fallback ?? ''}
                onChange={(e) => {
                  const v = e.target.value;
                  updateEdge(selectedEdge.id, {
                    mimic_fallback: v === '' ? undefined : (v as 'udp' | 'none'),
                  });
                }}
                className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
              >
                <option value="">{t(language, 'edgeEditor.mimicFallbackInherit')}</option>
                <option value="udp">{t(language, 'edgeEditor.mimicFallbackUdp')}</option>
                <option value="none">{t(language, 'edgeEditor.mimicFallbackNone')}</option>
              </select>
              <p className="mt-0.5 text-[10px] text-[var(--content-muted)]">{t(language, 'edgeEditor.mimicFallbackHint')}</p>
            </div>
          )}
        </div>
        {/* Link role (edge.md parallel links): empty = primary class; backup = an independent
            backup link. Changing the role changes the link identity (it re-keys: a backup's LinkKey
            carries a #edgeID suffix, distinct from the same-pair primary), so all allocation pins
            bound to the old identity (compiled_port + the six pinned_*) are now stale and must be
            cleared together -- otherwise an edge flipped from primary to backup would keep the
            primary link's .1/.2/51820 and collide on a pin with the still-present same-pair primary
            (the validator reports "two different links", which is exactly that bug). Once cleared,
            the edge is re-allocated on the next compile (e.g. .3/.4). */}
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'roleLabel')}</label>
          <select
            value={selectedEdge.role || ''}
            onChange={(e) => {
              const value = e.target.value;
              // Changing the role re-keys the link identity, so all allocation pins bound to the
              // old identity are stale and must clear together (see the safety comment above). The
              // pin-clear set is single-sourced via clearedPinFields (lib/normalizeEdges).
              updateEdge(selectedEdge.id, {
                role: value === '' ? undefined : (value as 'primary' | 'backup'),
                ...clearedPinFields(),
              });
            }}
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
          >
            <option value="">{t(language, 'rolePrimary')} ({t(language, 'edgeEditor.default')})</option>
            <option value="primary">{t(language, 'rolePrimary')}</option>
            <option value="backup">{t(language, 'roleBackup')}</option>
          </select>
          <p className="text-[10px] text-[var(--content-muted)] mt-0.5">
            {t(language, 'edgeEditor.backupLinksDefaultTo')}
          </p>
          {hasPinnedPort && (
            <p className="text-[10px] text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded mt-1">
              {t(language, 'edgeEditor.roleChangeRealloc')}
            </p>
          )}
        </div>
        <Field
          label={t(language, 'edgeEditor.priorityDrivesBabelLink')}
          type="number"
          value={selectedEdge.priority ?? ''}
          onChange={(e) => {
            const raw = e.target.value;
            if (raw === '') {
              updateEdge(selectedEdge.id, { priority: undefined });
              return;
            }
            const parsed = parseInt(raw, 10);
            if (!isNaN(parsed)) {
              updateEdge(selectedEdge.id, { priority: parsed });
            }
          }}
          placeholder={t(language, 'edgeEditor.default_2')}
        />
        <Field
          label={t(language, 'edgeEditor.weightDrivesBabelLink')}
          type="number"
          value={selectedEdge.weight ?? ''}
          onChange={(e) => {
            const raw = e.target.value;
            if (raw === '') {
              updateEdge(selectedEdge.id, { weight: undefined });
              return;
            }
            const parsed = parseInt(raw, 10);
            if (!isNaN(parsed)) {
              updateEdge(selectedEdge.id, { weight: parsed });
            }
          }}
          placeholder={t(language, 'edgeEditor.default_3')}
        />
        <Field
          label={t(language, 'edgeEditor.notes')}
          type="text"
          value={selectedEdge.notes || ''}
          onChange={(e) =>
            updateEdge(selectedEdge.id, { notes: e.target.value || undefined })
          }
          placeholder={t(language, 'edgeEditor.notesOptional')}
        />
        {/* Add backup link (edge.md parallel links): derive a parallel edge with role=backup from
            the current (primary) link; the store's addBackupEdge copies from/to/type/transport/
            endpoint_host (but not ports or pins) and auto-selects it. Hidden when either source/
            target is a client (the backend rejects backups on a client), and hidden when the
            selected edge is already a backup (a backup is added from the primary link). */}
        {showAddBackupButton && (
          <button
            onClick={() => addBackupEdge(selectedEdge.id)}
            className="w-full py-1 bg-[var(--accent)] hover:bg-[var(--accent-hover)] text-[var(--accent-fg)] rounded text-sm"
          >
            + {t(language, 'addBackupLink')}
          </button>
        )}
        {showBackupEndpointNudge && (
          <p className="text-[10px] text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded">
            {t(language, 'backupEndpointNudge')}
          </p>
        )}
        {/* Pinned allocation (PR7): the pins written back by the compiler/server, now operator-
            editable -- nail the internal listen ports and transit IPs into the range a
            port-restricted NAT VPS allows; persisted after Save and reused stickily on the next
            compile/deploy. link-local stays read-only (auto fe80::). The checks below are immediate
            inline feedback; the backend validator (Validate/Compile/Deploy) is the authoritative
            gate. See docs/spec/compiler/allocation-stability.md. */}
        {hasAnyPin && (
          <div className="p-2 bg-[var(--surface-sunken)] rounded space-y-2">
            <p className="text-xs text-[var(--content-muted)] font-semibold">
              {t(language, 'edgeEditor.pinnedAllocation')}
            </p>
            {/* Directional NAT readout (info): which internal port the external→internal forward
                must target. Shown when the edge dials a host (endpoint_host). */}
            {hasPinnedPort && selectedEdge.endpoint_host && (
              <div className="space-y-0.5">
                <p className="text-xs text-[var(--info)] font-mono break-all">
                  {t(language, 'edgeEditor.natForwardTitle')}: {selectedEdge.endpoint_host}:{natDialPort ?? '—'} → {natTargetNode?.name ? `${natTargetNode.name} ` : ''}{natTargetPort ?? '—'}
                </p>
                {natForwardActive && (
                  <p className="text-[10px] text-[var(--content-muted)]">{t(language, 'edgeEditor.natForwardHint')}</p>
                )}
              </div>
            )}
            {/* Editable listen ports (from → to). */}
            <div>
              <label className="text-xs text-[var(--content-muted)]">{t(language, 'edgeEditor.ports')}</label>
              <div className="flex items-center gap-1">
                <input
                  type="number"
                  value={selectedEdge.pinned_from_port ?? ''}
                  onChange={(e) => setPinPort('pinned_from_port', e.target.value)}
                  placeholder={t(language, 'edgeEditor.pinFrom')}
                  className="w-full px-2 py-1 bg-[var(--control)] rounded text-xs border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
                />
                <span className="text-[var(--content-muted)]">→</span>
                <input
                  type="number"
                  value={selectedEdge.pinned_to_port ?? ''}
                  onChange={(e) => setPinPort('pinned_to_port', e.target.value)}
                  placeholder={t(language, 'edgeEditor.pinTo')}
                  className="w-full px-2 py-1 bg-[var(--control)] rounded text-xs border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
                />
              </div>
              {portPairIncomplete && (
                <p className="text-[10px] text-[var(--warning)] mt-0.5">{t(language, 'edgeEditor.pinPairBoth')}</p>
              )}
              {portOutOfRange && (
                <p className="text-[10px] text-[var(--warning)] mt-0.5">
                  {t(language, 'edgeEditor.pinPortRange', { min: MIN_PINNED_PORT })}
                </p>
              )}
            </div>
            {/* Editable transit IPs (from → to), chosen from the edge's transit pool. */}
            <div>
              <label className="text-xs text-[var(--content-muted)]">{t(language, 'edgeEditor.transitIPs')}</label>
              <div className="flex items-center gap-1">
                <input
                  type="text"
                  value={selectedEdge.pinned_from_transit_ip ?? ''}
                  onChange={(e) => setPinTransit('pinned_from_transit_ip', e.target.value)}
                  placeholder={t(language, 'edgeEditor.pinFrom')}
                  className="w-full px-2 py-1 bg-[var(--control)] rounded text-xs border border-[var(--hairline)] focus:border-[var(--accent)] outline-none font-mono"
                />
                <span className="text-[var(--content-muted)]">→</span>
                <input
                  type="text"
                  value={selectedEdge.pinned_to_transit_ip ?? ''}
                  onChange={(e) => setPinTransit('pinned_to_transit_ip', e.target.value)}
                  placeholder={t(language, 'edgeEditor.pinTo')}
                  className="w-full px-2 py-1 bg-[var(--control)] rounded text-xs border border-[var(--hairline)] focus:border-[var(--accent)] outline-none font-mono"
                />
              </div>
              <p className="text-[10px] text-[var(--content-muted)] mt-0.5">
                {t(language, 'edgeEditor.transitPoolPick', { cidr: edgeTransitCidr })}
              </p>
              {transitPairIncomplete && (
                <p className="text-[10px] text-[var(--warning)] mt-0.5">{t(language, 'edgeEditor.pinPairBoth')}</p>
              )}
              {transitOutOfPool && (
                <p className="text-[10px] text-[var(--warning)] mt-0.5">{t(language, 'edgeEditor.transitOutOfPool')}</p>
              )}
            </div>
            {/* Link-locals stay read-only (auto fe80::; manual editing is error-prone). */}
            {hasLinkLocalPin && (
              <p className="text-xs text-[var(--info)] font-mono break-all">
                {t(language, 'edgeEditor.linkLocals')}: {selectedEdge.pinned_from_link_local ?? '—'} → {selectedEdge.pinned_to_link_local ?? '—'}
              </p>
            )}
            <button
              onClick={() =>
                // Unpin: drop every allocation pin so the edge re-allocates fresh on the next
                // compile. Single-sourced via clearedPinFields (lib/normalizeEdges).
                updateEdge(selectedEdge.id, clearedPinFields())
              }
              className="w-full py-1 bg-[var(--danger-solid)] text-[var(--danger-solid-fg)] rounded text-xs"
            >
              {t(language, 'edgeEditor.unpinReallocateOnNext')}
            </button>
          </div>
        )}
        <button
          onClick={() => removeEdge(selectedEdge.id)}
          className="w-full py-1 bg-[var(--danger-solid)] text-[var(--danger-solid-fg)] rounded text-sm"
        >
          {t(language, 'edgeEditor.deleteEdge')}
        </button>
      </div>
    </section>
  );
}
