// Sync slice: workflow mode + the server-authoritative sync machinery (hydrate-from-server, the
// lightweight save, the server-authoritative import), the mode-boundary switches, and the global
// volatile flags (loading / error / sync baselines). Moved verbatim from the single
// controllerStore.ts create() literal.

import type { ControllerSet, ControllerGet, ControllerState } from './types';
import type { Topology } from '../../types/topology';
import {
  getTopology as ctlGetTopology,
  updateTopology,
} from '../../api/controllerClient';
import {
  captureControllerActionContext,
  controllerActionContextIsCurrent,
  localizeError,
  canonicalDesign,
  isDesignDirty,
  canonicalDesignIgnoringPins,
  loadSlices,
  stableStringify,
  clearServerCanvasAtGate,
} from './helpers';
import { stripPrivateKeys, dropAllKeys } from '../../lib/custody';
import { useTopologyStore } from '../topologyStore';
import { useUiStore } from '../uiStore';
import { localOnly } from '../../lib/localOnly';
import { t } from '../../i18n';

// Workflow-mode changes are controller-context boundaries even when the endpoint/session is
// intentionally preserved for a later round-trip. Cancel only transient controller work here;
// the dedicated switch functions below retain or purge topology/fleet state according to their
// existing custody policy.
const modeContextReset = {
  loading: false,
  saving: false,
  previewing: false,
  deployPreview: null,
  deployPreviewing: false,
  deployPreviewError: null,
  pendingShrink: null,
  signing: false,
  enrolling: false,
  loginCeremony: false,
  totpRequired: false,
  pendingLoginPasskeyEnrollment: null,
  pendingKeystoneEnrollment: null,
  pendingKeystoneRotate: false,
  error: null,
} as const satisfies Partial<ControllerState>;

export function createSyncSlice(set: ControllerSet, get: ControllerGet) {
  return {
    mode: 'local' as const,

    loading: false,
    error: null,
    lastSyncedAt: null,
    lastSyncedSnapshot: null,
    lastSyncedTopology: null,
    saveConflict: false,
    saving: false,
    hydrationNotice: false,

    // Route every real mode transition through the custody-aware switch functions below; this
    // prevents a programmatic setMode call from bypassing their canvas purge/context invalidation.
    // The static-local build remains locked because switchToController carries the load-bearing
    // localOnly guard.
    setMode: (mode: 'local' | 'controller') => {
      if (mode === get().mode) return;
      if (mode === 'controller') get().switchToController();
      else get().switchToLocal();
    },

    // Server-authoritative hydration (plan-4, D1): the server's topology is the sole authority,
    // and the local cache is just a discardable mirror — overwritten after each login/session
    // restore. On failure (network/parse) it keeps the local canvas and returns quietly:
    // hydration is a side action of login and a single fetch failure must not block login
    // itself; the next login/refresh retries. Callers resolving an explicit save conflict opt into
    // a visible error and use the boolean result so the modal closes only after a real hydration.
    hydrateFromServer: async (opts?: { reportError?: boolean }) => {
      const context = captureControllerActionContext(get);
      if (opts?.reportError) set({ error: null });
      try {
        const raw = await ctlGetTopology(context.config);
        if (!controllerActionContextIsCurrent(get, context)) return false;
        if (raw === null) {
          if (opts?.reportError) {
            set({ error: t(useTopologyStore.getState().language, 'canvasToolbar.resyncFailed') });
          }
          return false; // Server has no topology yet (before the first deploy): keep the local canvas.
        }
        const topo = raw as Topology;
        if (!topo || typeof topo !== 'object' || !topo.project || !topo.domains || !topo.nodes || !topo.edges) {
          if (opts?.reportError) {
            set({ error: t(useTopologyStore.getState().language, 'canvasToolbar.resyncFailed') });
          }
          return false; // Shape mismatch: do not overwrite (the server bytes are guaranteed by update-topology's custody gate; this is just defensive).
        }
        // Record the server-authoritative design's normalized snapshot — the baseline for the
        // dirty indicator + save conflict check (plan-10 / T2). Update it whether or not the
        // local canvas is actually overwritten below (differs/!differs), so the baseline always
        // equals the current server state.
        set({ lastSyncedSnapshot: canonicalDesign(topo), lastSyncedTopology: topo });
        const topoStore = useTopologyStore.getState();
        const local = topoStore.getTopology();
        // Semantic comparison (plan-4 review): compare only the four slices loadTopology
        // actually consumes + the version, and sort object keys before comparing, to avoid
        // false differences from the "server canonical key order" differing from the "frontend
        // getTopology key order" (preserving array order = conservative, prefer over-backup to
        // a miss).
        const differs = stableStringify(loadSlices(local)) !== stableStringify(loadSlices(topo));
        if (!differs) {
          return true; // Identical to the local copy: skip the overwrite (do not needlessly clear history/selection).
        }
        // Backup insurance (D9, review fix): whenever an overwrite would discard a local design
        // that is "non-empty and differs from the server", export a backup first — no longer
        // "once per browser" (which would silently drop undeployed local edits from the second
        // time on). In steady state differs=false, so this branch never fires and downloads do
        // not flood; only genuinely divergent undeployed changes are backed up, exactly the
        // scenario to protect. In controller mode "persist=deploy", so undeployed local changes
        // are volatile.
        const localHasWork = local.nodes.length > 0 || local.edges.length > 0;
        if (localHasWork) {
          const stamp = new Date().toISOString().slice(0, 10);
          topoStore.exportProject(`pre-hydration-backup-${stamp}.json`);
          set({ hydrationNotice: true });
        }
        // fromServer=true: this canvas is a server secret mirror, forbidden from hitting disk /
        // must be wiped after logout (see topologyStore.canvasFromServer's security invariant).
        topoStore.loadTopology(topo, true);
        return true;
      } catch (err) {
        if (!controllerActionContextIsCurrent(get, context)) return false;
        // Fetch failed: keep the local canvas, do not block login (see the function comment).
        if (opts?.reportError) {
          set({ error: localizeError(err, 'canvasToolbar.resyncFailed') });
        }
        return false;
      }
    },

    dismissHydrationNotice: () => set({ hydrationNotice: false }),

    // Controller-mode "save" (plan-10 / T2): persist the current canvas as the
    // server-authoritative copy (+ version history), but never stage/promote — the live fleet is
    // untouched. This is the lightweight persistence primitive missing beyond deploy(), so
    // undeployed in-progress work no longer lives only in a discardable mirror (lost on refresh
    // / logout).
    saveDesign: async (opts?: { force?: boolean }) => {
      // Idempotency guard (plan-16 / 3.4): the Save button is disabled while `saving`, but a
      // re-entrant programmatic/synthetic invocation would otherwise re-enter and double-write.
      // Drop a call that arrives while a save is already in flight (mirrors deploy()'s guard on
      // its own in-flight flag).
      if (get().saving) return;
      const context = captureControllerActionContext(get);
      // loading = the global busy flag (consistent with other actions); saving = specific to
      // this save, driving the Save button / conflict dialog, so the global loading set by an
      // unrelated op does not light it up (plan-11 review #1). Both must be cleared at every exit.
      set({ loading: true, saving: true, error: null });
      try {
        // Zero-knowledge fail-safe: as in deploy(), strip private keys before upload (the
        // controller canvas has none anyway; this is the backstop).
        const current = useTopologyStore.getState().getTopology();
        let { topo: clean, stripped } = stripPrivateKeys(current);
        // no-op guard: if the design equals the last sync baseline, return immediately — neither
        // sending a network request nor needlessly adding a same-content version history entry on
        // the server (the backend does no content dedup, and the extra version would also push
        // out a genuinely old one). force skips it. Use isDesignDirty: a null baseline with an
        // empty canvas (first time, nothing to save) is also correctly judged "not dirty".
        if (!opts?.force && !isDesignDirty(current, get().lastSyncedSnapshot)) {
          set({ loading: false, saving: false });
          return;
        }
        // Re-GET the server design — for BOTH the non-clobber pin merge and the conflict check.
        // best-effort: a read failure skips both guards and writes `clean` (update-topology has
        // no optimistic-concurrency token; version history — plan-2 — is the backstop). The read
        // runs on force too, so even a force-Save adopts server pins before it overwrites.
        let readOk = true;
        let serverNow: Topology | null = null;
        try {
          serverNow = (await ctlGetTopology(context.config)) as Topology | null;
          if (!controllerActionContextIsCurrent(get, context)) return;
        } catch {
          if (!controllerActionContextIsCurrent(get, context)) return;
          readOk = false;
        }
        // Client-side conflict detection (D13): unless force, ignore pin fields when comparing
        // "current server state vs. the last sync baseline". This way "a deploy elsewhere only
        // added pins" (pin-only difference, adopted by the non-clobber merge below) does not
        // falsely report a conflict, while a genuine concurrent change (nodes/edges/non-pin
        // fields) still triggers "re-sync / overwrite / cancel". It must be decided before the
        // merge below: a conflict returns early and does not touch the canvas — otherwise a
        // cancelled Save would still silently overlay the server pins onto the canvas, leaving a
        // third state the user did not ask for. best-effort: a failed guard read skips conflict
        // detection and saves as usual (consistent with the deploy shrink guard), avoiding false
        // reports on a transient network error; this save then blind-writes the overwrite
        // (update-topology has no backend optimistic concurrency, see D13).
        if (!opts?.force && readOk) {
          const baseTopo = get().lastSyncedTopology;
          const baseNoPins = baseTopo ? canonicalDesignIgnoringPins(baseTopo) : null;
          const serverNoPins = serverNow ? canonicalDesignIgnoringPins(serverNow) : null;
          if (serverNoPins !== baseNoPins) {
            set({ loading: false, saving: false, saveConflict: true });
            return;
          }
        }
        // Non-clobber pin adoption (PR1): if the server carries freshly-allocated NAT ports/IPs
        // (compiled_port + pinned_*) that NEITHER the canvas NOR the last-synced base had — a
        // deploy on another tab, or one whose post-promote reconcile failed — adopt them onto the
        // canvas so this Save does not drop them and break the configured NAT forward. Operator-set
        // / operator-unpinned values win (lastSyncedTopology is the 3-way base). Runs only PAST the
        // conflict gate above (and on force, which skips that gate), so a cancelled/conflicted Save
        // never mutates the canvas — this is also what makes even a force-Save non-clobbering.
        // Recompute `clean` from the merged canvas before writing.
        if (readOk && serverNow && Array.isArray(serverNow.edges)) {
          const base = get().lastSyncedTopology;
          const ts = useTopologyStore.getState();
          ts.mergeServerAllocations(serverNow.edges, base ? base.edges : []);
          const reread = stripPrivateKeys(ts.getTopology());
          clean = reread.topo;
          stripped = reread.stripped;
        }
        await updateTopology(context.config, JSON.stringify(clean));
        if (!controllerActionContextIsCurrent(get, context)) return;
        // The canvas is now the server-authoritative copy: mark it server-held (stop hitting
        // disk, wipe on logout/gate), refresh the sync snapshot (reset dirty) + the timestamp,
        // and record the stripped private-key count (0=no notice).
        useTopologyStore.getState().setCanvasFromServer(true);
        set({
          loading: false,
          saving: false,
          saveConflict: false,
          lastStrippedKeys: stripped,
          lastSyncedSnapshot: canonicalDesign(clean),
          lastSyncedTopology: clean,
          lastSyncedAt: Date.now(),
        });
      } catch (err) {
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({ error: localizeError(err, 'error.generic'), loading: false, saving: false });
      }
    },

    dismissSaveConflict: () => set({ saveConflict: false }),

    importDesignToServer: async (file: File) => {
      const context = captureControllerActionContext(get);
      set({ loading: true, error: null });
      try {
        const text = await file.text();
        if (!controllerActionContextIsCurrent(get, context)) return;
        let topo: Topology;
        try {
          topo = JSON.parse(text) as Topology;
        } catch {
          set({ error: t(useTopologyStore.getState().language, 'topbar.importParseError'), loading: false });
          return;
        }
        // Shape validation: must be a complete design (same criteria as importProject).
        // domains/nodes/edges must be arrays — use Array.isArray rather than a truthiness check,
        // or a {} or a number would slip past validation and throw a generic error at
        // dropAllKeys's .map rather than giving the precise "not a valid design" hint.
        if (
          !topo ||
          typeof topo !== 'object' ||
          !topo.project ||
          !Array.isArray(topo.domains) ||
          !Array.isArray(topo.nodes) ||
          !Array.isArray(topo.edges)
        ) {
          set({ error: t(useTopologyStore.getState().language, 'topbar.importShapeError'), loading: false });
          return;
        }
        // Reserved feature route_policies: strip it if non-empty (consistent with
        // importProject; semantic validation rejects a non-empty array).
        if (Array.isArray(topo.route_policies) && topo.route_policies.length > 0) {
          delete topo.route_policies;
        }
        // The controller is the key authority: drop all key material from the file (private keys
        // are never uploaded; public keys come from each agent enrollment), clearing it before
        // writing to the server so a private key never enters the request body even for an
        // instant.
        const { topo: cleaned } = dropAllKeys(topo);
        // Write to the server: update-topology lands a version history entry and heals colliding
        // pins + normalizes at the write boundary. Never stage/promote — an import only updates
        // the authoritative design; reaching the fleet still needs a separate Deploy.
        await updateTopology(context.config, JSON.stringify(cleaned));
        if (!controllerActionContextIsCurrent(get, context)) return;
        // Optimistic load (the already-healed imported design, fromServer=true so it does not
        // hit disk): even if the subsequent hydrateFromServer fails on a transient network
        // error, the canvas already reflects this import rather than staying on the pre-import
        // stale design (the POST already succeeded).
        useTopologyStore.getState().loadTopology(cleaned, true);
        // Then align the canvas and the sync baseline with the server-authoritative copy
        // (already healed + normalized); on success this overwrites the optimistic value above.
        await get().hydrateFromServer();
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({ loading: false });
      } catch (err) {
        if (!controllerActionContextIsCurrent(get, context)) return;
        set({ error: localizeError(err, 'error.generic'), loading: false });
      }
    },

    // Clear the controller-mode transient notices (hydration / stripping / pending shrink). Called
    // on a controller→local switch so no controller-mode banner lingers in local mode (plan-5
    // review).
    clearModeNotices: () => set({ hydrationNotice: false, lastStrippedKeys: 0, pendingShrink: null, deployPreview: null }),

    // The single controller→local switch path (plan-10 / T1). The security fork is identical to
    // the login gate LoginPage's:
    //   - canvas is a server secret mirror (canvasFromServer) → flushWorkspace wipes it whole
    //     (never let the fleet's public IPs / SSH targets linger in local / localStorage across
    //     the switch);
    //   - canvas is local original work → purgeModeBoundaryState keeps the design, drops
    //     keys/allocations/compile history (the D6 lossy switch).
    // SettingsPage previously called purgeModeBoundaryState only (no serverHeld fork), which
    // downgraded a secret mirror to persistable local data → fleet secrets in localStorage
    // (audit T1). Converged here, the two can no longer diverge. Also: restore the local
    // translucency preference (A3, the server-pushed value must not carry into local mode) +
    // clear the controller banners.
    switchToLocal: () => {
      const topo = useTopologyStore.getState();
      // A server mirror goes through clearServerCanvasAtGate (mode is still controller at this
      // moment): it exports a backup before flushing when the mirror is dirty, sharing the same
      // "back up unsaved changes before flushing" logic as the logout/session-loss paths
      // (plan-10 / T2). Local original work goes through the D6 lossy purge (keep the design,
      // drop keys/allocations/history).
      if (topo.canvasFromServer) clearServerCanvasAtGate(get().mode, get().lastSyncedSnapshot);
      else topo.purgeModeBoundaryState();
      get().clearModeNotices();
      useUiStore.getState().restoreLocalTranslucency();
      // Switching back to local: clear the controller-mode sync snapshot and conflict flag
      // (server-authoritative concepts).
      // About the fleet-view cache (nodes/audit/lastDeploy/lastSyncedAt/lastFleetSyncedAt): deliberately NOT
      // cleared here. It is a non-secret advisory cache (only nodeId/status/agentVersion/timestamps,
      // no keys, no design-level public IP/SSH), and in local mode the fleet/overview routes are
      // already redirected by the RequireControllerMode guard and will not display it (plan-11 /
      // T5's render-gate is the real fix). Clearing it would instead break the partialize-design
      // "instant coloring on re-entering controller": a session-preserved
      // controller→local→controller round-trip does not trigger refresh (checkSession only
      // hydrates, does not fetch the fleet), so an empty fleet would show until a manual refresh
      // (plan-11 review #2/#3). So the cache is preserved.
      set((state) => ({
        ...modeContextReset,
        mode: 'local',
        authGeneration: state.authGeneration + 1,
        lastSyncedSnapshot: null,
        lastSyncedTopology: null,
        saveConflict: false,
      }));
    },

    // local→controller switch. Deliberately NOT a blanket key purge: the asymmetry vs switchToLocal
    // is intentional (an accidental controller-click must not wipe a valid local design's private
    // keys — that data-loss footgun is why this path historically did nothing and let the login gate
    // + server hydration take over). Instead, clear ONLY the stranded pubkey-only nodes (public key
    // present, no private key) — useless in either mode and the source of the per-node un-pin chore
    // when a pubkey-only file was imported in local mode. Valid keypairs are untouched; once logged
    // in, server hydration replaces the canvas anyway. Notices are cleared for a clean controller entry.
    switchToController: () => {
      // Static-local-design build (VITE_LOCAL_ONLY): controller mode is unreachable — refuse
      // the switch (mirrors setMode's guard; the calling affordance is hidden there too).
      if (localOnly()) return;
      const ts = useTopologyStore.getState();
      ts.clearStrandedKeys();
      // Drop any LOCAL air-gap compile result on the way into controller mode (review should-fix #1).
      // A local compile carries reconstructed REAL private keys in compileResult, and the Deploy
      // page renders CompilePreview whenever compileResult is present — so a stale local result would
      // surface real private keys inside controller mode, the very boundary controller mode exists to
      // protect. compileResult is never persisted, so clearing it here closes the only in-memory path
      // (local compile → toggle to controller); a controller Compile re-populates it after entry with
      // placeholder-key configs.
      ts.setCompileResult(null);
      get().clearModeNotices();
      set((state) => ({
        ...modeContextReset,
        mode: 'controller',
        authGeneration: state.authGeneration + 1,
        lastSyncedSnapshot: null,
        lastSyncedTopology: null,
        saveConflict: false,
      }));
    },
  };
}
