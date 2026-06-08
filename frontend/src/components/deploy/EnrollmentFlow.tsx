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
  const mintToken = useControllerStore((s) => s.mintToken);

  const [nodeId, setNodeId] = useState<string>('');
  const [ttlSeconds, setTtlSeconds] = useState<number>(3600);
  const [token, setToken] = useState<string | null>(null);
  const [minting, setMinting] = useState(false);
  const [mintError, setMintError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  // enroll 命令文案：节点持有者在目标机上执行它来加入控制器。
  const enrollCommand =
    token && nodeId
      ? `agent enroll --controller ${agentBaseURL} --node-id ${nodeId} --token ${token}`
      : '';

  const handleMint = async () => {
    if (!nodeId || ttlSeconds <= 0) return;
    setMinting(true);
    setMintError(null);
    setToken(null);
    setCopied(false);
    try {
      const tok = await mintToken(nodeId, ttlSeconds);
      setToken(tok);
    } catch (err) {
      setMintError(err instanceof Error ? err.message : 'Failed to mint enrollment token');
    } finally {
      setMinting(false);
    }
  };

  const handleCopy = async () => {
    if (!enrollCommand) return;
    try {
      await navigator.clipboard.writeText(enrollCommand);
      setCopied(true);
    } catch {
      // 剪贴板不可用（非安全上下文等）：保持命令可手动选中复制，不报错。
      setCopied(false);
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
          <div>
            <label className="text-[10px] text-gray-500 uppercase tracking-wider">
              {txt(language, '注册令牌', 'Enrollment token')}
            </label>
            <pre className="text-xs text-cyan-300 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded">
              {token}
            </pre>
          </div>
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-gray-500 uppercase tracking-wider">
                {txt(language, '注册命令', 'Enroll command')}
              </label>
              <button
                onClick={handleCopy}
                className="px-2 py-0.5 text-xs bg-gray-700 hover:bg-gray-600 rounded text-gray-200"
              >
                {copied ? txt(language, '已复制', 'Copied') : txt(language, '复制', 'Copy')}
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
