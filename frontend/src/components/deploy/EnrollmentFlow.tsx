import { useState } from 'react';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';
import { localizeError } from '../../lib/localizeError';

// EnrollmentFlow: the operator picks a node from the topology + a TTL, mints a single-use enrollment
// token, then hands the generated token and the copyable `agent enroll ...` command to the node
// holder to run.
// The token is visible only once (the controller stores only its hash), so it is not echoed again
// after display and is lost on refresh.
export function EnrollmentFlow() {
  const language = useTopologyStore((s) => s.language);
  const topoNodes = useTopologyStore((s) => s.nodes);

  const agentBaseURL = useControllerStore((s) => s.agentBaseURL);
  const settings = useControllerStore((s) => s.settings);
  const mintToken = useControllerStore((s) => s.mintToken);

  const [nodeId, setNodeId] = useState<string>('');
  const [ttlSeconds, setTtlSeconds] = useState<number>(3600);
  const [token, setToken] = useState<string | null>(null);
  const [minting, setMinting] = useState(false);
  const [mintError, setMintError] = useState<string | null>(null);
  // The server's non-blocking warning for "minting a token for a node-id not in the design" (plan-6).
  const [mintWarning, setMintWarning] = useState<string>('');
  const [copied, setCopied] = useState<'' | 'enroll' | 'bootstrap'>('');

  // The agent prefix is reported read-only by the server in GET /settings (YAOG_AGENT_PATH_PREFIX,
  // already normalized to '' or '/<seg>'): the panel no longer makes the operator mirror a second
  // environment variable by hand (plan-1.5, server-authoritative).
  // Note: never use the operator prefix (the pathPrefix mirror) — that belongs to the panel's own API
  // base, which differs once the two are split.
  // When settings is null (not loaded / fetch failed) the prefix is unknown — surface an explicit
  // warning rather than silently generating a command that may 404.
  const agentPrefixKnown = settings !== null;
  const agentPrefix = settings?.agentPathPrefix ?? '';

  // Combine the agent base + prefix: if the operator historically wrote the prefix into the base by
  // hand (older commands did not append the prefix, which was once the only way that worked), don't
  // append it again, avoiding a /s3cr3t/s3cr3t double-prefix 404 after the upgrade.
  const withAgentPrefix = (base: string) => {
    const trimmed = base.replace(/\/+$/, '');
    if (!agentPrefix || trimmed.endsWith(agentPrefix)) return trimmed;
    return `${trimmed}${agentPrefix}`;
  };

  // The enroll command text: the node holder runs it manually on the target host to join the
  // controller (the agent binary must be installed first).
  // --controller is scheme://host[:port] + agent prefix (the agent appends /api/v1/agent/ itself).
  const enrollCommand =
    token && nodeId
      ? `agent enroll --controller ${withAgentPrefix(agentBaseURL)} --node-id ${nodeId} --token ${token}`
      : '';

  // The one-shot bootstrap command (plan-5.2): the node holder runs it once as root to automatically
  // download the agent, enroll, apply, and install the systemd daemon. The curl target is the
  // server-configured public agent URL (falling back to agentBaseURL when unset) + the server-reported
  // agent secret prefix + /api/v1/agent/bootstrap.
  const bootstrapURL = `${withAgentPrefix(settings?.publicAgentURL || agentBaseURL)}/api/v1/agent/bootstrap`;
  const bootstrapCommand =
    token && nodeId
      ? `bash <(curl -fsSL ${bootstrapURL}) --token ${token} --node-id ${nodeId}`
      : '';

  const handleMint = async () => {
    if (!nodeId || ttlSeconds <= 0) return;
    setMinting(true);
    setMintError(null);
    setMintWarning('');
    setToken(null);
    setCopied('');
    try {
      const result = await mintToken(nodeId, ttlSeconds);
      setToken(result.token);
      setMintWarning(result.warning);
    } catch (err) {
      setMintError(localizeError(err, language));
    } finally {
      setMinting(false);
    }
  };

  const copyText = async (text: string, which: 'enroll' | 'bootstrap') => {
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setCopied(which);
    } catch {
      // Clipboard unavailable (non-secure context, etc.): keep the command selectable for manual copy,
      // do not raise an error.
      setCopied('');
    }
  };

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3">
      <h3 className="text-lg font-semibold text-purple-400">
        {t(language, 'enrollmentFlow.nodeEnrollment')}
      </h3>
      <p className="text-sm text-gray-400">
        {t(language, 'enrollmentFlow.mintASingleUse')}
      </p>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-3 items-end">
        <div className="md:col-span-1">
          <label className="text-xs text-gray-400">{t(language, 'enrollmentFlow.node')}</label>
          <select
            value={nodeId}
            onChange={(e) => setNodeId(e.target.value)}
            className="w-full px-2 py-2 bg-gray-600 rounded text-sm border border-gray-500"
          >
            <option value="">{t(language, 'enrollmentFlow.selectANode')}</option>
            {topoNodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.name} ({n.id})
              </option>
            ))}
          </select>
        </div>
        <div className="md:col-span-1">
          <label className="text-xs text-gray-400">{t(language, 'enrollmentFlow.ttlSeconds')}</label>
          <input
            type="number"
            min={1}
            value={ttlSeconds}
            onChange={(e) => setTtlSeconds(parseInt(e.target.value, 10) || 0)}
            className="w-full px-2 py-2 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div className="md:col-span-1">
          <button
            onClick={handleMint}
            disabled={minting || !nodeId || ttlSeconds <= 0}
            className="w-full py-2 bg-purple-600 hover:bg-purple-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {minting
              ? t(language, 'enrollmentFlow.minting')
              : t(language, 'enrollmentFlow.mintToken')}
          </button>
        </div>
      </div>

      {topoNodes.length === 0 && (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {t(language, 'enrollmentFlow.theCurrentTopologyHas')}
        </p>
      )}

      {mintError && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">
          ⚠️ {mintError}
        </p>
      )}

      {/* Design-membership warning (plan-6, warn-not-block): the token was minted and is usable, but
          this node-id is not in the current design and will be skipped at stage time — prompt the
          operator to add it to the design (or confirm it was minted ahead of time). mintWarning is
          only a "warn or not" switch; the copy is rendered on the frontend per language (the server
          string is English and is no longer concatenated verbatim). */}
      {mintWarning && (
        <p className="text-xs text-amber-300 bg-amber-900/20 px-2 py-1 rounded break-all">
          ⚠️{' '}
          {t(language, 'enrollmentFlow.thisNodeIdIs')}
        </p>
      )}

      {token && (
        <div className="space-y-2 p-3 bg-gray-900 border border-gray-700 rounded">
          <p className="text-xs text-yellow-400">
            {t(language, 'enrollmentFlow.thisTokenIsShown')}
          </p>
          {!agentPrefixKnown && (
            <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
              {t(language, 'enrollmentFlow.serverSettingsNotLoaded')}
            </p>
          )}
          <div>
            <label className="text-[10px] text-gray-500 uppercase tracking-wider">
              {t(language, 'enrollmentFlow.enrollmentToken')}
            </label>
            <pre className="text-xs text-cyan-300 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded">
              {token}
            </pre>
          </div>
          {/* Recommended: one-shot bootstrap (auto-installs the agent + enrolls + applies + systemd
              daemon). */}
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-emerald-400 uppercase tracking-wider font-semibold">
                {t(language, 'enrollmentFlow.oneShotInstallRecommended')}
              </label>
              <button
                onClick={() => copyText(bootstrapCommand, 'bootstrap')}
                className="px-3 py-1.5 text-xs bg-emerald-700 hover:bg-emerald-600 rounded text-gray-100"
              >
                {copied === 'bootstrap' ? t(language, 'enrollmentFlow.copied') : t(language, 'enrollmentFlow.copy')}
              </button>
            </div>
            <pre className="text-xs text-emerald-200 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded">
              {bootstrapCommand}
            </pre>
            {!settings?.publicAgentURL && (
              <p className="text-[10px] text-yellow-400 mt-1">
                {t(language, 'enrollmentFlow.tipNoPublicAgent')}
              </p>
            )}
          </div>
          {/* Alternative: manual enroll (when the node already ships with the agent binary). */}
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-gray-500 uppercase tracking-wider">
                {t(language, 'enrollmentFlow.orManualEnrollCommand')}
              </label>
              <button
                onClick={() => copyText(enrollCommand, 'enroll')}
                className="px-3 py-1.5 text-xs bg-gray-700 hover:bg-gray-600 rounded text-gray-200"
              >
                {copied === 'enroll' ? t(language, 'enrollmentFlow.copied_2') : t(language, 'enrollmentFlow.copy_2')}
              </button>
            </div>
            <pre className="text-xs text-gray-300 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded">
              {enrollCommand}
            </pre>
          </div>
        </div>
      )}
    </section>
  );
}
