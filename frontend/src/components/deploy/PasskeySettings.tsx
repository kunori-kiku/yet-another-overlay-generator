import { useEffect, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { localizeError } from '../../lib/localizeError';

// Login Passkey (plan-5.2): lets an operator who is logged in with a password register/remove a
// WebAuthn login passkey — a phishing-resistant second factor that can also be used for passwordless
// login. It is a different credential from the keystone signing passkey (a different key, a different
// purpose: this one is for login only). Available only for a password session — a break-glass token
// has no account (the backend returns 403), in which case passkeyRegistered stays null and the UI
// prompts to "sign in with a password".
//
// Registration reuses navigator.credentials.create() (only the public key leaves the authenticator);
// removal requires a fresh assertion (so a hijacked session cannot simply strip the factor). Both pop
// the authenticator — the loginCeremony flag drives the "touch your security key" prompt (separate
// from keystone's signing/enrolling, and it does not light up the DeployBar deploy banner). Errors are
// shown in place (the store action throws here), consistent with the local errors in
// TwoFactorSettings.
export function PasskeySettings() {
  const language = useTopologyStore((s) => s.language);
  const loggedIn = useControllerStore(selectLoggedIn);
  const passkeyRegistered = useControllerStore((s) => s.passkeyRegistered);
  const loadPasskeyStatus = useControllerStore((s) => s.loadPasskeyStatus);
  const registerPasskey = useControllerStore((s) => s.registerPasskey);
  const disablePasskey = useControllerStore((s) => s.disablePasskey);
  const loginCeremony = useControllerStore((s) => s.loginCeremony);

  const [localError, setLocalError] = useState<string | null>(null);

  // Fetch once when logged in but the status is unknown (a store action, not a setState — same shape as
  // BootstrapSettings).
  useEffect(() => {
    if (loggedIn && passkeyRegistered === null) {
      void loadPasskeyStatus();
    }
  }, [loggedIn, passkeyRegistered, loadPasskeyStatus]);

  const handleRegister = async () => {
    setLocalError(null);
    try {
      await registerPasskey();
    } catch (err) {
      setLocalError(localizeError(err, language));
    }
  };

  const handleDisable = async () => {
    setLocalError(null);
    try {
      await disablePasskey();
    } catch (err) {
      setLocalError(localizeError(err, language));
    }
  };

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-[var(--accent)]">
        {t(language, 'passkeySettings.loginPasskey')}
      </h3>
      <p className="text-sm text-[var(--content-muted)]">
        {t(language, 'passkeySettings.registerAWebAuthnPasskey')}
      </p>

      {!loggedIn ? (
        <p className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded">
          {t(language, 'passkeySettings.signInWithYour')}
        </p>
      ) : passkeyRegistered === null ? (
        <p className="text-xs text-[var(--content-muted)]">{t(language, 'passkeySettings.checkingStatus')}</p>
      ) : passkeyRegistered ? (
        <div className="space-y-2">
          <p className="text-xs text-[var(--success)] bg-[var(--success-bg)] px-2 py-1 rounded">
            {t(language, 'passkeySettings.aLoginPasskeyIs')}
          </p>
          <button
            onClick={() => void handleDisable()}
            disabled={loginCeremony}
            className="px-4 py-1.5 text-sm bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--danger-solid-fg)] font-medium"
          >
            {loginCeremony
              ? t(language, 'passkeySettings.touchYourSecurityKey')
              : t(language, 'passkeySettings.removePasskey')}
          </button>
          <p className="text-[10px] text-[var(--content-muted)]">
            {t(language, 'passkeySettings.removalRequiresAFresh')}
          </p>
        </div>
      ) : (
        <button
          onClick={() => void handleRegister()}
          disabled={loginCeremony}
          className="px-4 py-1.5 text-sm bg-[var(--accent)] hover:bg-[var(--accent-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--accent-fg)] font-medium"
        >
          {loginCeremony
            ? t(language, 'passkeySettings.touchYourSecurityKey_2')
            : t(language, 'passkeySettings.registerALoginPasskey')}
        </button>
      )}

      {localError && (
        <p className="text-xs text-[var(--danger)] bg-[var(--danger-bg)] px-2 py-1 rounded break-all">⚠️ {localError}</p>
      )}
    </section>
  );
}
