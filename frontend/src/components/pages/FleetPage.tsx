import { NodeRegistry } from '../deploy/NodeRegistry';
import { EnrollmentFlow } from '../deploy/EnrollmentFlow';
import { ControllerErrorBanner } from '../deploy/ControllerErrorBanner';
import { useTopologyStore } from '../../stores/topologyStore';
import { useFleetLiveRefresh } from '../../hooks/useFleetLiveRefresh';
import { t } from '../../i18n';

// /fleet — controller-managed node registry + enrollment flow. (Dark surface to
// match the existing components; full light/dark parity is P6.)
export function FleetPage() {
  const language = useTopologyStore((s) => s.language);
  // Refresh-on-mount/auth + the opt-in (default-OFF) Live poll, shared with the node-detail page.
  const { live, setLive } = useFleetLiveRefresh();

  return (
    <div className="h-full overflow-y-auto bg-[var(--surface)] text-[var(--content)] p-3 sm:p-6 space-y-6">
      <ControllerErrorBanner />
      <div className="flex justify-end">
        <label className="flex items-center gap-2 text-xs text-[var(--content-muted)]">
          <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} />
          {t(language, 'updateStatus.live')}
        </label>
      </div>
      <NodeRegistry />
      <EnrollmentFlow />
    </div>
  );
}
