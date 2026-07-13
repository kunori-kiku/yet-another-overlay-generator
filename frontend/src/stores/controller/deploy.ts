// Deploy slice: the deploy pipeline (compile preview, deploy preview dialog, the stage → keystone
// sign → promote deploy, the per-node force redeploy, and the shrink-confirm / strip-notice gates).
// Moved verbatim from the single controllerStore.ts create() literal; base64StdToBytes (used only
// on the signing path here) is co-located as a module-private helper.

import type { ControllerSet, ControllerGet } from './types';
import type { Topology } from '../../types/topology';
import type { DeployForceArg } from '../../lib/deployPreview';
import { configOf, localizeError, tLocal, canonicalDesign, sameIdSet, selectHasLocalSigningKey } from './helpers';
import {
  compilePreview as ctlCompilePreview,
  deployPreview as ctlDeployPreview,
  stage,
  promote,
  updateTopology,
  getTopology as ctlGetTopology,
  getTrustlist,
  postTrustlistSignature,
} from '../../api/controllerClient';
import { stripPrivateKeys } from '../../lib/custody';
import { signManifest } from '../../lib/webauthn';
import { useTopologyStore } from '../topologyStore';

// base64StdToBytes decodes STANDARD base64 (padded — the encoding of GET /trustlist's
// trustlist_json) back to raw bytes. Those bytes are the canonical manifest bytes, whose SHA-256
// is the WebAuthn challenge.
//
// FOOTGUN: the input MUST be STANDARD (padded) base64 because it pairs with Go's
// base64.StdEncoding on trustlist_json (handler_controller.go ~:1103) — this is a
// DIFFERENT dialect from the base64url (no-pad) used for every SignedTrustList
// field. atob() here requires the std alphabet + padding; if the Go side ever
// switches trustlist_json to base64url, this mis-decodes and the node rejects
// with ErrChallengeMismatch. Keep both sides on std base64 in lockstep.
function base64StdToBytes(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
}

export function createDeploySlice(set: ControllerSet, get: ControllerGet) {
  return {
    lastDeploy: null,
    deployPreview: null,
    deployPreviewing: false,
    deployPreviewError: null,
    lastStrippedKeys: 0,
    pendingShrink: null,
    previewing: false,

    // compilePreview (PR6): a server-authoritative read-only compile. See the interface comment.
    // previewing is its dedicated flag (not the global loading).
    compilePreview: async () => {
      set({ previewing: true, error: null });
      try {
        const cfg = configOf(get());
        // POST the current canvas (zero-knowledge fail-safe: strip private keys; the controller
        // canvas has none anyway).
        const current = useTopologyStore.getState().getTopology();
        const { topo: clean } = stripPrivateKeys(current);
        const resp = await ctlCompilePreview(cfg, JSON.stringify(clean));
        // No enrolled nodes → no rendered configs: clear the preview and give an actionable
        // hint, leaving the canvas alone.
        if (!resp.topology || !resp.wireguard_configs || Object.keys(resp.wireguard_configs).length === 0) {
          useTopologyStore.getState().setCompileResult(null);
          set({
            previewing: false,
            error: tLocal('controllerStore.noEnrolledNodes'),
          });
          return;
        }
        // Display the server-authoritative compile result (CompilePreview / EdgeEditor's
        // "actual post-compile values").
        useTopologyStore.getState().setCompileResult(resp);
        // Merge the server-computed allocations (compiled_port + all pinned_*) back onto the
        // canvas unconditionally (server-authoritative), so the operator immediately sees the
        // internal listen port/IP and configures NAT off it; then Save persists and Deploy
        // reuses them stickily.
        if (resp.topology.edges) {
          useTopologyStore.getState().mergeServerAllocations(resp.topology.edges);
        }
        set({ previewing: false });
      } catch (err) {
        set({ error: localizeError(err, 'error.generic'), previewing: false });
      }
    },

    // openDeployPreview (plan-6): fetch the read-only dry-run and open the confirmation dialog.
    // Guarded so a re-click while previewing / deploying / dialog-open does not re-fetch. Live-only:
    // deployPreview is never persisted (see the partialize note).
    openDeployPreview: async () => {
      if (get().deployPreviewing || get().loading || get().deployPreview) return;
      // Clear any prior preview-error banner so a retry starts clean (do NOT guard on it — the
      // Deploy button must be able to re-attempt the preview after a failure).
      set({ deployPreviewing: true, error: null, deployPreviewError: null });
      try {
        // Preview EXACTLY the canvas a Deploy will push+stage: deploy() reads the same
        // getTopology(), strips private keys, then update-topology's it before staging — so we POST
        // the identically-stripped canvas here. Previewing the stored server design instead would
        // misreport the blast radius whenever the canvas has unsaved edits (the plan-6 defect). The
        // controller canvas is already key-free; the strip is the zero-knowledge fail-safe (mirrors
        // compilePreview / deploy()).
        const current = useTopologyStore.getState().getTopology();
        const { topo: clean } = stripPrivateKeys(current);
        const preview = await ctlDeployPreview(configOf(get()), JSON.stringify(clean));
        set({ deployPreview: preview, deployPreviewing: false });
      } catch (err) {
        // Best-effort (plan-6): the preview endpoint may be unavailable (a newer panel POSTs the
        // deploy-preview route to an OLDER controller — 405 GET-only / 404 no route). Record the
        // failure in deployPreviewError (NOT the global `error`) so the DeployBar surfaces it beside
        // a "Deploy anyway" fallback, rather than leaving Deploy permanently dead.
        set({ deployPreviewError: localizeError(err, 'error.generic'), deployPreviewing: false });
      }
    },

    cancelDeployPreview: () => set({ deployPreview: null, deployPreviewError: null }),

    // forceRedeployNode (plan-6): re-stage ONE node even if unchanged, then the usual promote path.
    // It reuses deploy() with force_nodes:[nodeId] (so the keystone sign step is not reimplemented);
    // the current server-authoritative canvas is what gets deployed. Any error surfaces in `error`.
    forceRedeployNode: async (nodeId: string) => {
      await get().deploy({ force: { forceNodes: [nodeId] } });
    },

    deploy: async (opts?: { confirmedShrink?: boolean; force?: DeployForceArg }) => {
      // Idempotency guard (plan-16 / 3.4): a deploy is a multi-request fleet mutation
      // (update-topology → stage → sign → promote). The Deploy button is disabled while
      // loading, but a re-entrant programmatic/synthetic invocation would otherwise re-enter and
      // double-POST. Drop a call that arrives while a deploy is already in flight. A confirmed-
      // shrink re-call is unaffected: the shrink-confirm branch sets loading:false before
      // returning, so deploy({confirmedShrink:true}) runs with loading already cleared.
      if (get().loading) return;
      // Clear the preview-error banner too: a deploy (whether from the dialog Confirm or the
      // "Deploy anyway" fallback) supersedes any stale preview-fetch failure.
      set({ loading: true, error: null, deployPreviewError: null });
      try {
        const cfg = configOf(get());

        // Resolve the design to upload + its stripped-key count. On a confirmed
        // shrink we deploy the SNAPSHOT the warning was computed from (binds the
        // confirmation to what the operator actually saw, not a since-changed
        // canvas); otherwise we strip the live canvas now.
        let cleanTopo: Topology;
        let stripped: number;
        // Force (plan-6): from opts on a fresh deploy, or carried in pendingShrink on a
        // confirmed-shrink continuation so the force the operator chose survives the shrink gate.
        let force: DeployForceArg | undefined;
        const confirming = opts?.confirmedShrink ? get().pendingShrink : null;
        if (confirming) {
          cleanTopo = confirming.snapshot;
          stripped = confirming.stripped;
          force = confirming.force;
        } else {
          force = opts?.force;
          // Custody strip (plan-5, D4): never send a private key to the server (the
          // client mirror of the server's update-topology 400). In controller mode
          // the hydrated design is already key-free, but a locally-compiled/imported
          // design could carry keys — this is the fail-safe.
          const local = useTopologyStore.getState().getTopology();
          const r = stripPrivateKeys(local);
          cleanTopo = r.topo;
          stripped = r.stripped;

          // Shrink/empty guard (plan-5): before mutating the server design, compare
          // the canvas against the server copy. Emptying it, or dropping ≥50% of the
          // server's node-IDs, requires a typed confirmation (the audit's
          // one-click-destruction scenario). Version history (plan-2) is the recovery
          // backstop; this is the prevention layer.
          //
          // The server read is best-effort: a 404 means no server topology yet (no
          // shrink possible), and ANY other read failure (5xx/timeout/CSRF) must NOT
          // abort an otherwise-valid deploy — we proceed and rely on version history,
          // rather than blocking a legitimate upload on a transient guard-read error.
          let server: Topology | null = null;
          try {
            server = (await ctlGetTopology(cfg)) as Topology | null;
          } catch {
            server = null; // guard read failed → skip the guard (history is the backstop)
          }
          if (server && Array.isArray(server.nodes) && server.nodes.length > 0) {
            const canvasIds = new Set(cleanTopo.nodes.map((n) => n.id));
            const dropped = server.nodes.filter((n) => !canvasIds.has(n.id)).length;
            const emptied = cleanTopo.nodes.length === 0;
            const majorityDropped = dropped / server.nodes.length >= 0.5;
            if (emptied || majorityDropped) {
              set({
                pendingShrink: {
                  serverNodeCount: server.nodes.length,
                  canvasNodeCount: cleanTopo.nodes.length,
                  // Never empty: an empty phrase would let an empty input match and
                  // one-click past the gate. Fall back to a typed sentinel.
                  confirmPhrase: cleanTopo.project.name || cleanTopo.project.id || 'DELETE',
                  snapshot: cleanTopo,
                  stripped,
                  force,
                },
                // Close the preview dialog: the typed-confirm shrink modal now takes over (both
                // render in DeployBar; the shrink gate is the stricter of the two).
                deployPreview: null,
                loading: false,
              });
              return; // wait for the typed confirmation, then deploy({confirmedShrink:true})
            }
          }
        }

        const topoJSON = JSON.stringify(cleanTopo);
        await updateTopology(cfg, topoJSON);
        // The design is now the server's authoritative copy. Mark the canvas server-held
        // (even if stage/promote later fails — it IS on the server now) so it stops
        // persisting at rest and is flushed on logout/gate. Without this, a design BUILT
        // locally then deployed (first-deploy: server was empty, so hydrate never set the
        // flag) would leave the live fleet's public IPs + SSH targets readable in
        // localStorage while logged out (review: first-deploy leak).
        //
        // plan-10 / T7: on a CONFIRMED-shrink deploy the UPLOADED design is `confirming
        // .snapshot` (what the warning was computed from), which may differ from the
        // since-edited live canvas. Flipping the flag on the live canvas would mislabel a
        // divergent canvas as server-held, so partialize/gate would later flush those
        // post-warning edits with no backup. Load the snapshot so the canvas equals what
        // was actually uploaded; otherwise just flip the flag (live canvas IS what we sent).
        // (loadTopology also resets compile history + selection — intentional here, the canvas
        // is being replaced by the uploaded snapshot; compile history is empty in controller
        // mode anyway since local compile is refused.)
        if (confirming) {
          useTopologyStore.getState().loadTopology(confirming.snapshot, true);
        } else {
          useTopologyStore.getState().setCanvasFromServer(true);
        }
        // Keep the sync baseline (dirty/conflict — plan-10 / T2) in step with what we just
        // persisted, so a freshly-deployed canvas reads as clean (not dirty). This is the
        // UNPINNED upload; the post-deploy reconciliation below re-bases it to the pinned
        // design once stage's allocation has been read back (and is the fallback baseline
        // if that read fails).
        set({ lastSyncedSnapshot: canonicalDesign(cleanTopo), lastSyncedTopology: cleanTopo });
        const result = await stage(cfg, force);
        // When there are no enrolled nodes, stage produces no bundle (staged is empty), and
        // promote would then return 409 ErrNoStagedBundle — that is not an error but "no node
        // has joined the network yet". Just show skippedUnenrolled and skip promote (and
        // signing too), so a normal situation is not rendered as an error.
        if (result.staged.length > 0) {
          // KEYSTONE: retrieve the manifest to sign. null = keystone OFF, promote directly.
          const toSign = await getTrustlist(cfg);
          if (toSign !== null) {
            // Signing-handle auto-recovery belt (plan-3): a fresh/cleared browser may hold no local
            // signing descriptor even though the controller HAS a credential pinned. Re-probe server
            // truth once on the deploy path — hydrateKeystoneStatus recovers the non-secret
            // descriptor into the empty local slots (WebAuthn only) — then re-read, so the operator
            // is not forced into a fleet-stranding re-pin just because this browser was cleared.
            // Best-effort: a probe failure must not mask the actionable precondition error below.
            if (!selectHasLocalSigningKey(get())) {
              try {
                await get().hydrateKeystoneStatus();
              } catch {
                // ignore — the precondition check below still fires with a clear message.
              }
            }
            const credentialId = get().operatorCredentialId;
            const alg = get().operatorCredentialAlg;
            const pem = get().operatorPublicKeyPEM;
            if (credentialId === null || alg === null || !pem) {
              // keystone is on (nodes require a signature) but this browser still holds no complete
              // signing descriptor (credential_id + alg + PEM). We are PROVABLY inside the
              // keystone-ON branch (toSign !== null ⇒ getTrustlist returned a staged manifest ⇒ a
              // credential is pinned), so the right message is "pinned but this browser couldn't
              // recover a browser-signable descriptor — connect the enrolling authenticator /
              // re-enroll on this device, NOT a re-pin". Discriminate on `!== false` (not truthy):
              // serverOperatorPinned is null on a fresh browser and the belt re-probe above is
              // best-effort, so a transient probe failure leaves it null — a truthy check would
              // then wrongly nudge toward a fleet-stranding re-pin (the very thing this fixes).
              // Only an impossible false (keystone off, yet toSign was non-null) takes the
              // enroll-here branch, kept as defense-in-depth.
              throw new Error(
                get().serverOperatorPinned !== false
                  ? tLocal('controllerStore.signingDescriptorUnrecovered')
                  : tLocal('controllerStore.noSigningKeyEnrolled'),
              );
            }
            // rpId must equal the value pinned at enroll time (nodes verify SHA256(rpid)==the
            // assertion's rpIdHash); an old record may lack rpId, so fall back to the current
            // hostname (which is what enroll used).
            const rpId = get().operatorRpId ?? window.location.hostname;
            // Decode the standard-base64 canonical bytes back to raw bytes: their SHA-256 is the
            // WebAuthn challenge (nodes compare base64url(SHA256(Canonical(manifest)))).
            const manifestBytes = base64StdToBytes(toSign.trustlistJson);
            set({ signing: true });
            let signed;
            try {
              signed = await signManifest(manifestBytes, credentialId, alg, rpId, pem);
            } finally {
              set({ signing: false });
            }
            // Before submitting the signature, re-check trustlist_json with the server's
            // substitution guard (echo back the exact standard-base64 bytes we just signed).
            await postTrustlistSignature(cfg, {
              trustlistJson: toSign.trustlistJson,
              signed,
            });
          }
          await promote(cfg);
        }
        // Post-deploy reconciliation (PR1): stage() ran CompileAndStage → persistAllocations,
        // which merged the freshly-allocated compiled_port + pinned_* (ports, transit IPs,
        // link-locals) BY EDGE ID into the STORED topology. Re-GET it and overlay those onto
        // the canvas so the operator immediately SEES the allocated internal port/IP — the
        // value a NAT port-forward must target — WITHOUT a full hydrate that would drop the
        // current selection / open EdgeEditor. Full hydrate only if the node/edge SET diverged
        // (a concurrent edit), where a field overlay would be wrong. Re-base the sync baseline
        // from the reconciled canvas so the freshly-pinned design reads clean (not dirty, and
        // no phantom save-conflict on the next edit). best-effort: a failed re-GET leaves the
        // canvas as the uploaded (unpinned) design — the pins are on the server and a later
        // Save adopts them (non-clobber) or a re-login hydrates them.
        try {
          const persisted = (await ctlGetTopology(cfg)) as Topology | null;
          if (persisted && Array.isArray(persisted.nodes) && Array.isArray(persisted.edges)) {
            const ts = useTopologyStore.getState();
            const canvas = ts.getTopology();
            if (sameIdSet(canvas.nodes, persisted.nodes) && sameIdSet(canvas.edges, persisted.edges)) {
              ts.mergeServerAllocations(persisted.edges);
            } else {
              ts.loadTopology(persisted, true);
            }
            const reconciled = useTopologyStore.getState().getTopology();
            set({ lastSyncedSnapshot: canonicalDesign(reconciled), lastSyncedTopology: reconciled });
          }
        } catch {
          // best-effort (see comment above) — never fail an otherwise-successful deploy on it.
        }
        // Clear any pending shrink-confirm (a confirmed deploy consumes it) + the preview dialog
        // (the deploy it authorized is done) and surface how many private keys were stripped before
        // upload (0 = no notice). lastDeploy now carries the unchanged (delta-skipped) set too.
        set({
          lastDeploy: result,
          loading: false,
          pendingShrink: null,
          deployPreview: null,
          lastStrippedKeys: stripped,
        });
        await get().refresh();
      } catch (err) {
        // Clear pendingShrink on failure too: a CONFIRMED-shrink deploy
        // (deploy({confirmedShrink:true})) that throws during update/stage/
        // promote/signature still has pendingShrink set (it is consumed only on
        // the SUCCESS path at the end of try). Leaving it set keeps the
        // full-screen shrink-confirm modal (DeployBar renders solely on
        // pendingShrink) stuck open over the error. Clearing it surfaces the
        // error in the deploy bar and lets the operator retry Deploy, which
        // re-evaluates the shrink guard against the current server state.
        set({
          error: localizeError(err, 'error.generic'),
          loading: false,
          signing: false,
          pendingShrink: null,
          // Close the preview dialog on failure too, so the error surfaces in the DeployBar
          // (unoccluded by the modal) and the operator can retry Deploy, which re-previews.
          deployPreview: null,
        });
      }
    },

    cancelShrinkConfirm: () => set({ pendingShrink: null }),
    dismissStripNotice: () => set({ lastStrippedKeys: 0 }),
  };
}
