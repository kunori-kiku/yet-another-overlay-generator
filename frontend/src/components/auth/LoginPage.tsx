import { useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { ThemeToggle } from '../shell/ThemeToggle';
import { LanguageToggle } from '../shell/LanguageToggle';
import { landingPathForMode } from '../shell/nav';
import { FOCUS_RING } from '../shell/styles';

// plan-4 (D2): in persisted controller mode, entering the panel lands first on this full-screen
// login page — before any shell chrome (sidebar/topbar/canvas) renders. The login-form logic is
// moved verbatim from ConnectionSettings (move, not fork): username/password + conditional TOTP,
// passwordless passkey, and a collapsed break-glass entry. It also includes a collapsed
// "Connection" config (operator base URL + secret prefix) — without it, a cross-origin / fresh-
// browser deploy would be locked out at the login page (the API address can only be changed on
// the post-login settings page, and that page lives behind the gate).
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
  // Passkey login is username-directed (the server looks up that account's credential by
  // username to issue the challenge), so a username must come first. Rather than graying the
  // button out (which looks broken), keep it always clickable: on an empty username, focus the
  // username field and show a hint — turning a dead button into a guided, live one.
  const usernameRef = useRef<HTMLInputElement>(null);
  const [passkeyHint, setPasskeyHint] = useState(false);
  const [showConnection, setShowConnection] = useState(false);
  const [showBreakGlass, setShowBreakGlass] = useState(false);
  // The break-glass token uses a LOCAL buffer, committed to the store only on an
  // explicit button click — NOT bound directly. The Shell gate opens the instant
  // operatorToken becomes non-empty, so a directly-bound field would unmount this
  // page after the first keystroke, making the token impossible to type (review).
  const [breakGlassInput, setBreakGlassInput] = useState('');

  // Editing the credential pair discards any pending second-factor step (moved from ConnectionSettings).
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
      {/* Top right: language and theme can be toggled even before login (a11y / multilingual operators). */}
      <header className="flex items-center justify-end gap-2 p-3">
        <LanguageToggle />
        <ThemeToggle />
      </header>

      <main className="flex flex-1 items-center justify-center px-4 pb-16">
        <div className="w-full max-w-sm space-y-6">
          {/* Brand block: the Y square + name, matching the sidebar. */}
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
            {/* Second factor: appears only when the backend returns totp_required (password
                correct, code needed). */}
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

          {/* Passwordless passkey login (the password+passkey 2FA step is handled automatically
              by store.login). */}
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <div className="flex-1 border-t border-[var(--hairline)]" />
              <span className="text-xs text-[var(--content-muted)]">{t(language, 'loginPage.or')}</span>
              <div className="flex-1 border-t border-[var(--hairline)]" />
            </div>
            <button
              type="button"
              onClick={() => {
                // On an empty username, neither fail silently nor gray out: focus the username
                // field and prompt "fill in the username first", then let the user click again.
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

          {/* Collapsed: connection config. Without it, a cross-origin deploy / fresh browser
              cannot point at the correct API while still outside the gate. */}
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

            {/* Collapsed: break-glass recovery credential. It is not a login (it mints no session),
                but holding it is enough to enter the panel (the recovery path must not be locked
                out by the login gate). */}
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

            {/* Back to local mode: the login gate must not lock out a user who only wants the
                local designer. controller→local is a lossy switch (plan-5, D6): window.confirm
                lists the losses (same semantics as the SettingsPage dialog); on confirm it purges
                keys/allocations/history, then switches and navigates to the local landing page
                (avoiding rendering blank on a controller-only deep-link route such as /fleet). */}
            <button
              type="button"
              onClick={() => {
                // The safety-forked confirm copy is chosen by canvasFromServer: a server-held secret
                // mirror → warn that the whole canvas is cleared (never let the fleet's public IPs /
                // SSH targets leak across the switch); local-original work → D6's "keep the graph,
                // purge keys/allocations/history". The actual switch logic goes through the shared
                // controllerStore.switchToLocal (used by the settings page too, so the two never
                // diverge — plan-10 / T1). On confirm it navigates to the local landing page.
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
