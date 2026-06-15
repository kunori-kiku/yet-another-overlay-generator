import { ControllerError } from '../api/controllerClient';
import { t, tError, type MessageKey, type UILanguage } from '../i18n';

// localizeError turns any caught error into an operator-facing, LANGUAGE-LOCALIZED string. A
// ControllerError carries the backend's coded error envelope, so tError localizes it through the
// 'error.<code>' catalog (no raw "<status> <JSON>" ever reaches the UI); any other Error keeps its
// browser-generated message; a non-Error falls back to the given catalog key.
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
  if (err instanceof Error && err.message) return err.message;
  return t(lang, fallbackKey);
}
