import { useEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';
import { CloseIcon } from './icons';
import { FOCUS_RING } from './styles';

// ─────────────────────────────────────────────────────────────────────────────
// Drawer — the CANONICAL off-canvas / slide-in overlay primitive for the whole
// Subject-2 (phone-UX) program. Every off-canvas surface (the mobile sidebar,
// future bottom-sheets) mounts on this one component, so focus-trap, Esc,
// backdrop-click, body-scroll-lock, route-change auto-close and the z-index
// contract are written ONCE and never re-derived per consumer.
//
// CONTROLLED: holds NO open-state of its own — the consumer owns `open` /
// `onClose`. (When `open` is false it renders null; mounting is the consumer's
// job, so a closed Drawer costs nothing.)
//
// Z-INDEX CONTRACT: backdrop `z-30` / panel `z-40`. Both sit strictly BELOW the
// existing `z-50` modal layer (the four ad-hoc role=dialog overlays) and below
// Shell.tsx's `focus:z-50` skip-link — so a confirm dialog or the keyboard
// skip-link always wins over an open Drawer.
//
// FUTURE `variant: 'centered'` (anti-debt): a focus-trapped, Esc/scroll-locked
// CENTERED modal is a small additive change to this primitive and is the
// migration target for the four pre-existing ad-hoc `role="dialog"` overlays
// that currently re-implement (incorrectly — no focus-trap/Esc/scroll-lock) the
// `fixed inset-0 z-50 grid place-items-center bg-black/50` shape:
//   - components/design/CanvasToolbar.tsx
//   - components/deploy/DeployBar.tsx
//   - components/deploy/AgentUpdateSettings.tsx
//   - components/pages/SettingsPage.tsx
// Recorded as tracked backlog; the `centered` variant is NOT built here.
// ─────────────────────────────────────────────────────────────────────────────

export type DrawerSide = 'left' | 'right' | 'bottom';

interface DrawerProps {
  /** Consumer-owned visibility. The Drawer holds no open-state of its own. */
  open: boolean;
  /** Called on Esc, backdrop-click, the close (X) button, and route change. */
  onClose: () => void;
  /** Which edge the panel anchors to (`bottom` is the Sheet shape). */
  side: DrawerSide;
  /** Accessible name for the dialog (the consumer's own label). */
  ariaLabel: string;
  /** Optional DOM id on the panel, for a trigger's aria-controls. */
  id?: string;
  children: ReactNode;
}

// Panel anchor + the off-screen translate it slides FROM. The slide is a CSS
// `transition-transform` (not a JS animation) so index.css's
// `prefers-reduced-motion: reduce` rule neutralizes it automatically.
const SIDE_CLASSES: Record<DrawerSide, { anchor: string; hidden: string }> = {
  left: { anchor: 'inset-y-0 left-0 h-full w-72 max-w-[85vw]', hidden: '-translate-x-full' },
  right: { anchor: 'inset-y-0 right-0 h-full w-72 max-w-[85vw]', hidden: 'translate-x-full' },
  bottom: { anchor: 'inset-x-0 bottom-0 max-h-[85vh] w-full', hidden: 'translate-y-full' },
};

// Focusable selector for the focus-trap (mirrors common a11y patterns).
const FOCUSABLE =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function Drawer({ open, onClose, side, ariaLabel, id, children }: DrawerProps) {
  const language = useTopologyStore((s) => s.language);
  const location = useLocation();
  const panelRef = useRef<HTMLDivElement>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  // Drives the slide-in: the panel mounts in its off-screen position, then
  // `entered` flips on the first frame so `transition-transform` animates it to
  // rest. Under prefers-reduced-motion the transition is neutralized by
  // index.css, so the panel simply appears in place (no jump).
  const [entered, setEntered] = useState(false);

  // Body-scroll-lock: toggle `overflow-hidden` on <body> while open. The cleanup
  // ALWAYS restores it — including when the Drawer unmounts while still open
  // (e.g. the route changed and the consumer stopped rendering us before the
  // close transition), so the lock can never strand a frozen page.
  useEffect(() => {
    if (!open) return;
    const { body } = document;
    const previous = body.style.overflow;
    body.style.overflow = 'hidden';
    return () => {
      body.style.overflow = previous;
    };
  }, [open]);

  // Flip `entered` after the open mount so the panel slides from off-screen to
  // rest; reset it when closed so the next open animates again.
  useEffect(() => {
    if (!open) {
      setEntered(false);
      return;
    }
    const rafId = requestAnimationFrame(() => setEntered(true));
    return () => cancelAnimationFrame(rafId);
  }, [open]);

  // Route-change auto-close: a navigation should never leave a stale overlay up.
  // The Drawer lives inside createBrowserRouter's tree, so useLocation is
  // available. Effect-guarded on `open` so it is a no-op when already closed.
  useEffect(() => {
    if (open) onClose();
    // Intentionally keyed on the pathname only — re-run when the route changes,
    // not when `open`/`onClose` identity changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.pathname]);

  // Focus management + key handling while open: capture the trigger, focus the
  // first focusable in the panel, trap Tab within it, close on Esc, and restore
  // focus to the trigger on close/unmount.
  useEffect(() => {
    if (!open) return;
    restoreFocusRef.current = document.activeElement as HTMLElement | null;

    const panel = panelRef.current;
    const focusables = panel?.querySelectorAll<HTMLElement>(FOCUSABLE);
    focusables?.[0]?.focus();

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab' || !panel) return;
      const items = panel.querySelectorAll<HTMLElement>(FOCUSABLE);
      if (items.length === 0) {
        // Nothing focusable inside — keep focus on the panel itself.
        event.preventDefault();
        panel.focus();
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      const activeEl = document.activeElement;
      if (event.shiftKey) {
        if (activeEl === first || !panel.contains(activeEl)) {
          event.preventDefault();
          last.focus();
        }
      } else if (activeEl === last || !panel.contains(activeEl)) {
        event.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('keydown', onKeyDown);
      restoreFocusRef.current?.focus();
    };
  }, [open, onClose]);

  if (!open) return null;

  const { anchor, hidden } = SIDE_CLASSES[side];
  const restingTranslate = side === 'bottom' ? 'translate-y-0' : 'translate-x-0';

  return (
    <div>
      {/* Backdrop — dims the page and closes on click. Strictly below the panel. */}
      <button
        type="button"
        aria-label={t(language, 'shell.dialogBackdrop')}
        tabIndex={-1}
        onClick={onClose}
        className="fixed inset-0 z-30 cursor-default bg-black/40"
      />
      <div
        ref={panelRef}
        id={id}
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        tabIndex={-1}
        className={`app-chrome fixed z-40 flex flex-col border-[var(--hairline)] shadow-2xl transition-transform duration-200 ease-[var(--ease-quiet)] outline-none ${anchor} ${
          entered ? restingTranslate : hidden
        } ${
          side === 'left' ? 'border-r' : side === 'right' ? 'border-l' : 'rounded-t-2xl border-t'
        }`}
      >
        <div className="flex justify-end p-2">
          <button
            type="button"
            onClick={onClose}
            aria-label={t(language, 'shell.closeDrawer')}
            className={`grid h-10 w-10 place-items-center rounded-lg text-[var(--content-muted)] transition-colors hover:bg-[var(--surface-sunken)] hover:text-[var(--content)] ${FOCUS_RING}`}
          >
            <CloseIcon />
          </button>
        </div>
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">{children}</div>
      </div>
    </div>
  );
}
