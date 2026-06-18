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
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-sky-400">
        {t(language, 'twoFactorSettings.twoFactorTOTP')}
      </h3>
      <p className="text-sm text-gray-400">
        {t(language, 'twoFactorSettings.addATimeBased')}
      </p>

      {!loggedIn ? (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {t(language, 'twoFactorSettings.signInWithYour')}
        </p>
      ) : totpEnabled === null ? (
        <p className="text-xs text-gray-500">{t(language, 'twoFactorSettings.checkingStatus')}</p>
      ) : totpEnabled ? (
        // Enabled: show the status + require a current code to disable.
        <div className="space-y-2">
          <p className="text-xs text-green-300 bg-green-900/20 px-2 py-1 rounded">
            {t(language, 'twoFactorSettings.twoFactorIsEnabled')}
          </p>
          <label className="text-xs text-gray-400 block">
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
              className="flex-1 px-2 py-1 bg-gray-600 rounded text-sm font-mono tracking-widest border border-gray-500 focus:border-blue-400 outline-none"
            />
            <button
              onClick={() => void handleDisable()}
              disabled={busy || code.length < 6}
              className="px-4 py-1.5 text-sm bg-red-700 hover:bg-red-600 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
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
          className="px-4 py-1.5 text-sm bg-sky-600 hover:bg-sky-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
        >
          {busy ? t(language, 'twoFactorSettings.preparing') : t(language, 'twoFactorSettings.enableTwoFactor')}
        </button>
      ) : (
        // Enroll in progress: show the setup key + otpauth URI and take a code to complete activation.
        <div className="space-y-3 p-3 bg-gray-900 border border-gray-700 rounded">
          <p className="text-xs text-gray-300">
            {t(language, 'twoFactorSettings.1AddTheKey')}
          </p>
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-gray-500 uppercase tracking-wider">
                {t(language, 'twoFactorSettings.setupKey')}
              </label>
              <button
                onClick={() => void copyText(pending.secret, 'secret')}
                className="px-2 py-0.5 text-xs bg-gray-700 hover:bg-gray-600 rounded text-gray-200"
              >
                {copied === 'secret' ? t(language, 'twoFactorSettings.copied') : t(language, 'twoFactorSettings.copy')}
              </button>
            </div>
            <pre className="text-sm text-sky-200 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded tracking-widest">
              {groupedSecret(pending.secret)}
            </pre>
          </div>
          <div>
            <div className="flex items-center justify-between">
              <label className="text-[10px] text-gray-500 uppercase tracking-wider">
                {t(language, 'twoFactorSettings.otpauthLink')}
              </label>
              <button
                onClick={() => void copyText(pending.otpauthURI, 'uri')}
                className="px-2 py-0.5 text-xs bg-gray-700 hover:bg-gray-600 rounded text-gray-200"
              >
                {copied === 'uri' ? t(language, 'twoFactorSettings.copied_2') : t(language, 'twoFactorSettings.copy_2')}
              </button>
            </div>
            <pre className="text-[11px] text-gray-400 font-mono break-all whitespace-pre-wrap bg-gray-950 p-2 rounded">
              {pending.otpauthURI}
            </pre>
          </div>
          <div>
            <label className="text-xs text-gray-400 block mb-1">
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
                className="flex-1 px-2 py-1 bg-gray-600 rounded text-sm font-mono tracking-widest border border-gray-500 focus:border-blue-400 outline-none"
              />
              <button
                onClick={() => void handleConfirm()}
                disabled={busy || code.length < 6}
                className="px-4 py-1.5 text-sm bg-green-600 hover:bg-green-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
              >
                {busy ? t(language, 'twoFactorSettings.verifying') : t(language, 'twoFactorSettings.confirmEnable')}
              </button>
              <button
                onClick={handleCancelEnroll}
                disabled={busy}
                className="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 disabled:bg-gray-700 rounded text-gray-200"
              >
                {t(language, 'twoFactorSettings.cancel')}
              </button>
            </div>
          </div>
        </div>
      )}

      {localError && (
        <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">⚠️ {localError}</p>
      )}
    </section>
  );
}
