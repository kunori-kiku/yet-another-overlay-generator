import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { t } from '../../i18n';

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
        {t(language, 'connectionSettings.controllerConnection')}
      </h3>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
        <div>
          <label className="text-xs text-gray-400">
            {t(language, 'connectionSettings.operatorBaseURL')}
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
            {t(language, 'connectionSettings.secretPathPrefixOptional')}
          </label>
          <input
            type="text"
            value={pathPrefix}
            onChange={(e) => setConfig({ pathPrefix: e.target.value })}
            placeholder="/s3cr3t"
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          />
          <p className="text-[10px] text-gray-500 mt-0.5">
            {t(language, 'connectionSettings.mustMatchTheServer')}
          </p>
        </div>
        <div>
          <label className="text-xs text-gray-400">
            {t(language, 'connectionSettings.agentBaseURL')}
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
        {t(language, 'connectionSettings.signInHappensOn')}
      </p>
      {/* Refresh as a bottom submit-style action — gives the connection form a
          clear "submit" affordance, connecting/syncing the panel with the backend. */}
      <button
        onClick={() => refresh()}
        disabled={loading}
        className="w-full py-2 text-sm font-medium bg-teal-700 hover:bg-teal-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white"
      >
        {loading
          ? t(language, 'connectionSettings.syncing')
          : t(language, 'connectRefresh')}
      </button>
      {error && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">⚠️ {error}</p>
      )}
      {lastSyncedAt !== null && (
        <p className="text-[10px] text-gray-500">
          {t(language, 'connectionSettings.lastSynced')}: {new Date(lastSyncedAt).toLocaleString()}
        </p>
      )}
    </section>
  );
}
