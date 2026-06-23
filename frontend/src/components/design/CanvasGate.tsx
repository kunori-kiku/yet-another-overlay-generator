import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useUiStore } from '../../stores/uiStore';
import { t } from '../../i18n';

// Small-screen (below lg) interstitial for the /design route. The full topology
// editor is desktop-shaped (w-56 create forms, drag-to-connect, w-80 aside editors)
// so editing is not offered on a phone; instead this gate explains that editing
// needs a larger screen and lets the operator drop into a READ-ONLY pan/zoom preview
// (DesignPage renders <TopologyCanvas editable={false} /> behind it).
//
// Presentational: the only state it owns is a session-only dismiss flag. Dismissing
// reveals the read-only canvas; it NEVER re-enables edit interactions (those are
// owned by TopologyCanvas's editable prop, which stays false below lg). Reuses the
// modal idiom + Design surface tokens from CanvasToolbar's saveConflict dialog.
export function CanvasGate() {
  const language = useTopologyStore((s) => s.language);
  // Read-only consumer of plan-11's shell-drawer state. Reading the setter does not
  // mutate the store's shape; "Open menu" opens the off-canvas nav so a phone operator
  // can navigate to a usable page. The gate is fully self-contained without it.
  const setMobileNavOpen = useUiStore((s) => s.setMobileNavOpen);
  const [dismissed, setDismissed] = useState(false);

  if (dismissed) {
    // Read-only preview is showing. Leave a quiet, non-blocking badge so it is always
    // obvious the canvas cannot be edited here.
    return (
      <div className="pointer-events-none absolute inset-x-0 top-0 z-40 flex justify-center p-3">
        <span className="pointer-events-auto rounded-full border border-[var(--hairline)] bg-[var(--surface-elevated)] px-3 py-1 text-xs text-[var(--content)] shadow">
          {t(language, 'canvasGate.readOnlyBadge')}
        </span>
      </div>
    );
  }

  return (
    <div
      className="absolute inset-0 z-50 grid place-items-center bg-black/50 p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby="canvas-gate-title"
    >
      <div className="w-full max-w-md space-y-4 rounded-lg border border-[var(--hairline)] bg-[var(--surface-elevated)] p-5">
        <h4 id="canvas-gate-title" className="text-base font-semibold text-[var(--content)]">
          {t(language, 'canvasGate.title')}
        </h4>
        <p className="text-sm text-[var(--content)]">{t(language, 'canvasGate.body')}</p>
        <div className="flex flex-wrap justify-end gap-2">
          <button
            type="button"
            onClick={() => {
              // Drop the gate scrim as we open the drawer: the z-50 gate would
              // otherwise occlude the z-40 off-canvas nav. Dismissing reveals the
              // read-only canvas (with its badge) under the sliding-in drawer —
              // the intended flow. Edit interactions stay disabled (editable=false).
              setDismissed(true);
              setMobileNavOpen(true);
            }}
            className="min-h-11 rounded border border-[var(--hairline)] px-4 py-2 text-sm text-[var(--content)] hover:bg-[var(--control-hover)]"
          >
            {t(language, 'canvasGate.openMenu')}
          </button>
          <button
            type="button"
            onClick={() => setDismissed(true)}
            className="min-h-11 rounded bg-[var(--accent)] px-4 py-2 text-sm text-[var(--accent-fg)] hover:bg-[var(--accent-hover)]"
          >
            {t(language, 'canvasGate.viewReadOnly')}
          </button>
        </div>
      </div>
    </div>
  );
}
