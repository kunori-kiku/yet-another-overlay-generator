import { useEffect, useState } from 'react';
import { useControllerStore, selectLoggedIn } from '../stores/controllerStore';

// LIVE_POLL_MS: the opt-in auto-refresh cadence for the fleet views. Within the 15–30s band —
// frequent enough to watch a canary→applied (or self-update) transition, light enough not to hammer
// the controller.
export const LIVE_POLL_MS = 20000;

// useFleetLiveRefresh centralizes the fleet status-refresh behavior shared by /fleet (the list) and
// /fleet/nodes/:id (the detail page). Before beta.16 the detail page had NO refresh-on-mount and no
// poll — it rendered a frozen localStorage snapshot, so "Last Seen won't advance when I look later"
// even when the controller was current. This hook (1) refreshes on mount / when a session becomes
// available (server truth over the persisted advisory cache), and (2) runs an opt-in (default-OFF)
// "Live" poll that pauses while the tab is hidden and tears down on unmount AND on logout / leaving
// controller mode (loggedIn flips → cleanup), so no request leaks past the session gate. An immediate
// refresh fires on enable so the operator sees fresh state at once rather than waiting a full tick.
// Returns the Live toggle state for the caller's checkbox.
export function useFleetLiveRefresh(): { live: boolean; setLive: (v: boolean) => void } {
  const refresh = useControllerStore((s) => s.refresh);
  const loggedIn = useControllerStore(selectLoggedIn);
  const [live, setLive] = useState(false);

  useEffect(() => {
    if (loggedIn) void refresh();
  }, [loggedIn, refresh]);

  useEffect(() => {
    if (!live || !loggedIn) return;
    void refresh();
    const id = setInterval(() => {
      if (!document.hidden) void refresh();
    }, LIVE_POLL_MS);
    return () => clearInterval(id);
  }, [live, loggedIn, refresh]);

  return { live, setLive };
}
