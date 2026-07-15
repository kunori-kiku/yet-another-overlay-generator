import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// One shared statement of the enrollment-only UV policy. Both credential surfaces render this
// component so the compatibility rationale and attestation boundary cannot drift independently.
export function WebAuthnEnrollmentNotice() {
  const language = useTopologyStore((s) => s.language);
  return (
    <p className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] border border-[var(--warning-border)] px-2 py-1 rounded">
      {t(language, 'security.webAuthnEnrollmentWarning')}
    </p>
  );
}
