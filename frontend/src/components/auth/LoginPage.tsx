import { useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { ThemeToggle } from '../shell/ThemeToggle';
import { LanguageToggle } from '../shell/LanguageToggle';
import { landingPathForMode } from '../shell/nav';
import { FOCUS_RING } from '../shell/styles';

// plan-4（D2）：持久化的 controller 模式下，进入面板首先落在这个全屏登录页——任何 shell
// chrome（侧栏/顶栏/画布）渲染之前。登录表单逻辑从 ConnectionSettings 原样迁来（move,
// not fork）：用户名/密码 + 条件 TOTP、无密码 passkey、折叠的 break-glass 入口。另含
// 折叠的「连接」配置（Operator 基址 + secret 前缀）——没有它，跨源/全新浏览器部署在
// 登录页就会被锁死（API 地址只能在登录后的设置页改，而设置页在门后）。
export function LoginPage() {
  const language = useTopologyStore((s) => s.language);
  const navigate = useNavigate();

  const baseURL = useControllerStore((s) => s.baseURL);
  const pathPrefix = useControllerStore((s) => s.pathPrefix);
  const setConfig = useControllerStore((s) => s.setConfig);
  const loading = useControllerStore((s) => s.loading);
  const error = useControllerStore((s) => s.error);
  const login = useControllerStore((s) => s.login);
  const loginWithPasskey = useControllerStore((s) => s.loginWithPasskey);
  const loginCeremony = useControllerStore((s) => s.loginCeremony);
  const totpRequired = useControllerStore((s) => s.totpRequired);
  const resetTOTPChallenge = useControllerStore((s) => s.resetTOTPChallenge);
  const switchToLocal = useControllerStore((s) => s.switchToLocal);

  const [loginUser, setLoginUser] = useState('');
  const [loginPass, setLoginPass] = useState('');
  const [loginTotp, setLoginTotp] = useState('');
  // Passkey 登录是「用户名定向」的（服务端按用户名取该账户的 credential 来挑战），所以需要先有
  // 用户名。与其把按钮置灰（看起来像坏的），不如让它始终可点：空用户名时聚焦用户名框并给出提示，
  // 把死按钮变成有引导的活按钮。
  const usernameRef = useRef<HTMLInputElement>(null);
  const [passkeyHint, setPasskeyHint] = useState(false);
  const [showConnection, setShowConnection] = useState(false);
  const [showBreakGlass, setShowBreakGlass] = useState(false);
  // The break-glass token uses a LOCAL buffer, committed to the store only on an
  // explicit button click — NOT bound directly. The Shell gate opens the instant
  // operatorToken becomes non-empty, so a directly-bound field would unmount this
  // page after the first keystroke, making the token impossible to type (review).
  const [breakGlassInput, setBreakGlassInput] = useState('');

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
        <LanguageToggle />
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
              <h1 className="text-lg font-semibold">{t(language, 'brandName')}</h1>
              <p className="text-sm text-[var(--content-muted)]">
                {t(language, 'loginPage.controllerModeSignIn')}
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
                {t(language, 'loginPage.username')}
              </label>
              <input
                id="login-username"
                ref={usernameRef}
                type="text"
                value={loginUser}
                onChange={(e) => {
                  setLoginUser(e.target.value);
                  if (passkeyHint) setPasskeyHint(false);
                  onCredentialEdit();
                }}
                autoComplete="username"
                autoFocus
                className={inputClass}
              />
            </div>
            <div>
              <label htmlFor="login-password" className="mb-1 block text-xs text-[var(--content-muted)]">
                {t(language, 'loginPage.password')}
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
                  {t(language, 'loginPage.code6Digits')}
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
                  {t(language, 'loginPage.thisAccountHas2FA')}
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
                ? t(language, 'loginPage.signingIn')
                : totpRequired
                ? t(language, 'loginPage.verifySignIn')
                : t(language, 'loginPage.signIn')}
            </button>
          </form>

          {/* 无密码 passkey 登录（password+passkey 2FA 步骤由 store.login 自动完成）。 */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <div className="flex-1 border-t border-[var(--hairline)]" />
              <span className="text-xs text-[var(--content-muted)]">{t(language, 'loginPage.or')}</span>
              <div className="flex-1 border-t border-[var(--hairline)]" />
            </div>
            <button
              type="button"
              onClick={() => {
                // 用户名为空时不静默失败、也不置灰：聚焦用户名框并提示「先填用户名」，再让用户点一次。
                if (loginUser.trim() === '') {
                  setPasskeyHint(true);
                  usernameRef.current?.focus();
                  return;
                }
                setPasskeyHint(false);
                void loginWithPasskey(loginUser);
              }}
              disabled={loading || loginCeremony}
              className={`w-full rounded-lg border border-[var(--hairline)] py-2 text-sm font-medium text-[var(--content)] transition-colors hover:bg-[var(--surface-sunken)] disabled:opacity-40 ${FOCUS_RING}`}
            >
              {t(language, 'loginPage.signInWithPasskey')}
            </button>
            {passkeyHint && loginUser.trim() === '' && (
              <p className="text-center text-xs text-[var(--content-muted)]" role="status">
                {t(language, 'loginPage.enterYourUsernameAbove')}
              </p>
            )}
            {loginCeremony && (
              <p className="text-center text-xs text-[var(--content-muted)]" role="status">
                {t(language, 'loginPage.touchYourSecurityKey')}
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
              {showConnection ? '▾' : '▸'} {t(language, 'loginPage.connectionSettings')}
            </button>
            {showConnection && (
              <div className="space-y-2 rounded-lg border border-[var(--hairline)] p-3">
                <div>
                  <label htmlFor="login-baseurl" className="mb-1 block text-xs text-[var(--content-muted)]">
                    {t(language, 'loginPage.operatorBaseURL')}
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
                    {t(language, 'loginPage.secretPathPrefixOptional')}
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
                    {t(language, 'loginPage.mustMatchTheServer')}
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
              {showBreakGlass ? '▾' : '▸'} {t(language, 'loginPage.recoveryBreakGlass')}
            </button>
            {showBreakGlass && (
              <form
                className="space-y-2 rounded-lg border border-[var(--hairline)] p-3"
                onSubmit={(e) => {
                  e.preventDefault();
                  // Commit the buffered token to the store in one shot — this is what
                  // opens the gate, AFTER the full token is entered.
                  if (breakGlassInput.trim() !== '') {
                    setConfig({ operatorToken: breakGlassInput });
                  }
                }}
              >
                <label htmlFor="login-breakglass" className="block text-xs text-[var(--content-muted)]">
                  {t(language, 'loginPage.operatorTokenBreakGlass')}
                </label>
                <input
                  id="login-breakglass"
                  type="password"
                  value={breakGlassInput}
                  onChange={(e) => setBreakGlassInput(e.target.value)}
                  placeholder={t(language, 'loginPage.optionalNeverPersisted')}
                  autoComplete="off"
                  className={inputClass}
                />
                <button
                  type="submit"
                  disabled={breakGlassInput.trim() === ''}
                  className={`w-full rounded-lg border border-[var(--hairline)] py-1.5 text-sm text-[var(--content)] transition-colors hover:bg-[var(--surface-sunken)] disabled:opacity-40 ${FOCUS_RING}`}
                >
                  {t(language, 'loginPage.enterWithRecoveryToken')}
                </button>
                <p className="text-xs text-[var(--content-muted)]">
                  {t(language, 'loginPage.submittingOpensThePanel')}
                </p>
              </form>
            )}

            {/* 回到本地模式：登录门不该把只想用本地设计器的用户锁在外面。controller→local
                是有损切换（plan-5，D6）：window.confirm 列出损失（与 SettingsPage 对话框一致的
                语义），确认后清洗密钥/分配/历史，再切换并导航到本地落地页（避免停留在
                controller-only 的深链路由如 /fleet 上渲染空白）。 */}
            <button
              type="button"
              onClick={() => {
                // 安全分叉的确认文案据 canvasFromServer 选择：服务端机密镜像 → 警告整画布清空
                //（绝不让 fleet 的公网 IP/SSH 目标随切换泄漏）；本地原创工作 → D6 的「保图、清
                // 密钥/分配/历史」。实际切换逻辑统一走 controllerStore.switchToLocal（与设置页共用，
                // 杜绝两处发散——plan-10 / T1）。确认后导航到本地落地页。
                const serverHeld = useTopologyStore.getState().canvasFromServer;
                const ok = window.confirm(
                  serverHeld
                    ? t(language, 'loginPage.notSignedInSo')
                    : t(language, 'loginPage.switchingToLocalMode'),
                );
                if (ok) {
                  switchToLocal();
                  navigate(landingPathForMode('local'));
                }
              }}
              className={`block text-xs text-[var(--content-muted)] underline-offset-2 hover:underline ${FOCUS_RING}`}
            >
              {t(language, 'loginPage.switchToLocalMode')}
            </button>
          </div>
        </div>
      </main>
    </div>
  );
}
