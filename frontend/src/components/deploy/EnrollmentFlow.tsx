import { useState } from 'react';
import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

// 注册流程：操作员从拓扑里选一个节点 + 一个 TTL，铸造一次性 enrollment token，
// 然后把生成的 token 与可复制的 `agent enroll ...` 命令交给节点持有者执行。
// token 仅此一次可见（控制器只存其哈希），因此展示后不再回显，刷新即丢失。
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
  const [copied, setCopied] = useState<'' | 'enroll' | 'bootstrap'>('');

  // Agent 前缀是服务端在 GET /settings 里只读上报的（YAOG_AGENT_PATH_PREFIX，已归一化为
  // '' 或 '/<seg>'）：面板不再让操作员手工镜像第二个环境变量（plan-1.5，server-authoritative）。
  // 注意绝不能用操作员前缀（pathPrefix mirror）——那属于面板自己的 API base，两者拆分后不同。
  // settings 为 null（未加载/拉取失败）时前缀未知——此时给出显式警告而非静默生成可能 404 的命令。
  const agentPrefixKnown = settings !== null;
  const agentPrefix = settings?.agentPathPrefix ?? '';

  // 组合 agent 基址 + 前缀：若操作员历史上已把前缀手工写进了基址（旧版命令不补前缀，
  // 这曾是唯一能用的写法），不再二次追加，避免升级后出现 /s3cr3t/s3cr3t 双前缀 404。
  const withAgentPrefix = (base: string) => {
    const trimmed = base.replace(/\/+$/, '');
    if (!agentPrefix || trimmed.endsWith(agentPrefix)) return trimmed;
    return `${trimmed}${agentPrefix}`;
  };

  // enroll 命令文案：节点持有者在目标机上手动执行它来加入控制器（需先装好 agent 二进制）。
  // --controller 是 scheme://host[:port] + agent 前缀（agent 自己补 /api/v1/controller/）。
  const enrollCommand =
    token && nodeId
      ? `agent enroll --controller ${withAgentPrefix(agentBaseURL)} --node-id ${nodeId} --token ${token}`
      : '';

  // 一键 bootstrap 命令（plan-5.2）：节点持有者以 root 跑一次，自动下载 agent、入网、应用、
  // 并装上 systemd 守护进程。curl 目标是服务端配置的 public agent URL（未配置则回退到
  // agentBaseURL）+ 服务端上报的 agent secret 前缀 + /api/v1/controller/bootstrap。
  const bootstrapURL = `${withAgentPrefix(settings?.publicAgentURL || agentBaseURL)}/api/v1/controller/bootstrap`;
  const bootstrapCommand =
    token && nodeId
      ? `bash <(curl -fsSL ${bootstrapURL}) --token ${token} --node-id ${nodeId}`
      : '';

  const handleMint = async () => {
    if (!nodeId || ttlSeconds <= 0) return;
    setMinting(true);
    setMintError(null);
    setToken(null);
    setCopied('');
    try {
      const tok = await mintToken(nodeId, ttlSeconds);
      setToken(tok);
    } catch (err) {
      setMintError(err instanceof Error ? err.message : 'Failed to mint enrollment token');
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
      // 剪贴板不可用（非安全上下文等）：保持命令可手动选中复制，不报错。
      setCopied('');
    }
  };

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3">
      <h3 className="text-lg font-semibold text-purple-400">
        {txt(language, '节点注册', 'Node Enrollment')}
      </h3>
      <p className="text-sm text-gray-400">
        {txt(
          language,
          '为某个拓扑节点签发一次性注册令牌，并把下方命令交给该节点的持有者执行以加入控制器。',
          'Mint a single-use token for a topology node, then hand the command below to that node’s operator to join the controller.',
        )}
      </p>

      <div className="grid grid-cols-1 md:grid-cols-3 gap-3 items-end">
        <div className="md:col-span-1">
          <label className="text-xs text-gray-400">{txt(language, '节点', 'Node')}</label>
          <select
            value={nodeId}
            onChange={(e) => setNodeId(e.target.value)}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500"
          >
            <option value="">{txt(language, '选择节点...', 'Select a node...')}</option>
            {topoNodes.map((n) => (
              <option key={n.id} value={n.id}>
                {n.name} ({n.id})
              </option>
            ))}
          </select>
        </div>
        <div className="md:col-span-1">
          <label className="text-xs text-gray-400">{txt(language, 'TTL (秒)', 'TTL (seconds)')}</label>
          <input
            type="number"
            min={1}
            value={ttlSeconds}
            onChange={(e) => setTtlSeconds(parseInt(e.target.value, 10) || 0)}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div className="md:col-span-1">
          <button
            onClick={handleMint}
            disabled={minting || !nodeId || ttlSeconds <= 0}
            className="w-full py-1.5 bg-purple-600 hover:bg-purple-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
          >
            {minting
              ? txt(language, '签发中...', 'Minting...')
              : txt(language, '🔑 签发令牌', '🔑 Mint Token')}
          </button>
        </div>
      </div>

      {topoNodes.length === 0 && (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {txt(
            language,
            '当前拓扑没有节点，请先在「编辑拓扑」中添加节点。',
            'The current topology has no nodes. Add nodes in Edit Topology first.',
          )}
        </p>
      )}

      {mintError && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">
          ⚠️ {mintError}
        </p>
      )}

      {token && (
        <div className="space-y-2 p-3 bg-gray-900 border border-gray-700 rounded">
          <p className="text-xs text-yellow-400">
            {txt(
              language,
              '⚠️ 令牌仅此一次可见，请立即复制保存。',
              '⚠️ This token is shown only once — copy it now.',
            )}
          </p>
          {!agentPrefixKnown && (
            <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
              {txt(
                language,
                '⚠️ 服务端设置尚未加载：以下命令可能缺少 agent 路径前缀。请等待设置加载或刷新后再复制。',
                '⚠️ Server settings not loaded yet: the commands below may be missing the agent path prefix. Wait for settings to load (or refresh) before copying.',
              )}
            </p>
          )}
          <div>
            <label className="text-[10px] text-gray-500 uppercase tracking-wider">
              {txt(language, '注册令牌', 'Enrollment token')}
            </label>
            <pre className="text-xs text-cyan-300 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded">
              {token}
            </pre>
          </div>
          {/* 推荐：一键 bootstrap（自动装 agent + 入网 + 应用 + systemd 守护）。 */}
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-emerald-400 uppercase tracking-wider font-semibold">
                {txt(language, '一键安装（推荐，以 root 运行）', 'One-shot install (recommended, run as root)')}
              </label>
              <button
                onClick={() => copyText(bootstrapCommand, 'bootstrap')}
                className="px-2 py-0.5 text-xs bg-emerald-700 hover:bg-emerald-600 rounded text-gray-100"
              >
                {copied === 'bootstrap' ? txt(language, '已复制', 'Copied') : txt(language, '复制', 'Copy')}
              </button>
            </div>
            <pre className="text-xs text-emerald-200 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded">
              {bootstrapCommand}
            </pre>
            {!settings?.publicAgentURL && (
              <p className="text-[10px] text-yellow-400 mt-1">
                {txt(
                  language,
                  '提示：未配置「公开 Agent 地址」，已回退到上方 Agent 基础地址。请在「Bootstrap 设置」里设置节点可达的公开地址。',
                  'Tip: no public agent URL configured — falling back to the Agent Base URL above. Set a node-reachable public URL in Bootstrap Settings.',
                )}
              </p>
            )}
          </div>
          {/* 备选：手动 enroll（节点已自带 agent 二进制时）。 */}
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-gray-500 uppercase tracking-wider">
                {txt(language, '或：手动 enroll 命令', 'Or: manual enroll command')}
              </label>
              <button
                onClick={() => copyText(enrollCommand, 'enroll')}
                className="px-2 py-0.5 text-xs bg-gray-700 hover:bg-gray-600 rounded text-gray-200"
              >
                {copied === 'enroll' ? txt(language, '已复制', 'Copied') : txt(language, '复制', 'Copy')}
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
