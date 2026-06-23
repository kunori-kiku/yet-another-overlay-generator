import { useEffect, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { localizeError } from '../../lib/localizeError';
import type { TOTPEnrollment } from '../../api/controllerClient';

// Two-factor (TOTP 2FA, plan-5.2): lets an operator who is logged in with a password self-enable/
// disable a time-based one-time-password second factor.
// Available only for a password session — a break-glass token has no account (the backend's
// currentOperator returns 403), in which case totpEnabled stays null and the UI prompts to "sign in
// with a password".
//
// Enrollment method (no QR-code dependency, never leaks the secret): show the otpauth:// URI + a
// grouped base32 setup key, and the operator uses "enter the key manually" in the authenticator. A QR
// code would pull in an extra dependency, and sending the secret to a third-party QR service is a
// leak — so it is deliberately not done. TOTP is used for login only, never as a signing mechanism
// (see docs/spec/controller/operator-auth.md).
export function TwoFactorSettings() {
  const language = useTopologyStore((s) => s.language);
  const loggedIn = useControllerStore(selectLoggedIn);
  const totpEnabled = useControllerStore((s) => s.totpEnabled);
  const loadTOTPStatus = useControllerStore((s) => s.loadTOTPStatus);
  const enrollTOTP = useControllerStore((s) => s.enrollTOTP);
  const confirmTOTP = useControllerStore((s) => s.confirmTOTP);
  const disableTOTP = useControllerStore((s) => s.disableTOTP);

  // Local state for the enroll ceremony: pending = the just-minted secret+uri (not persisted until
  // confirmed), code = the verification-code input (shared by confirm and disable), busy = request in
  // flight, localError = an in-place error (does not pollute the global banner).
  const [pending, setPending] = useState<TOTPEnrollment | null>(null);
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [localError, setLocalError] = useState<string | null>(null);
  const [copied, setCopied] = useState<'' | 'secret' | 'uri'>('');

  // Fetch once when logged in but the status is unknown (loadTOTPStatus is a store action, not a
  // useState setter — same shape as the loadSettings guard effect in BootstrapSettings, so it does not
  // trigger set-state-in-effect).
  useEffect(() => {
    if (loggedIn && totpEnabled === null) {
      void loadTOTPStatus();
    }
  }, [loggedIn, totpEnabled, loadTOTPStatus]);

  // Keep digits only, up to 6 (a TOTP is a 6-digit decimal code).
  const onCodeChange = (v: string) => setCode(v.replace(/\D/g, '').slice(0, 6));

  const handleEnroll = async () => {
    setBusy(true);
    setLocalError(null);
    setCode('');
    setCopied('');
    try {
      setPending(await enrollTOTP());
    } catch (err) {
      setLocalError(localizeError(err, language));
    } finally {
      setBusy(false);
    }
  };

  const handleConfirm = async () => {
    if (!pending || code.length < 6) return;
    setBusy(true);
    setLocalError(null);
    try {
      await confirmTOTP(pending.secret, code);
      // Activation succeeded: discard pending (the secret is now persisted server-side) and clear the
      // input. totpEnabled is set to true by the store.
      setPending(null);
      setCode('');
    } catch (err) {
      setLocalError(localizeError(err, language));
    } finally {
      setBusy(false);
    }
  };

  const handleCancelEnroll = () => {
    // Abandon an unconfirmed enroll: the server never persisted the secret, so a purely local discard
    // suffices.
    setPending(null);
    setCode('');
    setLocalError(null);
    setCopied('');
  };

  const handleDisable = async () => {
    if (code.length < 6) return;
    setBusy(true);
    setLocalError(null);
    try {
      await disableTOTP(code);
      setCode('');
    } catch (err) {
      setLocalError(localizeError(err, language));
    } finally {
      setBusy(false);
    }
  };

  const copyText = async (text: string, which: 'secret' | 'uri') => {
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setCopied(which);
    } catch {
      // Clipboard unavailable (non-secure context, etc.): keep the text selectable for manual copy, do
      // not raise an error.
      setCopied('');
    }
  };

  // Group the base32 secret every 4 characters to ease manual transcription into the authenticator
  // (does not change the value, display only).
  const groupedSecret = (s: string) => s.replace(/(.{4})/g, '$1 ').trim();

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-[var(--info)]">
        {t(language, 'twoFactorSettings.twoFactorTOTP')}
      </h3>
      <p className="text-sm text-[var(--content-muted)]">
        {t(language, 'twoFactorSettings.addATimeBased')}
      </p>

      {!loggedIn ? (
        <p className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded">
          {t(language, 'twoFactorSettings.signInWithYour')}
        </p>
      ) : totpEnabled === null ? (
        <p className="text-xs text-[var(--content-muted)]">{t(language, 'twoFactorSettings.checkingStatus')}</p>
      ) : totpEnabled ? (
        // Enabled: show the status + require a current code to disable.
        <div className="space-y-2">
          <p className="text-xs text-[var(--success)] bg-[var(--success-bg)] px-2 py-1 rounded">
            {t(language, 'twoFactorSettings.twoFactorIsEnabled')}
          </p>
          <label className="text-xs text-[var(--content-muted)] block">
            {t(language, 'twoFactorSettings.enterACurrentCode')}
          </label>
          <div className="flex gap-2">
            <input
              type="text"
              inputMode="numeric"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => onCodeChange(e.target.value)}
              placeholder="123456"
              className="flex-1 px-2 py-1 bg-[var(--control)] rounded text-sm font-mono tracking-widest border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
            />
            <button
              onClick={() => void handleDisable()}
              disabled={busy || code.length < 6}
              className="px-4 py-1.5 text-sm bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--danger-solid-fg)] font-medium"
            >
              {busy ? t(language, 'twoFactorSettings.working') : t(language, 'twoFactorSettings.disable2FA')}
            </button>
          </div>
        </div>
      ) : pending === null ? (
        // Disabled and enroll not yet started: a single enable button.
        <button
          onClick={() => void handleEnroll()}
          disabled={busy}
          className="px-4 py-1.5 text-sm bg-[var(--accent)] hover:bg-[var(--accent-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--accent-fg)] font-medium"
        >
          {busy ? t(language, 'twoFactorSettings.preparing') : t(language, 'twoFactorSettings.enableTwoFactor')}
        </button>
      ) : (
        // Enroll in progress: show the setup key + otpauth URI and take a code to complete activation.
        <div className="space-y-3 p-3 bg-[var(--surface-sunken)] border border-[var(--hairline)] rounded">
          <p className="text-xs text-[var(--content)]">
            {t(language, 'twoFactorSettings.1AddTheKey')}
          </p>
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-[var(--content-muted)] uppercase tracking-wider">
                {t(language, 'twoFactorSettings.setupKey')}
              </label>
              <button
                onClick={() => void copyText(pending.secret, 'secret')}
                className="px-2 py-0.5 text-xs bg-[var(--control)] hover:bg-[var(--control-hover)] rounded text-[var(--content)]"
              >
                {copied === 'secret' ? t(language, 'twoFactorSettings.copied') : t(language, 'twoFactorSettings.copy')}
              </button>
            </div>
            <pre className="text-sm text-[var(--info)] font-mono break-all whitespace-pre-wrap bg-[var(--surface-sunken)] p-2 rounded tracking-widest">
              {groupedSecret(pending.secret)}
            </pre>
          </div>
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-[var(--content-muted)] uppercase tracking-wider">
                {t(language, 'twoFactorSettings.otpauthLink')}
              </label>
              <button
                onClick={() => void copyText(pending.otpauthURI, 'uri')}
                className="px-2 py-0.5 text-xs bg-[var(--control)] hover:bg-[var(--control-hover)] rounded text-[var(--content)]"
              >
                {copied === 'uri' ? t(language, 'twoFactorSettings.copied_2') : t(language, 'twoFactorSettings.copy_2')}
              </button>
            </div>
            <pre className="text-[11px] text-[var(--content-muted)] font-mono break-all whitespace-pre-wrap bg-[var(--surface-sunken)] p-2 rounded">
              {pending.otpauthURI}
            </pre>
          </div>
          <div>
            <label className="text-xs text-[var(--content-muted)] block mb-1">
              {t(language, 'twoFactorSettings.6DigitCodeFrom')}
            </label>
            <div className="flex gap-2">
              <input
                type="text"
                inputMode="numeric"
                autoComplete="one-time-code"
                value={code}
                onChange={(e) => onCodeChange(e.target.value)}
                placeholder="123456"
                className="flex-1 px-2 py-1 bg-[var(--control)] rounded text-sm font-mono tracking-widest border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
              />
              <button
                onClick={() => void handleConfirm()}
                disabled={busy || code.length < 6}
                className="px-4 py-1.5 text-sm bg-[var(--success-solid)] hover:bg-[var(--success-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--success-solid-fg)] font-medium"
              >
                {busy ? t(language, 'twoFactorSettings.verifying') : t(language, 'twoFactorSettings.confirmEnable')}
              </button>
              <button
                onClick={handleCancelEnroll}
                disabled={busy}
                className="px-3 py-1.5 text-sm bg-[var(--control)] hover:bg-[var(--control-hover)] disabled:bg-[var(--control)] rounded text-[var(--content)]"
              >
                {t(language, 'twoFactorSettings.cancel')}
              </button>
            </div>
          </div>
        </div>
      )}

      {localError && (
        <p className="text-xs text-[var(--danger)] bg-[var(--danger-bg)] px-2 py-1 rounded break-all">⚠️ {localError}</p>
      )}
    </section>
  );
}
