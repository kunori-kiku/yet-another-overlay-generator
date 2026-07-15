// Fleet slice: the fleet-view cache (nodes / audit) + the refresh, node-history read, enrollment
// token mint, and per-node lifecycle ops (revoke / roll-keys / clear-rekey / manual-node bundle).
// Moved verbatim from the single controllerStore.ts create() literal.

import type { ControllerSet, ControllerGet } from './types';
import {
  captureControllerActionContext,
  controllerActionContextIsCurrent,
  requireControllerActionContext,
  localizeError,
} from './helpers';
import {
  getNodes,
  getAudit,
  getSettings,
  nodeHistory as ctlNodeHistory,
  mintEnrollmentToken,
  revoke,
  rekeyAll,
  clearRekey,
  downloadManualNodeBundle,
} from '../../api/controllerClient';
import { useUiStore } from '../uiStore';
import { triggerBrowserDownload } from '../../lib/download';

export function createFleetSlice(set: ControllerSet, get: ControllerGet) {
  return {
    nodes: [],
    audit: [],
    auditVerified: false,

    // Refresh the fleet view: fetch nodes + audit + bootstrap settings in parallel. If any
    // fails, record the error and leave the existing view unchanged. A settings-fetch failure
    // does not affect nodes/audit (best-effort, caught separately).
    refresh: async () => {
      const context = captureControllerActionContext(get);
      set({ loading: true, error: null });
      try {
        const [nodes, audit] = await Promise.all([
          getNodes(context.config),
          getAudit(context.config),
        ]);
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({
          nodes,
          audit: audit.entries,
          auditVerified: audit.verified,
          loading: false,
          lastSyncedAt: Date.now(),
        });
        // Also refresh the bootstrap settings (does not block the fleet view; keep the old
        // value on failure). In controller mode the server is the authority for translucency,
        // so once fetched sync it to the appearance store (same as loadSettings), keeping the
        // settings-page checkbox from diverging from the server value.
        try {
          const settings = await getSettings(context.config);
          if (!controllerActionContextIsCurrent(get, context)) return;
          set({ settings });
          if (get().mode === 'controller') {
            useUiStore.getState().applyServerTranslucency(settings.translucency);
          }
        } catch {
          if (!controllerActionContextIsCurrent(get, context)) return;
          /* Settings fetch failed: keep the existing settings, do not overwrite the fleet view's success state. */
        }
        // Keystone status is server-authoritative (the panel's "enrolled" source); refresh it
        // alongside the fleet so the display + the rotated-but-not-redeployed banner stay current.
        // Best-effort (hydrateKeystoneStatus swallows its own errors).
        if (!controllerActionContextIsCurrent(get, context)) return;
        await get().hydrateKeystoneStatus();
      } catch (err) {
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({
          error: localizeError(err, 'error.generic'),
          loading: false,
        });
      }
    },

    // Node resource-history read for the node-detail charts: wraps the client over the current auth
    // config and returns the parsed series. Live-only — no set()/persist (custody), no global
    // loading/error (the NodeResourceHistory card owns its own state); rethrows for local handling.
    fetchNodeHistory: async (nodeId: string, from: string, to: string, step?: string) => {
      const context = captureControllerActionContext(get);
      const history = await ctlNodeHistory(context.config, nodeId, from, to, step).catch((err: unknown) => {
        requireControllerActionContext(get, context);
        throw err;
      });
      requireControllerActionContext(get, context);
      return history;
    },

    // Mint a one-time enrollment token for a node, returning the plaintext token (visible this
    // once only).
    mintToken: async (nodeId: string, ttl: number) => {
      const context = captureControllerActionContext(get);
      const result = await mintEnrollmentToken(context.config, nodeId, ttl).catch((err: unknown) => {
        requireControllerActionContext(get, context);
        throw err;
      });
      requireControllerActionContext(get, context);
      return result;
    },

    // Refresh the view after evicting a node.
    revoke: async (nodeId: string) => {
      // Idempotency guard (plan-16 / 3.4): drop a re-entrant revoke while one is in flight (the
      // Revoke button disables on `loading`, but a synthetic re-click bubbles past it).
      if (get().loading) return;
      const context = captureControllerActionContext(get);
      set({ loading: true, error: null });
      try {
        await revoke(context.config, nodeId);
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({ loading: false });
        await get().refresh();
      } catch (err) {
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({
          error: localizeError(err, 'error.generic'),
          loading: false,
        });
      }
    },

    // Request a WG key rotation for the whole fleet (plan-4.6 ROUTINE tier): mark every approved
    // node rekey_requested, then refresh the view (the registry shows a rekeying badge). This is
    // only the first step of the zero-knowledge rotation flow — each agent regenerates its own
    // key and registers the new public key via /rekey; once the nodes have re-registered, the
    // operator must Deploy again, and the new generation of configs carrying everyone's new
    // public keys converges the fleet.
    rollKeys: async () => {
      // Idempotency guard (plan-16 / 3.4): drop a re-entrant Roll-keys while one is in flight. The
      // Roll-keys button shares the SAME `loading`/disabled guard as Deploy, so the proven
      // synthetic-re-click-bubbles-past-disabled double-POST applies identically here.
      if (get().loading) return;
      const context = captureControllerActionContext(get);
      set({ loading: true, error: null });
      try {
        await rekeyAll(context.config);
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({ loading: false });
        await get().refresh();
      } catch (err) {
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({
          error: localizeError(err, 'error.generic'),
          loading: false,
        });
      }
    },

    // Clear a single node's pending-rotation flag without evicting it (unlike revoke: it
    // preserves the approval status and bearer token), then refresh the view so the rekeying
    // badge/count converges. Used to release a stuck "Roll keys" straggler (a dead/offline node,
    // or a mis-clicked full rotation) that would otherwise leave a persistent deploy warning.
    clearRekey: async (nodeId: string) => {
      const context = captureControllerActionContext(get);
      set({ loading: true, error: null });
      try {
        await clearRekey(context.config, nodeId);
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({ loading: false });
        await get().refresh();
      } catch (err) {
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({
          error: localizeError(err, 'error.generic'),
          loading: false,
        });
      }
    },
    // Download a MANUAL node's promoted, off-host-signed install bundle and trigger a browser
    // download. The bundle is the same served snapshot a managed node's agent pulls from /config,
    // carrying PRIVATEKEY_PLACEHOLDER (install.sh splices the on-box key) — so zero-knowledge holds.
    // A 404 (node not yet staged+promoted, or not manual) surfaces as a localized error.
    downloadManualNodeBundle: async (nodeId: string) => {
      const context = captureControllerActionContext(get);
      set({ loading: true, error: null });
      try {
        const { blob, filename } = await downloadManualNodeBundle(context.config, nodeId);
        if (!controllerActionContextIsCurrent(get, context)) return;
        triggerBrowserDownload(blob, filename);
        set({ loading: false });
      } catch (err) {
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({ error: localizeError(err, 'error.generic'), loading: false });
      }
    },
  };
}
