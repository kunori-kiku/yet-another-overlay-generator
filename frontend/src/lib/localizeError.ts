import { ControllerError } from '../api/controllerClient';
import { t, tError, type MessageKey, type UILanguage } from '../i18n';
import { WebAuthnError, type WebAuthnErrorKind } from './webauthn';

const webAuthnErrorKey: Record<WebAuthnErrorKind, MessageKey> = {
  unsupported: 'error.webauthn_unsupported_client',
  cancelled: 'error.webauthn_cancelled_client',
  'unsupported-algorithm': 'error.webauthn_unsupported_algorithm_client',
  'no-public-key': 'error.webauthn_no_public_key_client',
  'invalid-rp-id': 'error.webauthn_invalid_rp_id_client',
  'enrollment-verification-failed': 'error.webauthn_enrollment_verification_failed_client',
  failed: 'error.webauthn_failed_client',
};

// localizeError turns any caught error into an operator-facing, LANGUAGE-LOCALIZED string. A
// ControllerError carries the backend's coded error envelope, so tError localizes it through the
// 'error.<code>' catalog (no raw "<status> <JSON>" ever reaches the UI). WebAuthnError uses its
// stable, exhaustive kind-to-catalog mapping so browser/authenticator diagnostics are never shown
// raw; any other Error keeps its message and a non-Error falls back to the given catalog key.
//
// It is the single localizer shared by the controller store (which supplies the live language via
// getState()) AND the components that surface errors the store re-throws or never catches —
// TwoFactorSettings, PasskeySettings, EnrollmentFlow — so localization is uniform across both
// layers and no controller error renders raw to the operator (plan-5 DoD).
export function localizeError(
  err: unknown,
  lang: UILanguage,
  fallbackKey: MessageKey = 'error.generic'
): string {
  if (err instanceof ControllerError) return tError(err.body, lang);
  if (err instanceof WebAuthnError) {
    // WebAuthn errors carry useful raw diagnostics for logs, but UI copy comes only from the
    // stable typed kind. This keeps every browser/authenticator failure bilingual and avoids
    // leaking implementation-specific DOMException text into the operator surface.
    return t(lang, webAuthnErrorKey[err.kind]);
  }
  if (err instanceof Error && err.message) return err.message;
  return t(lang, fallbackKey);
}
