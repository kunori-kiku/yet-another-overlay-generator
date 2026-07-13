// Controller panel (Mode B) store — the single source of truth for the controller connection +
// fleet view, independent of topologyStore. It is composed here from per-domain slice creators
// under ./controller/ behind ONE create()+persist; the slices were split out of a single giant
// create() literal as a pure structural refactor (no behavior change). The persistence allowlist
// (the zero-knowledge custody gate) lives as ONE auditable definition in ./controller/persist.ts —
// deliberately NOT fragmented across the slices.
//
// This module keeps the public store surface stable: the useControllerStore hook (with its full
// state + actions) and the exported selectors / design-diff helpers, so every consumer's imports
// keep working unchanged.

import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { ControllerState } from './controller/types';
import { createAuthSlice } from './controller/auth';
import { createFleetSlice } from './controller/fleet';
import { createDeploySlice } from './controller/deploy';
import { createKeystoneSlice } from './controller/keystone';
import { createSettingsSlice } from './controller/settings';
import { createSyncSlice } from './controller/sync';
import { PERSIST_NAME, partialize, merge } from './controller/persist';

// Re-export the exported design-diff helpers + selectors (moved to ./controller/helpers) so their
// consumer import path `stores/controllerStore` is unchanged.
export {
  canonicalDesign,
  isDesignDirty,
  selectLoggedIn,
  selectHasAuth,
  selectRekeyingCount,
  selectKeystoneStatusKnown,
  selectHasLocalSigningKey,
} from './controller/helpers';

export const useControllerStore = create<ControllerState>()(
  persist(
    (set, get) => ({
      ...createAuthSlice(set, get),
      ...createFleetSlice(set, get),
      ...createDeploySlice(set, get),
      ...createKeystoneSlice(set, get),
      ...createSettingsSlice(set, get),
      ...createSyncSlice(set, get),
    }),
    {
      name: PERSIST_NAME,
      partialize,
      merge,
    }
  )
);
