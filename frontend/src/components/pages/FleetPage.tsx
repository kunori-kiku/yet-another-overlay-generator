import { useEffect, useState } from 'react';
import { NodeRegistry } from '../deploy/NodeRegistry';
import { EnrollmentFlow } from '../deploy/EnrollmentFlow';
import { ControllerErrorBanner } from '../deploy/ControllerErrorBanner';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

// LIVE_POLL_MS: the opt-in auto-refresh cadence for the fleet view (plan-5). Within the 15–30s band
// — frequent enough to watch a canary→applied transition, light enough not to hammer the controller.
const LIVE_POLL_MS = 20000;

// /fleet — controller-managed node registry + enrollment flow. (Dark surface to
// match the existing components; full light/dark parity is P6.)
export function FleetPage() {
  const language = useTopologyStore((s) => s.language);
  const refresh = useControllerStore((s) => s.refresh);
  const loggedIn = useControllerStore(selectLoggedIn);
  // Opt-in (default OFF, per the outline post-flight decision): the operator turns on "Live" to
  // watch transitions; otherwise the view is a static snapshot they refresh on demand.
  const [live, setLive] = useState(false);

  // Opt-in live poll (plan-5): while enabled AND logged in, refresh on an interval so a
  // canary→applied transition appears without a manual reload. It PAUSES while the tab is hidden
  // (no point polling a backgrounded panel) and is torn down on unmount AND whenever the operator
  // logs out / leaves controller mode (loggedIn flips → the effect cleanup clears the interval),
  // so no request ever leaks past the session gate.
  useEffect(() => {
    if (!live || !loggedIn) return;
    const id = setInterval(() => {
      if (!document.hidden) void refresh();
    }, LIVE_POLL_MS);
    return () => clearInterval(id);
  }, [live, loggedIn, refresh]);

  return (
    <div className="h-full overflow-y-auto bg-gray-900 text-gray-100 p-6 space-y-6">
      <ControllerErrorBanner />
      <div className="flex justify-end">
        <label className="flex items-center gap-2 text-xs text-gray-400">
          <input type="checkbox" checked={live} onChange={(e) => setLive(e.target.checked)} />
          {t(language, 'updateStatus.live')}
        </label>
      </div>
      <NodeRegistry />
      <EnrollmentFlow />
    </div>
  );
}
