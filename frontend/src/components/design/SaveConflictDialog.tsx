import { useEffect, useRef, useState } from 'react';
import { useControllerStore } from '../../stores/controllerStore';
import { t, type UILanguage } from '../../i18n';

// Shared by Design and Fleet telemetry: saveDesign writes one authoritative topology, so both
// surfaces must expose the same conflict choices instead of silently overwriting another editor.
export function SaveConflictDialog({ language }: { language: UILanguage }) {
  const saveConflict = useControllerStore((state) => state.saveConflict);
  return saveConflict ? <OpenSaveConflictDialog language={language} /> : null;
}

// Mount only while open so transient failure state and focus bookkeeping reset for each conflict.
// Native showModal supplies focus containment and background inerting; the fallback open attribute
// keeps the dialog usable in older/test DOMs that do not implement showModal.
function OpenSaveConflictDialog({ language }: { language: UILanguage }) {
  const saving = useControllerStore((state) => state.saving);
  const error = useControllerStore((state) => state.error);
  const dismissSaveConflict = useControllerStore((state) => state.dismissSaveConflict);
  const hydrateFromServer = useControllerStore((state) => state.hydrateFromServer);
  const saveDesign = useControllerStore((state) => state.saveDesign);
  const [resyncing, setResyncing] = useState(false);
  const [resyncFailed, setResyncFailed] = useState(false);
  const dialogRef = useRef<HTMLDialogElement>(null);
  const cancelRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    const priorFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (typeof dialog.showModal === 'function') dialog.showModal();
    else dialog.setAttribute('open', '');
    cancelRef.current?.focus();
    return () => {
      if (dialog.open && typeof dialog.close === 'function') dialog.close();
      else dialog.removeAttribute('open');
      priorFocus?.focus();
    };
  }, []);

  const dismiss = () => dismissSaveConflict();
  const resync = async () => {
    setResyncFailed(false);
    setResyncing(true);
    const ok = await hydrateFromServer({ reportError: true });
    if (ok) {
      dismissSaveConflict();
      return;
    }
    setResyncFailed(true);
    setResyncing(false);
  };

  return (
    <dialog
      ref={dialogRef}
      className="m-auto w-full max-w-md space-y-4 rounded-lg border border-[var(--warning-border)] bg-[var(--surface-elevated)] p-5 text-[var(--content)] shadow-xl backdrop:bg-black/50"
      aria-modal="true"
      aria-labelledby="save-conflict-title"
      onCancel={(event) => {
        event.preventDefault();
        dismiss();
      }}
    >
      <h4 id="save-conflict-title" className="text-base font-semibold text-[var(--warning)]">
        {t(language, 'canvasToolbar.saveConflictTitle')}
      </h4>
      <p className="text-sm text-[var(--content)]">
        {t(language, 'canvasToolbar.saveConflictBody')}
      </p>
      {resyncFailed && (
        <p role="alert" className="rounded border border-[var(--danger-border)] bg-[var(--danger-bg)] p-2 text-sm text-[var(--danger)]">
          {error || t(language, 'canvasToolbar.resyncFailed')}
        </p>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        <button
          ref={cancelRef}
          type="button"
          disabled={saving || resyncing}
          onClick={dismiss}
          className="rounded border border-[var(--hairline)] px-3 py-1.5 text-sm text-[var(--content)] hover:bg-[var(--control-hover)] disabled:opacity-40"
        >
          {t(language, 'canvasToolbar.cancel')}
        </button>
        <button
          type="button"
          disabled={saving || resyncing}
          onClick={() => void resync()}
          className="rounded border border-[var(--warning-border)] px-3 py-1.5 text-sm text-[var(--warning)] hover:bg-[var(--warning-bg)] disabled:opacity-40"
        >
          {resyncing
            ? t(language, 'canvasToolbar.resyncing')
            : t(language, 'canvasToolbar.resyncFromServer')}
        </button>
        <button
          type="button"
          disabled={saving || resyncing}
          onClick={() => void saveDesign({ force: true })}
          className="rounded bg-[var(--warning-solid)] px-3 py-1.5 text-sm font-medium text-[var(--warning-solid-fg)] hover:bg-[var(--warning-solid)] disabled:bg-[var(--control)] disabled:text-[var(--content-muted)]"
        >
          {t(language, 'canvasToolbar.overwriteServer')}
        </button>
      </div>
    </dialog>
  );
}
