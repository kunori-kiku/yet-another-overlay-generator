import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { txt, STRINGS } from '../../i18n';
import { ThemeToggle } from '../shell/ThemeToggle';
import { FOCUS_RING } from '../shell/styles';

// plan-4（D2）：持久化的 controller 模式下，进入面板首先落在这个全屏登录页——任何 shell
// chrome（侧栏/顶栏/画布）渲染之前。登录表单逻辑从 ConnectionSettings 原样迁来（move,
// not fork）：用户名/密码 + 条件 TOTP、无密码 passkey、折叠的 break-glass 入口。另含
// 折叠的「连接」配置（Operator 基址 + secret 前缀）——没有它，跨源/全新浏览器部署在
// 登录页就会被锁死（API 地址只能在登录后的设置页改，而设置页在门后）。
export function LoginPage() {
  const language = useTopologyStore((s) => s.language);
  const setLanguage = useTopologyStore((s) => s.setLanguage);

  const baseURL = useControllerStore((s) => s.baseURL);
  const pathPrefix = useControllerStore((s) => s.pathPrefix);
  const operatorToken = useControllerStore((s) => s.operatorToken);
  const setConfig = useControllerStore((s) => s.setConfig);
  const loading = useControllerStore((s) => s.loading);
  const error = useControllerStore((s) => s.error);
  const login = useControllerStore((s) => s.login);
  const loginWithPasskey = useControllerStore((s) => s.loginWithPasskey);
  const loginCeremony = useControllerStore((s) => s.loginCeremony);
  const totpRequired = useControllerStore((s) => s.totpRequired);
  const resetTOTPChallenge = useControllerStore((s) => s.resetTOTPChallenge);
  const setMode = useControllerStore((s) => s.setMode);

  const [loginUser, setLoginUser] = useState('');
  const [loginPass, setLoginPass] = useState('');
  const [loginTotp, setLoginTotp] = useState('');
  const [showConnection, setShowConnection] = useState(false);
  const [showBreakGlass, setShowBreakGlass] = useState(false);

  // 改动凭据对即丢弃任何待处理的二次验证步骤（迁自 ConnectionSettings）。
  const onCredentialEdit = () => {
    if (totpRequired) {
      resetTOTPChallenge();
      setLoginTotp('');
    }
  };

  const inputClass =
    'w-full rounded-lg border border-[var(--hairline)] bg-[var(--surface-sunken)] px-3 py-2 text-sm text-[var(--content)] outline-none transition-colors focus:border-[var(--accent)]';

  return (
    <div className="flex min-h-screen flex-col bg-[var(--surface)] text-[var(--content)]">
      {/* 顶部右侧：登录前也可切换语言与主题（a11y / 多语操作员）。 */}
      <header className="flex items-center justify-end gap-2 p-3">
        <div className="flex items-center overflow-hidden rounded-lg border border-[var(--hairline)]">
          <button
            type="button"
            onClick={() => setLanguage('zh')}
            className={`px-2 py-1 text-xs transition-colors ${FOCUS_RING} ${
              language === 'zh'
                ? 'bg-[var(--accent)] text-[var(--accent-fg)]'
                : 'text-[var(--content-muted)] hover:bg-[var(--surface-sunken)]'
            }`}
          >
            中文
          </button>
          <button
            type="button"
            onClick={() => setLanguage('en')}
            className={`px-2 py-1 text-xs transition-colors ${FOCUS_RING} ${
              language === 'en'
                ? 'bg-[var(--accent)] text-[var(--accent-fg)]'
                : 'text-[var(--content-muted)] hover:bg-[var(--surface-sunken)]'
            }`}
          >
            EN
          </button>
        </div>
        <ThemeToggle />
      </header>

      <main className="flex flex-1 items-center justify-center px-4 pb-16">
        <div className="w-full max-w-sm space-y-6">
          {/* 品牌区：与侧栏一致的 Y 方块 + 名称。 */}
          <div className="flex flex-col items-center gap-3">
            <div className="grid h-12 w-12 place-items-center rounded-2xl bg-[var(--accent)] text-xl font-semibold text-[var(--accent-fg)]">
              Y
            </div>
            <div className="text-center">
              <h1 className="text-lg font-semibold">{txt(language, ...STRINGS.brandName)}</h1>
              <p className="text-sm text-[var(--content-muted)]">
                {txt(language, '控制器模式 · 请登录', 'Controller mode · sign in to continue')}
              </p>
            </div>
          </div>

          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              const totp = totpRequired ? loginTotp : undefined;
              void login(loginUser, loginPass, totp).then(() => {
                if (useControllerStore.getState().sessionToken) {
                  setLoginPass('');
                  setLoginTotp('');
                }
              });
            }}
          >
            <div>
              <label htmlFor="login-username" className="mb-1 block text-xs text-[var(--content-muted)]">
                {txt(language, '用户名', 'Username')}
              </label>
              <input
                id="login-username"
                type="text"
                value={loginUser}
                onChange={(e) => {
                  setLoginUser(e.target.value);
                  onCredentialEdit();
                }}
                autoComplete="username"
                autoFocus
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="login-password" className="mb-1 block text-xs text-[var(--content-muted)]">
                {txt(language, '密码', 'Password')}
              </label>
              <input
                id="login-password"
                type="password"
                value={loginPass}
                onChange={(e) => {
                  setLoginPass(e.target.value);
                  onCredentialEdit();
                }}
                autoComplete="current-password"
                className={inputClass}
              />
            </div>
            {/* 二次因子：仅当后端返回 totp_required（密码已对、需验证码）时出现。 */}
            {totpRequired && (
              <div>
                <label htmlFor="login-totp" className="mb-1 block text-xs text-[var(--content-muted)]">
                  {txt(language, '验证码 (6 位)', 'Code (6 digits)')}
                </label>
                <input
                  id="login-totp"
                  type="text"
                  inputMode="numeric"
                  value={loginTotp}
                  onChange={(e) => setLoginTotp(e.target.value.replace(/\D/g, '').slice(0, 6))}
                  autoComplete="one-time-code"
                  autoFocus
                  className={`${inputClass} font-mono tracking-widest`}
                />
                <p className="mt-1 text-xs text-[var(--content-muted)]">
                  {txt(
                    language,
                    '此账户启用了两步验证，请输入验证器 App 的 6 位码。',
                    'This account has 2FA enabled — enter the 6-digit code from your authenticator.',
                  )}
                </p>
              </div>
            )}
            <button
              type="submit"
              disabled={
                loading ||
                loginUser.trim() === '' ||
                loginPass === '' ||
                (totpRequired && loginTotp.length < 6)
              }
              className={`w-full rounded-lg bg-[var(--accent)] py-2 text-sm font-medium text-[var(--accent-fg)] transition-opacity disabled:opacity-40 ${FOCUS_RING}`}
            >
              {loading
                ? txt(language, '登录中...', 'Signing in...')
                : totpRequired
                ? txt(language, '验证并登录', 'Verify & sign in')
                : txt(language, '登录', 'Sign in')}
            </button>
          </form>

          {/* 无密码 passkey 登录（password+passkey 2FA 步骤由 store.login 自动完成）。 */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <div className="flex-1 border-t border-[var(--hairline)]" />
              <span className="text-xs text-[var(--content-muted)]">{txt(language, '或', 'or')}</span>
              <div className="flex-1 border-t border-[var(--hairline)]" />
            </div>
            <button
              type="button"
              onClick={() => void loginWithPasskey(loginUser)}
              disabled={loading || loginUser.trim() === ''}
              className={`w-full rounded-lg border border-[var(--hairline)] py-2 text-sm font-medium text-[var(--content)] transition-colors hover:bg-[var(--surface-sunken)] disabled:opacity-40 ${FOCUS_RING}`}
            >
              {txt(language, '🔑 用 passkey 登录', '🔑 Sign in with passkey')}
            </button>
            {loginCeremony && (
              <p className="text-center text-xs text-[var(--content-muted)]" role="status">
                {txt(language, '请触碰你的安全密钥...', 'Touch your security key...')}
              </p>
            )}
          </div>

          {error && (
            <p className="break-all rounded-lg bg-red-500/10 px-3 py-2 text-xs text-red-500" role="alert">
              ⚠️ {error}
            </p>
          )}

          {/* 折叠：连接配置。没有它，跨源部署/全新浏览器在门外无法指向正确的 API。 */}
          <div className="space-y-2 text-sm">
            <button
              type="button"
              onClick={() => setShowConnection((v) => !v)}
              aria-expanded={showConnection}
              className={`text-xs text-[var(--content-muted)] underline-offset-2 hover:underline ${FOCUS_RING}`}
            >
              {showConnection ? '▾' : '▸'} {txt(language, '连接设置', 'Connection settings')}
            </button>
            {showConnection && (
              <div className="space-y-2 rounded-lg border border-[var(--hairline)] p-3">
                <div>
                  <label htmlFor="login-baseurl" className="mb-1 block text-xs text-[var(--content-muted)]">
                    {txt(language, 'Operator 基础地址', 'Operator Base URL')}
                  </label>
                  <input
                    id="login-baseurl"
                    type="text"
                    value={baseURL}
                    onChange={(e) => setConfig({ baseURL: e.target.value })}
                    placeholder="http://localhost:8080"
                    className={inputClass}
                  />
                </div>
                <div>
                  <label htmlFor="login-prefix" className="mb-1 block text-xs text-[var(--content-muted)]">
                    {txt(language, 'Secret 路径前缀（可选）', 'Secret Path Prefix (optional)')}
                  </label>
                  <input
                    id="login-prefix"
                    type="text"
                    value={pathPrefix}
                    onChange={(e) => setConfig({ pathPrefix: e.target.value })}
                    placeholder="/s3cr3t"
                    className={inputClass}
                  />
                  <p className="mt-1 text-xs text-[var(--content-muted)]">
                    {txt(
                      language,
                      '需与服务端 YAOG_OPERATOR_PATH_PREFIX 一致；未设置则留空。',
                      "Must match the server's YAOG_OPERATOR_PATH_PREFIX; leave blank if unset.",
                    )}
                  </p>
                </div>
              </div>
            )}

            {/* 折叠：break-glass 恢复凭据。它不是登录（不建会话），但持有它即可进入面板
                （恢复路径不能被登录门锁死）。 */}
            <button
              type="button"
              onClick={() => setShowBreakGlass((v) => !v)}
              aria-expanded={showBreakGlass}
              className={`block text-xs text-[var(--content-muted)] underline-offset-2 hover:underline ${FOCUS_RING}`}
            >
              {showBreakGlass ? '▾' : '▸'} {txt(language, '恢复（break-glass）', 'Recovery (break-glass)')}
            </button>
            {showBreakGlass && (
              <div className="space-y-1 rounded-lg border border-[var(--hairline)] p-3">
                <label htmlFor="login-breakglass" className="mb-1 block text-xs text-[var(--content-muted)]">
                  {txt(language, 'Operator Token（恢复用）', 'Operator token (break-glass)')}
                </label>
                <input
                  id="login-breakglass"
                  type="password"
                  value={operatorToken}
                  onChange={(e) => setConfig({ operatorToken: e.target.value })}
                  placeholder={txt(language, '可选；不会被持久化', 'Optional; never persisted')}
                  autoComplete="off"
                  className={inputClass}
                />
                <p className="text-xs text-[var(--content-muted)]">
                  {txt(
                    language,
                    '输入后即可进入面板（不创建会话；仅当后端设置了 YAOG_CONTROLLER_OPERATOR_TOKEN 时有效）。',
                    'Entering a token opens the panel (no session is created; only works when the backend sets YAOG_CONTROLLER_OPERATOR_TOKEN).',
                  )}
                </p>
              </div>
            )}

            {/* 回到本地模式：登录门不该把只想用本地设计器的用户锁在外面。 */}
            <button
              type="button"
              onClick={() => setMode('local')}
              className={`block text-xs text-[var(--content-muted)] underline-offset-2 hover:underline ${FOCUS_RING}`}
            >
              {txt(language, '← 切换到本地模式（无需登录）', '← Switch to local mode (no sign-in needed)')}
            </button>
          </div>
        </div>
      </main>
    </div>
  );
}
