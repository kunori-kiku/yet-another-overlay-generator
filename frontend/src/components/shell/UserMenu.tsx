import { useEffect, useRef, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { txt, STRINGS } from '../../i18n';
import { UserIcon } from './icons';
import { FOCUS_RING } from './styles';

// Top-right account menu. A real click-outside popover primitive; its contents
// (login state, sign-in/out, language) are filled by later phases (P3/P5). For
// now it anchors the account affordance in the standard top-right position.
export function UserMenu() {
  const language = useTopologyStore((s) => s.language);
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const label = txt(language, ...STRINGS.userMenuLabel);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) setOpen(false);
    };
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onPointerDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onPointerDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  return (
    <div className="relative" ref={ref}>
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-haspopup="true"
        aria-expanded={open}
        aria-label={label}
        title={label}
        className={`inline-flex h-9 w-9 items-center justify-center rounded-lg text-[var(--content-muted)] transition-colors hover:bg-[var(--surface-sunken)] hover:text-[var(--content)] ${FOCUS_RING}`}
      >
        <UserIcon />
      </button>
      {open && (
        // Plain labelled popover for the P1 placeholder. P3 fills it with real
        // menu items and adds menu keyboard semantics (role="menu" + menuitems +
        // arrow-key navigation + focus management).
        <div
          aria-label={label}
          className="absolute right-0 z-20 mt-2 w-56 rounded-xl border border-[var(--hairline)] bg-[var(--surface-elevated)] p-2 shadow-lg"
        >
          <p className="px-2 py-1.5 text-sm text-[var(--content-muted)]">{label}</p>
        </div>
      )}
    </div>
  );
}
