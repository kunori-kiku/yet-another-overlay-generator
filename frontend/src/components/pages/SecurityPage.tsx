import { useControllerStore } from '../../stores/controllerStore';
import { TwoFactorSettings } from '../deploy/TwoFactorSettings';
import { PasskeySettings } from '../deploy/PasskeySettings';
import { AuditLog } from '../deploy/AuditLog';
import { AuditView } from '../audit/AuditView';
import { ControllerErrorBanner } from '../deploy/ControllerErrorBanner';

// /security — mode-split (plan-11 / T4+T5). The page is visible in both modes (nav.ts keeps it
// so the local "Compile History" isn't stranded), but its contents are mode-specific:
//   - controller: operator account security (2FA, passkeys) + the controller fleet audit log —
//     all controller-only constructs (no login / no fleet exists in local mode);
//   - local: the topology "Compile History" diff/exposure viewer (AuditView) — a local-only
//     construct fed by the air-gap compile, which is refused in controller mode.
export function SecurityPage() {
  const mode = useControllerStore((s) => s.mode);

  return (
    <div className="h-full overflow-y-auto bg-[var(--surface)] text-[var(--content)] p-3 sm:p-6 space-y-6">
      {mode === 'controller' ? (
        <>
          <ControllerErrorBanner />
          <TwoFactorSettings />
          <PasskeySettings />
          <AuditLog />
        </>
      ) : (
        <AuditView />
      )}
    </div>
  );
}
