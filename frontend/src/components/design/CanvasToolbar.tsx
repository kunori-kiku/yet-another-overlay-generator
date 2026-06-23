import { useMemo } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, isDesignDirty } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { DomainForm } from '../domains/DomainForm';
import { NodeForm } from '../nodes/NodeForm';

// Canvas toolbar that replaces the docked LeftPanel: the [+ Domain] / [+ Node]
// create entry points (the self-contained collapsible forms), a toggle for the
// Domains & Nodes list drawer, and a mode-specific persist action — Compile in
// local mode, Save in controller mode. The canvas stays full-width by default —
// the list drawer is opt-in.
export function CanvasToolbar({
  listsOpen,
  onToggleLists,
}: {
  listsOpen: boolean;
  onToggleLists: () => void;
}) {
  const language = useTopologyStore((s) => s.language);
  const compile = useTopologyStore((s) => s.compile);
  const isCompiling = useTopologyStore((s) => s.isCompiling);
  const nodeCount = useTopologyStore((s) => s.nodes.length);
  // Local compile (POST /api/compile) generates/reconstructs WireGuard keys client-side and
  // requires private keys in the design. Controller mode is zero-knowledge: the hydrated design
  // is public-keys-only (the agents hold the private keys) and compilation happens SERVER-SIDE
  // during Deploy (stage/promote). So the air-gap Compile action is local-mode only — in
  // controller mode it would fail on every node ("pinned public key, no private key"). Deploy
  // is the controller-mode path; Save (below) persists a draft to the server without deploying.
  const mode = useControllerStore((s) => s.mode);

  // Controller-mode Save (plan-11 / T3): persists the design to the server's authoritative copy
  // (+ version history) via saveDesign() — NO stage/promote, the live fleet is untouched. This
  // is the controller-mode counterpart to local Compile. The Topbar import/export/flush cluster
  // is hidden in controller mode (it is local file-I/O), so this is the controller persist path.
  const saveDesign = useControllerStore((s) => s.saveDesign);
  // Controller-mode Compile (PR6): a SERVER-authoritative read-only compile — POSTs the current
  // design, the server renders the enrolled subgraph (zero-knowledge, placeholder keys), then the
  // store shows the result and merges the allocated ports/IPs onto the canvas so the operator can
  // see + adjust the NAT-relevant fields and Save. This is the controller counterpart to the
  // local air-gap Compile (which is unavailable in controller mode — see the mode note above).
  const compilePreview = useControllerStore((s) => s.compilePreview);
  const previewing = useControllerStore((s) => s.previewing);
  // save-scoped flag (not the global `loading`): so an unrelated in-flight controller op
  // (refresh/deploy/saveSettings) can't mislabel Save as "Saving…" or disable it (plan-11 review #1).
  const saving = useControllerStore((s) => s.saving);
  const saveConflict = useControllerStore((s) => s.saveConflict);
  const dismissSaveConflict = useControllerStore((s) => s.dismissSaveConflict);
  const hydrateFromServer = useControllerStore((s) => s.hydrateFromServer);
  const lastSyncedSnapshot = useControllerStore((s) => s.lastSyncedSnapshot);
  // Dirty = the canvas differs from the last server-synced baseline. Subscribe to the design
  // slices so this recomputes on every edit; memoize the (whole-design) canonicalization so the
  // toolbar's other re-renders (e.g. list toggle) don't repeat it.
  const project = useTopologyStore((s) => s.project);
  const domains = useTopologyStore((s) => s.domains);
  const nodes = useTopologyStore((s) => s.nodes);
  const edges = useTopologyStore((s) => s.edges);
  const allocSchemaVersion = useTopologyStore((s) => s.allocSchemaVersion);
  const getTopology = useTopologyStore((s) => s.getTopology);
  const dirty = useMemo(
    () => isDesignDirty(getTopology(), lastSyncedSnapshot),
    // getTopology reads the live slices; the slice deps drive recompute on edit.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [project, domains, nodes, edges, allocSchemaVersion, lastSyncedSnapshot, getTopology],
  );

  return (
    <div className="flex flex-wrap items-start gap-2 border-b border-[var(--hairline)] bg-[var(--surface-elevated)] px-3 py-2">
      <div className="w-56">
        <DomainForm />
      </div>
      <div className="w-56">
        <NodeForm />
      </div>
      <button
        type="button"
        onClick={onToggleLists}
        aria-pressed={listsOpen}
        aria-controls="design-lists-drawer"
        className={`h-8 rounded px-3 text-sm ${
          listsOpen ? 'bg-[var(--accent)] text-[var(--accent-fg)]' : 'bg-[var(--control)] text-[var(--content)] hover:bg-[var(--control-hover)]'
        }`}
      >
        <span aria-hidden="true">☰</span> {t(language, 'toolbarLists')}
      </button>
      <div className="flex-1" />
      {/* Compile is a LOCAL/air-gap action only — see the mode note above. In controller mode
          the user deploys from the Deploy page (server-side compile), so no Compile button here. */}
      {mode === 'local' && (
        <button
          type="button"
          onClick={() => compile()}
          disabled={isCompiling || nodeCount === 0}
          className="h-8 rounded bg-[var(--accent)] px-3 text-sm text-[var(--accent-fg)] hover:bg-[var(--accent-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)]"
        >
          {isCompiling ? t(language, 'canvasToolbar.compiling') : t(language, 'canvasToolbar.compile')}
        </button>
      )}
      {/* Controller-mode Compile (PR6): server-authoritative read-only compile + merge-to-canvas.
          Separate from local Compile (which reconstructs keys client-side and is local-only). */}
      {mode === 'controller' && (
        <button
          type="button"
          onClick={() => compilePreview()}
          disabled={previewing || nodeCount === 0}
          className="h-8 rounded bg-[var(--accent)] px-3 text-sm text-[var(--accent-fg)] hover:bg-[var(--accent-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)]"
        >
          {previewing ? t(language, 'canvasToolbar.compiling') : t(language, 'canvasToolbar.compile')}
        </button>
      )}
      {/* Save persists the draft to the server (controller mode). Disabled when nothing changed
          (not dirty) so a no-op save can't mint a redundant server version. */}
      {mode === 'controller' && (
        <button
          type="button"
          onClick={() => saveDesign()}
          disabled={saving || !dirty}
          title={dirty ? undefined : t(language, 'canvasToolbar.saveUpToDate')}
          className="h-8 rounded bg-[var(--accent)] px-3 text-sm text-[var(--accent-fg)] hover:bg-[var(--accent-hover)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)]"
        >
          {saving
            ? t(language, 'canvasToolbar.saving')
            : dirty
              ? t(language, 'canvasToolbar.save')
              : t(language, 'canvasToolbar.saved')}
        </button>
      )}

      {/* Save conflict (plan-10 / T2): the server design changed since we last synced. Offer a
          non-destructive re-sync (hydrateFromServer auto-backs-up divergent local work) or an
          explicit force-overwrite, instead of silently clobbering the other change. */}
      {saveConflict && (
        <div className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4" role="dialog" aria-modal="true">
          <div className="w-full max-w-md space-y-4 rounded-lg border border-[var(--warning-border)] bg-[var(--surface-elevated)] p-5">
            <h4 className="text-base font-semibold text-[var(--warning)]">
              {t(language, 'canvasToolbar.saveConflictTitle')}
            </h4>
            <p className="text-sm text-[var(--content)]">
              {t(language, 'canvasToolbar.saveConflictBody')}
            </p>
            <div className="flex flex-wrap justify-end gap-2">
              <button
                type="button"
                onClick={dismissSaveConflict}
                className="rounded border border-[var(--hairline)] px-3 py-1.5 text-sm text-[var(--content)] hover:bg-[var(--control-hover)]"
              >
                {t(language, 'canvasToolbar.cancel')}
              </button>
              <button
                type="button"
                disabled={saving}
                onClick={() => {
                  dismissSaveConflict();
                  void hydrateFromServer();
                }}
                className="rounded border border-[var(--warning-border)] px-3 py-1.5 text-sm text-[var(--warning)] hover:bg-[var(--warning-bg)] disabled:opacity-40"
              >
                {t(language, 'canvasToolbar.resyncFromServer')}
              </button>
              <button
                type="button"
                disabled={saving}
                onClick={() => void saveDesign({ force: true })}
                className="rounded bg-[var(--warning-solid)] px-3 py-1.5 text-sm font-medium text-[var(--warning-solid-fg)] hover:bg-[var(--warning-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)]"
              >
                {t(language, 'canvasToolbar.overwriteServer')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
