import { useControllerStore } from '../../stores/controllerStore';

// ControllerErrorBanner surfaces the controller store's current error on pages that would otherwise
// swallow it (Fleet, Security — a failed revoke / refresh / 2FA / passkey op sets store.error, but
// those pages render none of the components that already show it). The error is already localized
// by the store (localizeError/tError, plan-5), and it clears on the next store action, so the
// banner needs no dismiss control.
export function ControllerErrorBanner() {
  const error = useControllerStore((s) => s.error);
  if (!error) return null;
  return (
    <p
      role="alert"
      className="text-sm text-red-300 bg-red-900/20 border border-red-800 px-3 py-2 rounded break-words"
    >
      ⚠️ {error}
    </p>
  );
}
