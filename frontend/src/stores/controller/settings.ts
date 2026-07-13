// Settings slice: the server-persisted bootstrap settings (load / save) + the convenience
// release-pin / release-asset fetches the config cards use. Moved verbatim from the single
// controllerStore.ts create() literal.

import type { ControllerSet, ControllerGet } from './types';
import {
  getSettings,
  postSettings,
  fetchPins as ctlFetchPins,
  fetchReleaseAssets as ctlFetchReleaseAssets,
  type ControllerSettings,
  type AgentPinFetchRequest,
  type ReleaseAssetsRequest,
} from '../../api/controllerClient';
import { configOf, localizeError } from './helpers';
import { useUiStore } from '../uiStore';

export function createSettingsSlice(set: ControllerSet, get: ControllerGet) {
  return {
    settings: null,

    // Load the bootstrap settings (a standalone entry point, used on the settings area's first
    // render).
    loadSettings: async () => {
      try {
        const settings = await getSettings(configOf(get()));
        set({ settings });
        // In controller mode the server is the source of truth for translucency; apply
        // it to the appearance store as the EFFECTIVE value only (applyServerTranslucency
        // leaves the user's local preference intact, so a later controller→local switch
        // restores it rather than inheriting the fleet's appearance — plan-10 / A3). In
        // local mode the client uiStore value stands.
        if (get().mode === 'controller') {
          useUiStore.getState().applyServerTranslucency(settings.translucency);
        }
      } catch (err) {
        set({ error: localizeError(err, 'error.generic') });
      }
    },

    // Save the bootstrap settings: POST /settings, then write back the server-normalized value.
    saveSettings: async (s: ControllerSettings) => {
      set({ loading: true, error: null });
      try {
        const saved = await postSettings(configOf(get()), s);
        set({ settings: saved, loading: false });
        return null;
      } catch (err) {
        const msg = localizeError(err, 'error.generic');
        set({ error: msg, loading: false });
        return msg;
      }
    },

    // Convenience pin-fetch for the rollout/mimic config cards: wraps the client over the current
    // auth config and rethrows so the card localizes its own error. No global state side effects.
    fetchReleasePins: (body: AgentPinFetchRequest) => ctlFetchPins(configOf(get()), body),

    // Convenience asset-discovery for the mimic catalog card: wraps the client over the current
    // auth config and rethrows so the card localizes its own error. No global state side effects.
    fetchReleaseAssets: (body: ReleaseAssetsRequest) => ctlFetchReleaseAssets(configOf(get()), body),
  };
}
