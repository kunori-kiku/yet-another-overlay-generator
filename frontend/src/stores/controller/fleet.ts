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
  // All fleet reads share one request generation. A foreground refresh started after an older Live
  // read makes that older response stale, so it cannot overwrite post-mutation truth. Background
  // reads never start while the global mutation/loading gate is already held.
  let fleetReadSequence = 0;
  let backgroundRead: { generation: number; request: Promise<boolean> } | null = null;

  const readFleet = async (foreground: boolean): Promise<boolean> => {
    if (!foreground && get().loading) return false;

    const requestSequence = ++fleetReadSequence;
    const context = captureControllerActionContext(get);
    const isCurrent = () =>
      requestSequence === fleetReadSequence && controllerActionContextIsCurrent(get, context);
    if (foreground) set({ loading: true, error: null });

    try {
      if (!foreground) {
        // Live is a lightweight observation loop. Audit can contain ten thousand entries and
        // Settings/keystone are administrative/bootstrap truth, so downloading all three every ten
        // seconds would make a simple telemetry refresh increasingly expensive and let an unrelated
        // slow endpoint stall the next completion-based tick.
        const nodes = await getNodes(context.config);
        if (!isCurrent()) return false;
        const completedAt = Date.now();
        set({ nodes, lastSyncedAt: completedAt, lastFleetSyncedAt: completedAt });
        return true;
      }

      const [nodes, audit] = await Promise.all([getNodes(context.config), getAudit(context.config)]);
      if (!isCurrent()) return false;
      const completedAt = Date.now();
      set({
        nodes,
        audit: audit.entries,
        auditVerified: audit.verified,
        loading: false,
        lastSyncedAt: completedAt,
        lastFleetSyncedAt: completedAt,
      });

      // Bootstrap settings are best-effort and do not invalidate an otherwise successful fleet
      // observation. In controller mode the server remains authoritative for translucency.
      try {
        const settings = await getSettings(context.config);
        if (!isCurrent()) return false;
        set({ settings });
        if (get().mode === 'controller') {
          useUiStore.getState().applyServerTranslucency(settings.translucency);
        }
      } catch {
        if (!isCurrent()) return false;
      }

      // Keystone status is likewise a best-effort read and owns its own stale-response guard.
      if (!isCurrent()) return false;
      await get().hydrateKeystoneStatus();
      return isCurrent();
    } catch (err) {
      if (!isCurrent()) return false;
      if (foreground) {
        set({
          error: localizeError(err, 'error.generic'),
          loading: false,
        });
        return false;
      }
      // Live/manual Fleet feedback is component-local. Do not clear or replace a mutation's global
      // error banner merely because an observability read failed.
      throw err;
    }
  };

  return {
    nodes: [],
    audit: [],
    auditVerified: false,
    lastFleetSyncedAt: null,

    // Foreground refresh retains the historical global loading/error behavior used by login,
    // connection setup, and mutation follow-ups.
    refresh: async () => {
      await readFleet(true);
    },

    // Fleet Live/manual observation is deliberately isolated from mutation UI state. It returns
    // false when skipped/superseded, throws on a current read failure, and never toggles or clears
    // the global loading/error pair that guards deploy/revoke/save workflows.
    refreshFleetView: () => {
      // The hook is single-flight per mounted route. This store-level join covers the short route-
      // transition window where /fleet and /fleet/nodes/:id hook instances can overlap, keeping one
      // authenticated node observation in flight for the whole panel. The generation key prevents a
      // new login/controller context from joining an old context's hanging request.
      const generation = get().authGeneration;
      if (backgroundRead?.generation === generation) return backgroundRead.request;
      const request = readFleet(false).finally(() => {
        if (backgroundRead?.request === request) backgroundRead = null;
      });
      backgroundRead = { generation, request };
      return request;
    },

    // Node resource + active-probe history read for the node-detail charts: wraps the client over the
    // current auth config and returns the parsed series. Live-only — no set()/persist (custody), no global
    // loading/error (the NodeResourceHistory card owns its own state); rethrows for local handling.
    fetchNodeHistory: async (nodeId: string, from: string, to: string, step?: string, options = {}) => {
      const context = captureControllerActionContext(get);
      const history = await ctlNodeHistory(context.config, nodeId, from, to, step, options).catch((err: unknown) => {
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
