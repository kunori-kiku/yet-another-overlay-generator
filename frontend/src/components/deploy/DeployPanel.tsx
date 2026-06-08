import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { txt } from '../../i18n';
import { NodeRegistry } from './NodeRegistry';
import { EnrollmentFlow } from './EnrollmentFlow';
import { DeployBar } from './DeployBar';
import { AuditLog } from './AuditLog';

// 部署面板：两种模式。
//   Mode A（本地/手动）：复用 topologyStore 的 compile/exportArtifacts/downloadDeployScript，
//     密钥在浏览器侧生成，操作员手动把产物包/部署脚本拷到目标机执行。
//   Mode B（控制器）：连接表单（绑定 controllerStore）+ 节点注册表 + 注册流程 + 部署条 + 审计日志。
// 这是已有 App 的一个 viewMode 入口（不是新路由），由 TopBar 的「🚀 部署」按钮进入。
type DeployMode = 'local' | 'controller';

export function DeployPanel() {
  const language = useTopologyStore((s) => s.language);
  const nodes = useTopologyStore((s) => s.nodes);
  const compile = useTopologyStore((s) => s.compile);
  const exportArtifacts = useTopologyStore((s) => s.exportArtifacts);
  const downloadDeployScript = useTopologyStore((s) => s.downloadDeployScript);
  const isCompiling = useTopologyStore((s) => s.isCompiling);

  // 控制器连接配置（Mode B）。token 绝不持久化（store 的 partialize 已排除它）。
  const baseURL = useControllerStore((s) => s.baseURL);
  const pathPrefix = useControllerStore((s) => s.pathPrefix);
  const agentBaseURL = useControllerStore((s) => s.agentBaseURL);
  const operatorToken = useControllerStore((s) => s.operatorToken);
  const setConfig = useControllerStore((s) => s.setConfig);
  const refresh = useControllerStore((s) => s.refresh);
  const loading = useControllerStore((s) => s.loading);
  const error = useControllerStore((s) => s.error);
  const lastSyncedAt = useControllerStore((s) => s.lastSyncedAt);
  // 密码登录（plan-5.2）：session 优先于 break-glass operatorToken（见 store.configOf）。
  const login = useControllerStore((s) => s.login);
  const logout = useControllerStore((s) => s.logout);
  const loggedIn = useControllerStore(selectLoggedIn);
  const operatorName = useControllerStore((s) => s.operatorName);
  const sessionExpiresAt = useControllerStore((s) => s.sessionExpiresAt);

  const [mode, setMode] = useState<DeployMode>('local');
  // 登录表单的本地输入（密码只在内存里，登录成功后清空）。
  const [loginUser, setLoginUser] = useState('');
  const [loginPass, setLoginPass] = useState('');

  const noNodes = nodes.length === 0;

  return (
    <div className="h-full flex flex-col p-6 space-y-6 overflow-y-auto">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-bold text-white">
          {txt(language, '🚀 部署', '🚀 Deploy')}
        </h2>
        {/* Mode A/B 切换 */}
        <div className="flex items-center bg-gray-700 rounded border border-gray-600 overflow-hidden">
          <button
            onClick={() => setMode('local')}
            className={`px-3 py-1.5 text-sm ${
              mode === 'local' ? 'bg-blue-600 text-white' : 'text-gray-300 hover:bg-gray-600'
            }`}
          >
            {txt(language, '本地 / 手动', 'Local / Manual')}
          </button>
          <button
            onClick={() => setMode('controller')}
            className={`px-3 py-1.5 text-sm ${
              mode === 'controller' ? 'bg-blue-600 text-white' : 'text-gray-300 hover:bg-gray-600'
            }`}
          >
            {txt(language, '控制器', 'Controller')}
          </button>
        </div>
      </div>

      {mode === 'local' ? (
        <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
          <h3 className="text-lg font-semibold text-blue-400">
            {txt(language, '本地 / 手动部署', 'Local / Manual Deploy')}
          </h3>
          <p className="text-sm text-gray-400">
            {txt(
              language,
              '在浏览器内生成密钥与配置，下载安装产物包或部署脚本，然后手动在目标主机上执行。',
              'Keys and configs are generated in your browser. Download the install bundle or deploy script, then run it on each target host manually.',
            )}
          </p>
          {noNodes && (
            <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
              {txt(
                language,
                '当前拓扑没有节点，请先在「编辑拓扑」中添加节点。',
                'The current topology has no nodes. Add nodes in Edit Topology first.',
              )}
            </p>
          )}
          <div className="space-y-2">
            <button
              onClick={() => compile()}
              disabled={isCompiling || noNodes}
              className="w-full py-1.5 bg-green-600 hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
            >
              {isCompiling
                ? txt(language, '编译中...', 'Compiling...')
                : txt(language, '🔨 编译', '🔨 Compile')}
            </button>
            <button
              onClick={() => exportArtifacts()}
              disabled={noNodes}
              className="w-full py-1.5 bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
            >
              {txt(language, '📦 导出产物包', '📦 Export Artifacts')}
            </button>
            <div className="flex gap-2">
              <button
                onClick={() => downloadDeployScript('sh')}
                disabled={noNodes}
                className="flex-1 py-1.5 bg-orange-600 hover:bg-orange-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
              >
                {txt(language, '🚀 部署脚本 .sh', '🚀 Deploy .sh')}
              </button>
              <button
                onClick={() => downloadDeployScript('ps1')}
                disabled={noNodes}
                className="flex-1 py-1.5 bg-orange-600 hover:bg-orange-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-sm"
              >
                {txt(language, '🚀 部署脚本 .ps1', '🚀 Deploy .ps1')}
              </button>
            </div>
          </div>
        </section>
      ) : (
        <div className="space-y-6">
          {/* 连接表单 */}
          <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
            <div className="flex items-center justify-between">
              <h3 className="text-lg font-semibold text-teal-400">
                {txt(language, '控制器连接', 'Controller Connection')}
              </h3>
              <button
                onClick={() => refresh()}
                disabled={loading}
                className="px-3 py-1.5 text-sm bg-teal-700 hover:bg-teal-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white"
              >
                {loading
                  ? txt(language, '同步中...', 'Syncing...')
                  : txt(language, '🔄 刷新', '🔄 Refresh')}
              </button>
            </div>
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
              {/* 密码登录（plan-5.2）：日常鉴权路径。已登录显示身份 + 登出；未登录显示
                  用户名/密码 + 登录。下方的 Operator Token 是可选的 break-glass 恢复凭据。 */}
              <div className="border-t border-gray-600 pt-3 space-y-2">
                <label className="text-xs text-gray-400 font-medium">
                  {txt(language, '登录', 'Sign in')}
                </label>
                {loggedIn ? (
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-xs text-green-300">
                      {txt(language, '已登录为', 'Signed in as')}{' '}
                      <span className="font-mono">{operatorName ?? ''}</span>
                      {sessionExpiresAt && (
                        <span className="text-gray-400">
                          {' '}
                          {txt(language, '（到期', '(until')}{' '}
                          {new Date(sessionExpiresAt).toLocaleString()}
                          {txt(language, '）', ')')}
                        </span>
                      )}
                    </span>
                    <button
                      onClick={() => logout()}
                      disabled={loading}
                      className="px-3 py-1 text-xs bg-gray-600 hover:bg-gray-500 disabled:bg-gray-700 rounded text-white"
                    >
                      {txt(language, '登出', 'Sign out')}
                    </button>
                  </div>
                ) : (
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      void login(loginUser, loginPass).then(() => {
                        // 登录成功后清空密码输入（不在 React 状态里多留）。
                        if (useControllerStore.getState().sessionToken) {
                          setLoginPass('');
                        }
                      });
                    }}
                    className="space-y-2"
                  >
                    <input
                      type="text"
                      value={loginUser}
                      onChange={(e) => setLoginUser(e.target.value)}
                      placeholder={txt(language, '用户名', 'Username')}
                      autoComplete="username"
                      className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                    />
                    <input
                      type="password"
                      value={loginPass}
                      onChange={(e) => setLoginPass(e.target.value)}
                      placeholder={txt(language, '密码', 'Password')}
                      autoComplete="current-password"
                      className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                    />
                    <button
                      type="submit"
                      disabled={loading || loginUser.trim() === '' || loginPass === ''}
                      className="px-4 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
                    >
                      {loading
                        ? txt(language, '登录中...', 'Signing in...')
                        : txt(language, '登录', 'Sign in')}
                    </button>
                  </form>
                )}
              </div>
              <div className="border-t border-gray-600 pt-3">
                <label className="text-xs text-gray-400">
                  {txt(language, 'Operator Token（恢复用 / 可选）', 'Operator token (break-glass, optional)')}
                </label>
                <input
                  type="password"
                  value={operatorToken}
                  onChange={(e) => setConfig({ operatorToken: e.target.value })}
                  placeholder={txt(language, '可选；不会被持久化', 'Optional; never persisted')}
                  autoComplete="off"
                  className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                />
              </div>
            </div>
            <p className="text-[10px] text-gray-500">
              {txt(
                language,
                '日常请用密码登录。Operator Token 是可选的 break-glass 恢复凭据（仅当后端设置了 YAOG_CONTROLLER_OPERATOR_TOKEN 时可用）。session 与 token 都仅存内存（刷新后需重新登录/输入），其余连接端点会持久化。',
                'Use password sign-in for day-to-day. The operator token is an optional break-glass credential (only when the backend sets YAOG_CONTROLLER_OPERATOR_TOKEN). Both the session and the token are kept in memory only (re-enter after a page refresh); the other endpoints are persisted.',
              )}
            </p>
            {error && (
              <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">
                ⚠️ {error}
              </p>
            )}
            {lastSyncedAt !== null && (
              <p className="text-[10px] text-gray-500">
                {txt(language, '上次同步', 'Last synced')}: {new Date(lastSyncedAt).toLocaleString()}
              </p>
            )}
          </section>

          <NodeRegistry />
          <EnrollmentFlow />
          <DeployBar />
          <AuditLog />
        </div>
      )}
    </div>
  );
}
