import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { deriveCapabilitiesFromRole, type NodeRole } from '../../lib/roleCapabilities';
import { uuid } from '../../lib/uuid';
import type { Node } from '../../types/topology';

const DEFAULT_LISTEN_PORT = 51820;

// UX-5: parse the top-level "public address" input (of the form IP:port or domain:port) into
// host + port.
// - Only when the string contains exactly one colon and the part after it is all digits is it
//   split out as a :port suffix; this supports 203.0.113.10:51820 / example.com:51820 while not
//   mis-handling a bare IPv6 address (multiple colons).
// - On a missing/invalid port it falls back to 51820.
// - When the input is empty (after trimming) it returns null, meaning the node is behind NAT
//   (no public_endpoints written).
function parsePublicAddress(
  raw: string
): { host: string; port: number } | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;

  const colonCount = (trimmed.match(/:/g) || []).length;
  if (colonCount === 1) {
    const [host, portStr] = trimmed.split(':');
    if (host && /^\d+$/.test(portStr)) {
      const port = parseInt(portStr, 10);
      if (port > 0 && port <= 65535) {
        return { host, port };
      }
    }
  }
  // No recognizable port suffix (bare host / IPv6 / invalid port): the whole string is the
  // host, and the port takes the default.
  return { host: trimmed, port: DEFAULT_LISTEN_PORT };
}

export function NodeForm() {
  const { addNode, domains, language } = useTopologyStore();
  // fixed_private_key ("Pin private key") is a LOCAL/air-gap custody primitive — meaningless in
  // zero-knowledge controller mode (the agent holds the key). Gate the create-form control too,
  // mirroring the NodeEditor gate (plan-11 / T4 review): without this the create form is a
  // parallel reachable path that defeats the edit-form gate.
  const mode = useControllerStore((s) => s.mode);
  const [isOpen, setIsOpen] = useState(false);
  const [name, setName] = useState('');
  const [role, setRole] = useState<NodeRole>('peer');
  const [domainId, setDomainId] = useState('');
  const [hostname, setHostname] = useState('');
  // UX-5: the top-level "public address" input (the primary entry point). When non-empty it
  // derives has_public_ip=true and generates public_endpoints[0].
  const [publicAddress, setPublicAddress] = useState('');
  // The checkbox is now demoted to an "advanced" path that reveals the multi-endpoint (multiple
  // public mappings) editor; it is no longer the sole switch for public reachability.
  const [hasPublicIP, setHasPublicIP] = useState(false);
  const [mtu, setMtu] = useState(0);
  const [canForward, setCanForward] = useState(false);
  const [fixedPrivateKey, setFixedPrivateKey] = useState(false);
  const [endpointHost, setEndpointHost] = useState('');
  const [endpointPort, setEndpointPort] = useState(51820);
  const [error, setError] = useState('');

  const handleSubmit = () => {
    if (!name.trim()) {
      setError(t(language, 'nodeForm.nameIsRequired'));
      return;
    }
    const targetDomain = domainId || (domains.length > 0 ? domains[0].id : '');
    if (!targetDomain) {
      setError(t(language, 'nodeForm.pleaseCreateADomain'));
      return;
    }

    const id = `node-${uuid()}`;

    // UX-5: the top-level "public address" is the primary entry point for public reachability.
    // The client role is never reachable; any other role is treated as having a public IP as
    // long as the top-level address is non-empty or the advanced checkbox is ticked.
    const parsedPublic = role !== 'client' ? parsePublicAddress(publicAddress) : null;
    const effectiveHasPublicIP = role !== 'client' && (parsedPublic !== null || hasPublicIP);

    const capabilities = deriveCapabilitiesFromRole(role, effectiveHasPublicIP);
    // Preserve the operator's explicitly ticked "can forward" (matching the backend's behavior
    // of preserving an explicitly set true); the client role is not allowed to forward, so it is
    // not applied.
    if (canForward && role !== 'client') {
      capabilities.can_forward = true;
    }

    // Assemble public_endpoints: the top-level address (if any) becomes public_endpoints[0];
    // the advanced section (revealed by the checkbox) acts as an additional multi-endpoint editor
    // — appended only when the host is non-empty and does not duplicate the top-level address.
    const publicEndpoints: NonNullable<Node['public_endpoints']> = [];
    if (parsedPublic) {
      publicEndpoints.push({
        id: `${id}-ep-${uuid()}`,
        host: parsedPublic.host,
        port: parsedPublic.port,
      });
    }
    if (role !== 'client' && hasPublicIP && endpointHost.trim()) {
      const advHost = endpointHost.trim();
      const advPort = endpointPort || DEFAULT_LISTEN_PORT;
      const duplicate = publicEndpoints.some(
        (ep) => ep.host === advHost && ep.port === advPort
      );
      if (!duplicate) {
        publicEndpoints.push({
          id: `${id}-ep-${uuid()}`,
          host: advHost,
          port: advPort,
        });
      }
    }

    addNode({
      id,
      name: name.trim(),
      hostname: hostname.trim() || undefined,
      role,
      domain_id: targetDomain,
      mtu: mtu > 0 ? mtu : undefined,
      capabilities,
      // Defense-in-depth: never write the local-only pin flag from controller mode, even if the
      // checkbox state somehow lingered (it is hidden there).
      fixed_private_key: mode === 'local' ? fixedPrivateKey : false,
      public_endpoints: publicEndpoints,
    });

    setName('');
    setHostname('');
    setFixedPrivateKey(false);
    setPublicAddress('');
    setHasPublicIP(false);
    setEndpointHost('');
    setEndpointPort(51820);
    setError('');
    setIsOpen(false);
  };

  if (!isOpen) {
    return (
      <button
        onClick={() => setIsOpen(true)}
        className="w-full py-1.5 px-3 bg-[var(--accent)] hover:bg-[var(--accent-hover)] text-[var(--accent-fg)] rounded text-sm mb-2"
      >
        + {t(language, 'nodeForm.addNode')}
      </button>
    );
  }

  return (
    <div className="p-2 bg-[var(--surface-elevated)] rounded space-y-2 mb-2">
      <input
        type="text"
        placeholder={t(language, 'nodeForm.nodeName')}
        value={name}
        onChange={(e) => setName(e.target.value)}
        className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
      />
      {/* UX-5: the public-address primary entry point. When non-empty it derives has_public_ip.
          The client role has no such concept, so it is hidden. */}
      {role !== 'client' && (
        <div className="space-y-1">
          <label className="block text-xs text-[var(--content)]">
            {t(language, 'publicAddressLabel')}
          </label>
          <input
            type="text"
            placeholder={t(language, 'publicAddressPlaceholder')}
            value={publicAddress}
            onChange={(e) => setPublicAddress(e.target.value)}
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
          />
          {!publicAddress.trim() && (
            <p className="text-xs text-[var(--content-muted)]">
              {t(language, 'publicAddressHint')}
            </p>
          )}
        </div>
      )}
      <input
        type="text"
        placeholder={t(language, 'nodeForm.hostnameOptional')}
        value={hostname}
        onChange={(e) => setHostname(e.target.value)}
        className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
      />
      <select
        value={domainId || (domains.length > 0 ? domains[0].id : '')}
        onChange={(e) => setDomainId(e.target.value)}
        className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
      >
        {domains.map((d) => (
          <option key={d.id} value={d.id}>
            {d.name} ({d.cidr})
          </option>
        ))}
      </select>
      <select
        value={role}
        onChange={(e) => setRole(e.target.value as NodeRole)}
        className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
      >
        <option value="peer">Peer</option>
        <option value="router">Router</option>
        <option value="relay">Relay</option>
        <option value="gateway">Gateway</option>
        <option value="client">Client</option>
      </select>
      <input
        type="number"
        min={576}
        max={65535}
        placeholder={t(language, 'nodeForm.mtuLeaveEmptyFor')}
        value={mtu || ''}
        onChange={(e) => setMtu(parseInt(e.target.value) || 0)}
        className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
      />
      {/* UX-5: the checkbox is demoted to an advanced path that reveals the multi-endpoint
          (multiple public mappings) editor. Public reachability itself is already derived from
          the "public address" input above, so this is no longer the sole switch. The client role
          has no public-endpoint concept, so it is hidden. */}
      {role !== 'client' && (
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={hasPublicIP}
            onChange={(e) => setHasPublicIP(e.target.checked)}
            className="rounded"
          />
          {t(language, 'nodeForm.advancedAddMorePublic')}
        </label>
      )}
      {mode === 'local' && (
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={fixedPrivateKey}
            onChange={(e) => setFixedPrivateKey(e.target.checked)}
            className="rounded"
          />
          {t(language, 'nodeForm.pinPrivateKeyPersist')}
        </label>
      )}
      {hasPublicIP && (
        <div className="space-y-2 p-2 bg-[var(--surface-sunken)] rounded border border-[var(--hairline)]">
          <p className="text-xs text-[var(--content)]">{t(language, 'nodeForm.additionalPublicEndpointMapping')}</p>
          <input
            type="text"
            placeholder={t(language, 'nodeForm.publicIPOrDomain')}
            value={endpointHost}
            onChange={(e) => setEndpointHost(e.target.value)}
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
          />
          <input
            type="number"
            placeholder={t(language, 'nodeForm.port')}
            value={endpointPort}
            onChange={(e) => setEndpointPort(parseInt(e.target.value, 10) || 51820)}
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
          />
        </div>
      )}
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={canForward}
          onChange={(e) => setCanForward(e.target.checked)}
          className="rounded"
        />
        {t(language, 'nodeForm.canForwardTraffic')}
      </label>
      {error && <p className="text-xs text-[var(--danger)]">{error}</p>}
      <div className="flex gap-2">
        <button
          onClick={handleSubmit}
          className="flex-1 py-1 bg-[var(--success-solid)] hover:bg-[var(--success-solid)] text-[var(--success-solid-fg)] rounded text-sm"
        >
          {t(language, 'nodeForm.confirm')}
        </button>
        <button
          onClick={() => { setIsOpen(false); setError(''); }}
          className="flex-1 py-1 bg-[var(--control)] hover:bg-[var(--control-hover)] rounded text-sm"
        >
          {t(language, 'nodeForm.cancel')}
        </button>
      </div>
    </div>
  );
}
