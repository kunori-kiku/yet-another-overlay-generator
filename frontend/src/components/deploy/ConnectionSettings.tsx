import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { txt, STRINGS } from '../../i18n';

// 控制器连接设置（/settings 的 Connection 区块）。plan-4 起登录/身份/break-glass 都
// 移去了全屏 LoginPage（D2）与 UserMenu（登出）；这里只保留连接端点（持久化）与
// 「连接 / 刷新」动作。
export function ConnectionSettings() {
  const language = useTopologyStore((s) => s.language);

  const baseURL = useControllerStore((s) => s.baseURL);
  const pathPrefix = useControllerStore((s) => s.pathPrefix);
  const agentBaseURL = useControllerStore((s) => s.agentBaseURL);
  const setConfig = useControllerStore((s) => s.setConfig);
  const refresh = useControllerStore((s) => s.refresh);
  const loading = useControllerStore((s) => s.loading);
  const error = useControllerStore((s) => s.error);
  const lastSyncedAt = useControllerStore((s) => s.lastSyncedAt);

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-teal-400">
        {txt(language, '控制器连接', 'Controller Connection')}
      </h3>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div>
          <label className="text-xs text-gray-400">
            {txt(language, 'Operator 基础地址', 'Operator Base URL')}
          </label>
          <input
            type="text"
            value={baseURL}
            onChange={(e) => setConfig({ baseURL: e.target.value })}
            placeholder="http://localhost:8080"
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-gray-400">
            {txt(language, 'Secret 路径前缀（可选）', 'Secret Path Prefix (optional)')}
          </label>
          <input
            type="text"
            value={pathPrefix}
            onChange={(e) => setConfig({ pathPrefix: e.target.value })}
            placeholder="/s3cr3t"
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
          <p className="text-[10px] text-gray-500 mt-0.5">
            {txt(
              language,
              '需与服务端部署变量 YAOG_OPERATOR_PATH_PREFIX 一致：此处不设置任何东西，只告诉面板操作员 API 的位置。服务端未设置则留空。',
              "Must match the server's YAOG_OPERATOR_PATH_PREFIX (set at deploy time). This sets nothing — it only tells the panel where the operator API is. Leave blank if the server has none.",
            )}
          </p>
        </div>
        <div>
          <label className="text-xs text-gray-400">
            {txt(language, 'Agent 基础地址', 'Agent Base URL')}
          </label>
          <input
            type="text"
            value={agentBaseURL}
            onChange={(e) => setConfig({ agentBaseURL: e.target.value })}
            placeholder="http://localhost:9090"
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
        </div>
      </div>
      <p className="text-[10px] text-gray-500">
        {txt(
          language,
          '登录在进入面板时的全屏登录页完成；登出在右上角的账户菜单。连接端点会持久化。',
          'Sign-in happens on the full-page login screen when entering the panel; sign-out lives in the top-right account menu. Connection endpoints are persisted.',
        )}
      </p>
      {/* Refresh as a bottom submit-style action — gives the connection form a
          clear "submit" affordance, connecting/syncing the panel with the backend. */}
      <button
        onClick={() => refresh()}
        disabled={loading}
        className="w-full py-2 text-sm font-medium bg-teal-700 hover:bg-teal-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white"
      >
        {loading
          ? txt(language, '同步中...', 'Syncing...')
          : txt(language, ...STRINGS.connectRefresh)}
      </button>
      {error && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">⚠️ {error}</p>
      )}
      {lastSyncedAt !== null && (
        <p className="text-[10px] text-gray-500">
          {txt(language, '上次同步', 'Last synced')}: {new Date(lastSyncedAt).toLocaleString()}
        </p>
      )}
    </section>
  );
}
