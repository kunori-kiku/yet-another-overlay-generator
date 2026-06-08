import { useControllerStore } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt } from '../../i18n';

// 部署条：把当前拓扑发布到 fleet。controllerStore.deploy() 串联
// update-topology → (step-up seam) → stage → promote → refresh，整套 promote-to-fleet。
// 这里只负责触发与回显结果（staged / skippedUnenrolled / generation）+ 错误。
// requireUserKey 的 step-up（Plan-5 签名钩子）在 store 内部、stage/promote 之前。
export function DeployBar() {
  const language = useTopologyStore((s) => s.language);

  const deploy = useControllerStore((s) => s.deploy);
  const loading = useControllerStore((s) => s.loading);
  const error = useControllerStore((s) => s.error);
  const lastDeploy = useControllerStore((s) => s.lastDeploy);
  const operatorToken = useControllerStore((s) => s.operatorToken);

  // 未填 operator token 时无法发起 operator 请求，禁用按钮并给出提示。
  const noToken = operatorToken.trim() === '';

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold text-teal-400">
          {txt(language, '发布到 Fleet', 'Deploy to Fleet')}
        </h3>
        <button
          onClick={() => deploy()}
          disabled={loading || noToken}
          className="px-4 py-1.5 text-sm bg-teal-600 hover:bg-teal-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
        >
          {loading
            ? txt(language, '发布中...', 'Deploying...')
            : txt(language, '🚀 发布', '🚀 Deploy')}
        </button>
      </div>

      <p className="text-sm text-gray-400">
        {txt(
          language,
          '将当前拓扑上传到控制器，编译已注册节点的子图，并提升为新一代配置（已注册节点会自动拉取）。',
          'Upload the current topology, compile the enrolled subgraph, and promote it as a new generation (enrolled nodes pull automatically).',
        )}
      </p>

      {noToken && (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {txt(
            language,
            '请先在上方填写 Operator Token。',
            'Enter the operator token above first.',
          )}
        </p>
      )}

      {error && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">
          ⚠️ {error}
        </p>
      )}

      {lastDeploy && (
        <div className="p-3 bg-gray-900 border border-gray-700 rounded space-y-2 text-sm">
          <p className="text-gray-300">
            {txt(language, '最近一次发布', 'Last deploy')} —{' '}
            <span className="font-mono text-cyan-300">
              {txt(language, '代号', 'generation')} {lastDeploy.generation}
            </span>
          </p>
          <div>
            <p className="text-xs text-gray-400">
              {txt(language, '已编译节点', 'Staged nodes')} ({lastDeploy.staged.length})
            </p>
            {lastDeploy.staged.length === 0 ? (
              <p className="text-xs text-gray-500 italic">{txt(language, '（无）', '(none)')}</p>
            ) : (
              <p className="text-xs text-green-300 font-mono break-all">
                {lastDeploy.staged.join(', ')}
              </p>
            )}
          </div>
          {lastDeploy.skippedUnenrolled.length > 0 && (
            <div>
              <p className="text-xs text-gray-400">
                {txt(language, '因未注册被跳过', 'Skipped (unenrolled)')} (
                {lastDeploy.skippedUnenrolled.length})
              </p>
              <p className="text-xs text-yellow-300 font-mono break-all">
                {lastDeploy.skippedUnenrolled.join(', ')}
              </p>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
