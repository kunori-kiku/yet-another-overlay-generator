import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { txt, STRINGS } from '../../i18n';

// 控制器连接 + 登录设置（从原 DeployPanel 的控制器连接 <section> 原样抽出，作为
// /settings 路由的 Connection 区块）。token / session / 验证码只在内存中，绝不持久化。
export function ConnectionSettings() {
  const language = useTopologyStore((s) => s.language);

  const baseURL = useControllerStore((s) => s.baseURL);
  const pathPrefix = useControllerStore((s) => s.pathPrefix);
  const agentBaseURL = useControllerStore((s) => s.agentBaseURL);
  const operatorToken = useControllerStore((s) => s.operatorToken);
  const setConfig = useControllerStore((s) => s.setConfig);
  const refresh = useControllerStore((s) => s.refresh);
  const loading = useControllerStore((s) => s.loading);
  const error = useControllerStore((s) => s.error);
  const lastSyncedAt = useControllerStore((s) => s.lastSyncedAt);
  const login = useControllerStore((s) => s.login);
  const logout = useControllerStore((s) => s.logout);
  const loggedIn = useControllerStore(selectLoggedIn);
  const operatorName = useControllerStore((s) => s.operatorName);
  const sessionExpiresAt = useControllerStore((s) => s.sessionExpiresAt);
  const totpRequired = useControllerStore((s) => s.totpRequired);
  const resetTOTPChallenge = useControllerStore((s) => s.resetTOTPChallenge);
  const loginWithPasskey = useControllerStore((s) => s.loginWithPasskey);
  const loginCeremony = useControllerStore((s) => s.loginCeremony);

  const [loginUser, setLoginUser] = useState('');
  const [loginPass, setLoginPass] = useState('');
  const [loginTotp, setLoginTotp] = useState('');

  // 改动凭据对（用户名/密码）即丢弃任何待处理的二次验证步骤：验证码框只应对后端实际标记的
  // 那一对凭据出现。否则换个无 2FA 的账号后，提交按钮仍会被「需 6 位码」的守卫卡住。
  const onCredentialEdit = () => {
    if (totpRequired) {
      resetTOTPChallenge();
      setLoginTotp('');
    }
  };

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
              '需与服务端部署变量 YAOG_CONTROLLER_PATH_PREFIX 一致：此处不设置任何东西，只告诉面板 API 的位置。服务端未设置则留空。',
              "Must match the server's YAOG_CONTROLLER_PATH_PREFIX (set at deploy time). This sets nothing — it only tells the panel where the API is. Leave blank if the server has none.",
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
            <div className="space-y-2">
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  // 仅当后端要求二次码时才带上 totp（未启用 2FA 的账户不必填）。
                  const totp = totpRequired ? loginTotp : undefined;
                  void login(loginUser, loginPass, totp).then(() => {
                    // 登录成功后清空密码与验证码（不在 React 状态里多留）。
                    if (useControllerStore.getState().sessionToken) {
                      setLoginPass('');
                      setLoginTotp('');
                    }
                  });
                }}
                className="space-y-2"
              >
                <input
                  type="text"
                  value={loginUser}
                  onChange={(e) => {
                    setLoginUser(e.target.value);
                    onCredentialEdit();
                  }}
                  placeholder={txt(language, '用户名', 'Username')}
                  autoComplete="username"
                  className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                />
                <input
                  type="password"
                  value={loginPass}
                  onChange={(e) => {
                    setLoginPass(e.target.value);
                    onCredentialEdit();
                  }}
                  placeholder={txt(language, '密码', 'Password')}
                  autoComplete="current-password"
                  className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
                />
                {/* 二次因子：仅当后端返回 totp_required（密码已对、需验证码）时出现。 */}
                {totpRequired && (
                  <input
                    type="text"
                    inputMode="numeric"
                    value={loginTotp}
                    onChange={(e) => setLoginTotp(e.target.value.replace(/\D/g, '').slice(0, 6))}
                    placeholder={txt(language, '验证码 (6 位)', 'Code (6 digits)')}
                    autoComplete="one-time-code"
                    autoFocus
                    className="w-full px-2 py-1 bg-gray-600 rounded text-sm font-mono tracking-widest border border-blue-500 focus:border-blue-400 outline-none"
                  />
                )}
                <button
                  type="submit"
                  disabled={
                    loading ||
                    loginUser.trim() === '' ||
                    loginPass === '' ||
                    (totpRequired && loginTotp.length < 6)
                  }
                  className="px-4 py-1.5 text-sm bg-blue-600 hover:bg-blue-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
                >
                  {loading
                    ? txt(language, '登录中...', 'Signing in...')
                    : totpRequired
                    ? txt(language, '验证并登录', 'Verify & sign in')
                    : txt(language, '登录', 'Sign in')}
                </button>
                {totpRequired && (
                  <p className="text-[10px] text-sky-300">
                    {txt(
                      language,
                      '此账户启用了两步验证，请输入验证器 App 的 6 位码。',
                      'This account has 2FA enabled — enter the 6-digit code from your authenticator.',
                    )}
                  </p>
                )}
              </form>
              {/* 无密码 passkey 登录：输入用户名后直接用安全密钥登录（password+passkey 的
                  2FA 步骤由 store.login 在收到 passkey_required 后自动完成，无需额外输入）。 */}
              <div className="flex items-center gap-2 pt-1">
                <div className="flex-1 border-t border-gray-700" />
                <span className="text-[10px] text-gray-500">{txt(language, '或', 'or')}</span>
                <div className="flex-1 border-t border-gray-700" />
              </div>
              <button
                type="button"
                onClick={() => void loginWithPasskey(loginUser)}
                disabled={loading || loginUser.trim() === ''}
                className="w-full px-4 py-1.5 text-sm bg-fuchsia-700 hover:bg-fuchsia-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
              >
                {txt(language, '🔑 用 passkey 登录', '🔑 Sign in with passkey')}
              </button>
              {loginCeremony && (
                <p className="text-[10px] text-fuchsia-300">
                  {txt(language, '请触碰你的安全密钥...', 'Touch your security key...')}
                </p>
              )}
            </div>
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
