import { TwoFactorSettings } from '../deploy/TwoFactorSettings';
import { PasskeySettings } from '../deploy/PasskeySettings';
import { AuditLog } from '../deploy/AuditLog';
import { AuditView } from '../audit/AuditView';

// /security — operator account security (2FA, passkeys), the controller audit
// log, and the topology "Compile History" diff viewer (renamed from AuditView's
// label to avoid colliding with the controller audit log).
export function SecurityPage() {
  return (
    <div className="h-full overflow-y-auto bg-gray-900 text-gray-100 p-6 space-y-6">
      <TwoFactorSettings />
      <PasskeySettings />
      <AuditLog />
      <AuditView />
    </div>
  );
}
