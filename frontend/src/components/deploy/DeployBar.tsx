import { useState } from 'react';
import {
  useControllerStore,
  selectRekeyingCount,
  selectKeystoneStatusKnown,
  selectHasLocalSigningKey,
  selectHasAuth,
} from '../../stores/controllerStore';
import { useTopologyStore } from '../../stores/topologyStore';
import { t, type UILanguage } from '../../i18n';
import { BTN_CTA } from '../shell/styles';
import {
  emptyForceSelection,
  setForceAll,
  toggleForceNode,
  summarizeDeployPreview,
  deployPreviewRows,
  resolveDeployForce,
  type ForceSelection,
  type DeployPreview,
  type DeployForceArg,
} from '../../lib/deployPreview';

// DeployBar publishes the current topology to the fleet. controllerStore.deploy() chains
// update-topology → stage → (KEYSTONE signing) → promote → refresh, the whole promote-to-fleet flow.
// It triggers the action, echoes the result (staged / skippedUnenrolled / generation) + errors, and
// provides the enrollment entry point for the off-host operator signing key (passkey / YubiKey) plus
// the "touch your security key" prompt.
// The KEYSTONE signing hook (plan-5.1d) lives inside the store, after stage and before promote (only
// when a node requires signing).
export function DeployBar() {
  const language = useTopologyStore((s) => s.language);

  const deploy = useControllerStore((s) => s.deploy);
  const rollKeys = useControllerStore((s) => s.rollKeys);
  // Pre-deploy preview (plan-6): Deploy now opens a dry-run confirmation dialog before deploying.
  const openDeployPreview = useControllerStore((s) => s.openDeployPreview);
  const cancelDeployPreview = useControllerStore((s) => s.cancelDeployPreview);
  const deployPreview = useControllerStore((s) => s.deployPreview);
  const deployPreviewing = useControllerStore((s) => s.deployPreviewing);
  // Best-effort preview (plan-6): non-null when the dry-run fetch failed (e.g. an older controller
  // 404s/405s the POST route). We then show the error + a "Deploy anyway" fallback rather than a dead
  // Deploy button.
  const deployPreviewError = useControllerStore((s) => s.deployPreviewError);
  const enrollOperator = useControllerStore((s) => s.enrollOperator);
  const loading = useControllerStore((s) => s.loading);
  const signing = useControllerStore((s) => s.signing);
  const enrolling = useControllerStore((s) => s.enrolling);
  const error = useControllerStore((s) => s.error);
  const lastDeploy = useControllerStore((s) => s.lastDeploy);
  // Keystone status is SERVER-authoritative (never the browser-local cache): null = checking,
  // true = a credential is pinned on the controller, false = none. This is what kills the false
  // "Not enrolled" a browser-data clear used to show (which invited a fleet-stranding re-pin).
  const serverOperatorPinned = useControllerStore((s) => s.serverOperatorPinned);
  const keystoneKnown = useControllerStore(selectKeystoneStatusKnown);
  const serverOperatorAlg = useControllerStore((s) => s.serverOperatorAlg);
  const serverOperatorFingerprint = useControllerStore((s) => s.serverOperatorFingerprint);
  const serverRedeployRequired = useControllerStore((s) => s.serverRedeployRequired);
  // A credential can be pinned on the server yet ABSENT from this browser (enrolled on another
  // device / after a browser-data clear) — then the operator must sign on the enrolling device.
  const hasLocalSigningKey = useControllerStore(selectHasLocalSigningKey);
  // Pending rotate confirmation: arming this (instead of starting the ceremony) is how a re-pin of
  // an already-pinned keystone is gated behind an explicit acknowledgement.
  const pendingKeystoneRotate = useControllerStore((s) => s.pendingKeystoneRotate);
  const cancelKeystoneRotate = useControllerStore((s) => s.cancelKeystoneRotate);
  // Post-deploy orphan list (plan-6): approved nodes still in the fleet registry but not in the
  // generation that was just published.
  const ctlNodes = useControllerStore((s) => s.nodes);
  const revoke = useControllerStore((s) => s.revoke);
  // Shrink-deploy confirmation (plan-5) and the "stripped N private keys" notice.
  const pendingShrink = useControllerStore((s) => s.pendingShrink);
  const cancelShrinkConfirm = useControllerStore((s) => s.cancelShrinkConfirm);
  const lastStrippedKeys = useControllerStore((s) => s.lastStrippedKeys);
  const dismissStripNotice = useControllerStore((s) => s.dismissStripNotice);
  const [shrinkTyped, setShrinkTyped] = useState('');
  // Number of nodes still in rekey_requested: when >0 it drives Deploy's advisory confirm and notice
  // (see the note below).
  const rekeyingCount = useControllerStore(selectRekeyingCount);

  // With no session and no break-glass token, no operator request can be issued — disable the buttons
  // and explain. Use selectHasAuth (session || token); don't look at operatorToken alone, or a
  // logged-in operator would be wrongly blocked.
  const noAuth = !useControllerStore(selectHasAuth);

  // While nodes are still rekey_requested, Deploy is no longer hard-disabled (the backend never gated
  // on this flag): anyRekeying now only drives the "advisory" experience — the button title hint plus
  // the window.confirm in onDeploy (see the note below) — so a single straggling/offline node cannot
  // wedge the whole fleet's deploy.
  const anyRekeying = rekeyingCount > 0;

  // "Roll keys" is the fleet-wide key rotation of the plan-4.6 ROUTINE tier: it flags every approved
  // node for rekey, and each agent regenerates its own local WG private key and registers the new
  // public key (the controller never touches the private key). The operation is not single-click
  // — convergence requires another Deploy after the nodes re-register — so confirm before firing.
  const onRollKeys = () => {
    const ok = window.confirm(
      t(language, 'deployBar.thisRequestsAWireGuard'),
    );
    if (ok) {
      rollKeys();
    }
  };

  // Deploy is the step that COMPLETES a "Roll keys" rotation (it recompiles each node with its
  // CURRENT registered key). We do NOT hard-block it while nodes still owe a rotation — the backend
  // never gated on the flag, a mixed old/new-key deploy is consistent (each node is compiled with
  // whatever key the registry holds), and a single stuck/offline straggler must not wedge every
  // deploy. Instead, an advisory confirm: a straggler deploys with its OLD key (it re-rotates and
  // needs another deploy, or use "Cancel rekey" in the registry to release a node that will never
  // re-register). With no rekey pending, Deploy fires directly.
  const onDeploy = () => {
    if (anyRekeying) {
      const ok = window.confirm(
        t(language, 'deployBar.deployWhileRekeyingConfirm', { count: rekeyingCount }),
      );
      if (!ok) return;
    }
    // Open the pre-deploy preview dialog (plan-6): fetch the dry-run, then the operator reviews the
    // changed/unchanged split + picks any Force and confirms. The actual deploy fires from the dialog.
    void openDeployPreview();
  };

  // "Deploy anyway" fallback (plan-6 best-effort): the preview fetch failed, so the confirmation
  // dialog never opened. Deploy directly (no preview, no Force — a plain delta deploy) so the operator
  // is never stuck. It still honors the same advisory rekeying confirm as onDeploy.
  const onDeployAnyway = () => {
    if (anyRekeying) {
      const ok = window.confirm(
        t(language, 'deployBar.deployWhileRekeyingConfirm', { count: rekeyingCount }),
      );
      if (!ok) return;
    }
    void deploy();
  };

  // Orphans (plan-6): approved fleet nodes that were NOT in the just-promoted
  // generation. Computed against lastDeploy.staged — the node-ids actually deployed —
  // NOT the live canvas (which can drift from what was promoted after a local edit;
  // plan-6 review). They still hold a valid token and poll, but this deploy didn't
  // include them. One-click manual revoke (never automatic — D10). Only meaningful
  // alongside lastDeploy, so the list renders inside that block.
  const deployedIds = new Set(lastDeploy?.staged ?? []);
  const orphans = lastDeploy
    ? ctlNodes.filter((n) => n.status === 'approved' && !deployedIds.has(n.nodeId))
    : [];

  return (
    <section className="bg-[var(--surface-elevated)] border border-[var(--hairline)] p-4 rounded-lg space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-lg font-semibold text-[var(--accent)]">
          {t(language, 'deployBar.deployToFleet')}
        </h3>
        <div className="flex items-center gap-2">
          <button
            data-testid="roll-keys"
            onClick={onRollKeys}
            disabled={loading || noAuth}
            className="px-4 py-2 text-sm bg-[var(--info-solid)] hover:bg-[var(--info-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--info-solid-fg)] font-medium"
          >
            {t(language, 'deployBar.rollKeys')}
          </button>
          <button
            data-testid="deploy"
            onClick={onDeploy}
            disabled={loading || noAuth || deployPreviewing}
            title={
              anyRekeying
                ? t(language, 'deployBar.rekeyingTitle', { count: rekeyingCount })
                : undefined
            }
            className={`px-4 py-2 text-sm ${BTN_CTA} disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded font-medium`}
          >
            {loading
              ? t(language, 'deployBar.deploying')
              : t(language, 'deployBar.deploy')}
          </button>
        </div>
      </div>

      <p className="text-sm text-[var(--content-muted)]">
        {t(language, 'deployBar.uploadTheCurrentTopology')}
      </p>

      <p className="text-xs text-[var(--info)]">
        {t(language, 'deployBar.rollKeysAsksEach')}
      </p>

      {/* KEYSTONE (plan-5.1d): the off-host operator signing key (passkey / YubiKey). The status is
          server-authoritative (serverOperatorPinned) and no longer relies on the browser-local cache
          — clearing browser data will not falsely report "not enrolled" anymore. When enrolled it
          shows the algorithm + fingerprint; rotation is a fleet-invalidating dangerous action, so it
          goes through an explicit confirm. */}
      <div className="p-3 bg-[var(--surface-sunken)] border border-[var(--hairline)] rounded space-y-2">
        <div className="flex items-center justify-between gap-2">
          <h4 className="text-sm font-semibold text-[var(--warning)]">
            {t(language, 'deployBar.operatorSigningKey')}
          </h4>
          {!keystoneKnown ? (
            <span className="text-xs text-[var(--content-muted)] bg-[var(--control)] px-2 py-0.5 rounded">
              {t(language, 'deployBar.keystoneChecking')}
            </span>
          ) : serverOperatorPinned ? (
            <span className="text-xs text-[var(--success)] bg-[var(--success-bg)] px-2 py-0.5 rounded font-mono">
              {t(language, 'deployBar.enrolled')}
              {serverOperatorAlg ? ` (${serverOperatorAlg})` : ''}
              {serverOperatorFingerprint ? ` · ${serverOperatorFingerprint.slice(0, 12)}` : ''}
            </span>
          ) : (
            <span className="text-xs text-[var(--content-muted)] bg-[var(--control)] px-2 py-0.5 rounded">
              {t(language, 'deployBar.notEnrolled')}
            </span>
          )}
        </div>
        <p className="text-xs text-[var(--content-muted)]">
          {t(language, 'deployBar.pinAnOffHost')}
        </p>

        {/* Rotated-but-not-redeployed: the served bundle is still signed under the OLD key, so every
            node is stranded until a fresh signed deploy lands. Surface it loudly. */}
        {serverRedeployRequired && (
          <p className="text-xs text-[var(--danger)] bg-[var(--danger-bg)] border border-[var(--danger-border)] px-2 py-1 rounded">
            {t(language, 'deployBar.keystoneRedeployRequired')}
          </p>
        )}

        {/* Pinned on the server but this browser has no local signing key (enrolled elsewhere / after
            a browser-data clear): you can't sign a deploy here — do it on the enrolling device. */}
        {serverOperatorPinned && !hasLocalSigningKey && (
          <p className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] border border-[var(--warning-border)] px-2 py-1 rounded">
            {t(language, 'deployBar.keystonePinnedNoLocalKey')}
          </p>
        )}

        {/* Pending rotate confirmation: rotating strands the fleet, so demand an explicit confirm. */}
        {pendingKeystoneRotate ? (
          <div className="space-y-2 border border-[var(--danger-border)] bg-[var(--danger-bg)] rounded p-2">
            <p className="text-xs text-[var(--danger)]">
              {t(language, 'deployBar.rotateKeystoneWarning')}
            </p>
            <div className="flex gap-2">
              <button
                onClick={() => enrollOperator({ rotate: true })}
                disabled={enrolling || loading || noAuth}
                className="px-3 py-2 text-xs bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--danger-solid-fg)] font-medium"
              >
                {t(language, 'deployBar.rotateKeystoneConfirm')}
              </button>
              <button
                onClick={() => cancelKeystoneRotate()}
                disabled={enrolling}
                className="px-3 py-2 text-xs bg-[var(--control)] hover:bg-[var(--control-hover)] rounded text-[var(--content)]"
              >
                {t(language, 'deployBar.cancel')}
              </button>
            </div>
          </div>
        ) : (
          <button
            onClick={() => enrollOperator()}
            disabled={enrolling || loading || noAuth || !keystoneKnown}
            className="px-4 py-2 text-sm bg-[var(--warning-solid)] hover:bg-[var(--warning-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--warning-solid-fg)] font-medium"
          >
            {enrolling
              ? t(language, 'deployBar.waitingForSecurityKey')
              : serverOperatorPinned
                ? t(language, 'deployBar.rotateKeystone')
                : t(language, 'deployBar.enrollSigningKeyPasskey')}
          </button>
        )}
        <p className="text-[10px] text-[var(--content-muted)]">
          {t(language, 'deployBar.whenTheKeystoneIs')}
        </p>
      </div>

      {/* A prominent prompt while the WebAuthn dialog is up, waiting for the user to touch the
          security key. The copy distinguishes the two ceremonies: enroll (registering the signing
          key, with no deploy in progress) versus deploy signing (authorizing this deploy). */}
      {(signing || enrolling) && (
        <p className="text-sm text-[var(--warning)] bg-[var(--warning-bg)] border border-[var(--warning-border)] px-3 py-2 rounded animate-pulse">
          {enrolling
            ? t(language, 'deployBar.touchYourSecurityKey')
            : t(language, 'deployBar.touchYourSecurityKey_2')}
        </p>
      )}

      {noAuth && (
        <p className="text-xs text-[var(--warning)] bg-[var(--warning-bg)] px-2 py-1 rounded">
          {t(language, 'deployBar.signInAboveFirst')}
        </p>
      )}

      {anyRekeying && (
        <p className="text-xs text-[var(--info)] bg-[var(--info-bg)] px-2 py-1 rounded">
          {t(language, 'deployBar.rekeyingBanner', { count: rekeyingCount })}
        </p>
      )}

      {error && (
        <p className="text-xs text-[var(--danger)] bg-[var(--danger-bg)] px-2 py-1 rounded break-all">
          ⚠️ {error}
        </p>
      )}

      {/* "Stripped N private keys" notice (plan-5, D4): controller mode is zero-knowledge, so private
          keys were stripped before upload. Dismissible. */}
      {lastStrippedKeys > 0 && (
        <div className="flex items-start justify-between gap-2 text-xs text-[var(--info)] bg-[var(--info-bg)] px-2 py-1 rounded">
          <span>
            {t(language, 'deployBar.strippedKeys', { count: lastStrippedKeys })}
          </span>
          <button
            type="button"
            onClick={dismissStripNotice}
            aria-label={t(language, 'deployBar.dismissNotice')}
            className="shrink-0 px-1 text-[var(--info)] hover:text-[var(--info)]"
          >
            ✕
          </button>
        </div>
      )}

      {lastDeploy && (
        <div className="p-3 bg-[var(--surface-sunken)] border border-[var(--hairline)] rounded space-y-2 text-sm">
          <p className="text-[var(--content)]">
            {t(language, 'deployBar.lastDeploy')} —{' '}
            <span className="font-mono text-[var(--info)]">
              {t(language, 'deployBar.generation')} {lastDeploy.generation}
            </span>
          </p>
          <div>
            <p className="text-xs text-[var(--content-muted)]">
              {t(language, 'deployBar.stagedNodes')} ({lastDeploy.staged.length})
            </p>
            {lastDeploy.staged.length === 0 ? (
              <p className="text-xs text-[var(--content-muted)] italic">{t(language, 'deployBar.none')}</p>
            ) : (
              <p className="text-xs text-[var(--success)] font-mono break-all">
                {lastDeploy.staged.join(', ')}
              </p>
            )}
          </div>
          {/* plan-6: the delta-skipped (unchanged) set — nodes that kept their generation. */}
          {lastDeploy.unchanged.length > 0 && (
            <div>
              <p className="text-xs text-[var(--content-muted)]">
                {t(language, 'deployBar.unchangedNodes')} ({lastDeploy.unchanged.length})
              </p>
              <p className="text-xs text-[var(--content-muted)] font-mono break-all">
                {lastDeploy.unchanged.join(', ')}
              </p>
            </div>
          )}
          {lastDeploy.skippedUnenrolled.length > 0 && (
            <div>
              <p className="text-xs text-[var(--content-muted)]">
                {t(language, 'deployBar.skippedUnenrolled')} (
                {lastDeploy.skippedUnenrolled.length})
              </p>
              <p className="text-xs text-[var(--warning)] font-mono break-all">
                {lastDeploy.skippedUnenrolled.join(', ')}
              </p>
            </div>
          )}
          {/* plan-6 identity reconciliation: nodes that are enrolled but not in this design. They
              were not deployed to, yet still hold a token and keep polling — offer a one-click
              "revoke" per row (manual only, never automatic, D10). */}
          {orphans.length > 0 && (
            <div>
              <p className="text-xs text-[var(--warning)]">
                {t(language, 'deployBar.enrolledButNotIn')} (
                {orphans.length})
              </p>
              <ul className="mt-1 space-y-1">
                {orphans.map((o) => (
                  <li key={o.nodeId} className="flex items-center justify-between gap-2 bg-[var(--warning-bg)] px-2 py-1 rounded">
                    <span className="text-xs text-[var(--warning)] font-mono break-all">{o.nodeId}</span>
                    <button
                      onClick={() => revoke(o.nodeId)}
                      disabled={loading}
                      className="shrink-0 px-3 py-2 text-xs bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded text-[var(--danger-solid-fg)]"
                    >
                      {t(language, 'deployBar.revoke')}
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}

      {/* Best-effort preview fallback (plan-6): the dry-run fetch failed (e.g. a newer panel POSTs the
          deploy-preview route to an older controller that 404s/405s it). The Deploy button only opens
          the preview dialog, so without this the operator could not deploy at all — surface the error
          and a "Deploy anyway" that deploys with no preview (a plain delta deploy). Dismissible. */}
      {deployPreviewError && (
        <div className="space-y-2 rounded border border-[var(--warning-border)] bg-[var(--warning-bg)] px-3 py-2">
          <div className="flex items-start justify-between gap-2 text-xs text-[var(--warning)]">
            <span className="break-all">
              {t(language, 'deployBar.previewUnavailable')} {deployPreviewError}
            </span>
            <button
              type="button"
              onClick={cancelDeployPreview}
              aria-label={t(language, 'deployBar.dismissNotice')}
              className="shrink-0 px-1 text-[var(--warning)] hover:text-[var(--warning)]"
            >
              ✕
            </button>
          </div>
          <button
            type="button"
            data-testid="deploy-anyway"
            onClick={onDeployAnyway}
            disabled={loading}
            className={`px-4 py-2 text-sm ${BTN_CTA} disabled:bg-[var(--control)] disabled:text-[var(--content-muted)] rounded font-medium`}
          >
            {loading ? t(language, 'deployBar.deploying') : t(language, 'deployBar.deployAnyway')}
          </button>
        </div>
      )}

      {/* Pre-deploy preview dialog (plan-6): a read-only dry-run of what a Deploy would do — the
          changed/unchanged split, a per-node Force checkbox (re-stage an unchanged node), and a
          fleet Force-all toggle. When a keystone rotation/first-pin pends, EVERY node re-stages, so
          the per-node force is moot and a rotation note replaces the counts. Confirm deploys with the
          chosen Force; the dialog stays up (showing "Deploying…") until the deploy settles. */}
      {deployPreview && (
        <DeployPreviewDialog
          preview={deployPreview}
          loading={loading}
          language={language}
          onCancel={cancelDeployPreview}
          onConfirm={(force) => void deploy({ force })}
        />
      )}

      {/* Type-to-confirm guard for a shrinking/emptying deploy (plan-5): this publish would sharply
          shrink the server-side design (empty it or drop more than half the nodes). It requires
          typing the project name to confirm, preventing a single misclick from overwriting the whole
          fleet design with an empty one (the audited "one-click destroy" scenario). Version history
          (plan-2) is the after-the-fact backstop; this guard is the up-front prevention. */}
      {pendingShrink && (
        <div className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4" role="dialog" aria-modal="true">
          <div className="w-full max-w-md space-y-4 rounded-lg border border-[var(--danger-border)] bg-[var(--surface-elevated)] p-5">
            <h4 className="text-base font-semibold text-[var(--danger)]">
              {t(language, 'deployBar.thisDeployShrinksThe')}
            </h4>
            <p className="text-sm text-[var(--content)]">
              {t(language, 'deployBar.shrinkSummary', {
                server: pendingShrink.serverNodeCount,
                canvas: pendingShrink.canvasNodeCount,
              })}
            </p>
            <p className="text-xs text-[var(--content-muted)]">
              {t(language, 'deployBar.shrinkConfirmPrompt', { phrase: pendingShrink.confirmPhrase })}
            </p>
            <input
              type="text"
              value={shrinkTyped}
              onChange={(e) => setShrinkTyped(e.target.value)}
              placeholder={pendingShrink.confirmPhrase}
              autoFocus
              className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--danger-border)] outline-none"
            />
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => {
                  setShrinkTyped('');
                  cancelShrinkConfirm();
                }}
                className="rounded border border-[var(--hairline)] px-3 py-2 text-sm text-[var(--content)] hover:bg-[var(--control-hover)]"
              >
                {t(language, 'deployBar.cancel')}
              </button>
              <button
                type="button"
                disabled={shrinkTyped !== pendingShrink.confirmPhrase || loading}
                onClick={() => {
                  setShrinkTyped('');
                  void deploy({ confirmedShrink: true });
                }}
                className="rounded bg-[var(--danger-solid)] px-3 py-2 text-sm font-medium text-[var(--danger-solid-fg)] hover:bg-[var(--danger-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)]"
              >
                {t(language, 'deployBar.confirmDeploy')}
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}

// DeployPreviewDialog is the plan-6 pre-deploy confirmation surface. It is mounted ONLY while a
// preview is open (DeployBar renders it conditionally on deployPreview), so its Force-selection
// state starts fresh on every open with no reset effect. It stays mounted through the in-flight
// deploy (the store clears the preview on completion), so a re-entrant Confirm hits deploy()'s
// in-flight guard rather than double-POSTing. All display text is theme-tokenized + i18n-keyed; the
// pure changed/unchanged/force logic lives in ../../lib/deployPreview.
function DeployPreviewDialog({
  preview,
  loading,
  language,
  onCancel,
  onConfirm,
}: {
  preview: DeployPreview;
  loading: boolean;
  language: UILanguage;
  onCancel: () => void;
  onConfirm: (force: DeployForceArg) => void;
}) {
  const [forceSel, setForceSel] = useState<ForceSelection>(emptyForceSelection());
  const rows = deployPreviewRows(preview, forceSel);
  const summary = summarizeDeployPreview(preview);

  return (
    <div
      className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4"
      role="dialog"
      aria-modal="true"
      data-testid="deploy-preview"
    >
      <div className="w-full max-w-lg space-y-4 rounded-lg border border-[var(--hairline)] bg-[var(--surface-elevated)] p-5">
        <h4 className="text-base font-semibold text-[var(--accent)]">
          {t(language, 'deployBar.reviewDeploy')}
        </h4>

        {preview.keystoneFullRestage ? (
          <p
            data-testid="deploy-keystone-restage"
            className="rounded border border-[var(--warning-border)] bg-[var(--warning-bg)] px-3 py-2 text-sm text-[var(--warning)]"
          >
            {t(language, 'deployBar.keystoneRestagePending')}
          </p>
        ) : (
          <>
            <p className="text-sm text-[var(--content)]">
              {t(language, 'deployBar.previewSummary', {
                changed: summary.changed,
                unchanged: summary.unchanged,
              })}
            </p>
            <label className="flex items-center gap-2 text-sm text-[var(--content)]">
              <input
                type="checkbox"
                data-testid="deploy-force-all"
                checked={forceSel.forceAll}
                onChange={(e) => setForceSel((s) => setForceAll(s, e.target.checked))}
              />
              {t(language, 'deployBar.forceAll')}
            </label>
          </>
        )}

        {rows.length === 0 ? (
          <p className="text-sm italic text-[var(--content-muted)]">
            {t(language, 'deployBar.previewNoNodes')}
          </p>
        ) : (
          <ul className="max-h-64 space-y-1 overflow-y-auto">
            {rows.map((r) => (
              <li
                key={r.nodeId}
                data-testid={`deploy-preview-node-${r.nodeId}`}
                className="flex items-center justify-between gap-2 rounded bg-[var(--surface-sunken)] px-2 py-1"
              >
                <span className="break-all font-mono text-xs text-[var(--content)]">{r.name}</span>
                <span className="flex shrink-0 items-center gap-2">
                  <span
                    className={`text-xs ${r.willStage ? 'text-[var(--success)]' : 'text-[var(--content-muted)]'}`}
                  >
                    {r.changed
                      ? t(language, 'deployBar.previewWillUpdate')
                      : t(language, 'deployBar.previewUnchanged')}
                  </span>
                  {r.forceable && (
                    <label className="flex items-center gap-1 text-xs text-[var(--content-muted)]">
                      <input
                        type="checkbox"
                        data-testid={`deploy-force-node-${r.nodeId}`}
                        checked={r.forced}
                        onChange={() => setForceSel((s) => toggleForceNode(s, r.nodeId))}
                      />
                      {t(language, 'deployBar.forceNode')}
                    </label>
                  )}
                </span>
              </li>
            ))}
          </ul>
        )}

        {preview.skippedUnenrolled.length > 0 && (
          <p className="text-xs text-[var(--warning)]">
            {t(language, 'deployBar.skippedUnenrolled')} ({preview.skippedUnenrolled.length}):{' '}
            <span className="break-all font-mono">{preview.skippedUnenrolled.join(', ')}</span>
          </p>
        )}

        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={onCancel}
            disabled={loading}
            className="rounded border border-[var(--hairline)] px-3 py-2 text-sm text-[var(--content)] hover:bg-[var(--control-hover)] disabled:text-[var(--content-muted)]"
          >
            {t(language, 'deployBar.cancel')}
          </button>
          <button
            type="button"
            data-testid="deploy-preview-confirm"
            onClick={() => onConfirm(resolveDeployForce(forceSel))}
            disabled={loading}
            className={`rounded px-3 py-2 text-sm font-medium ${BTN_CTA} disabled:bg-[var(--control)] disabled:text-[var(--content-muted)]`}
          >
            {loading ? t(language, 'deployBar.deploying') : t(language, 'deployBar.confirmDeploy')}
          </button>
        </div>
      </div>
    </div>
  );
}
