import { useEffect, useRef, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectLoggedIn } from '../../stores/controllerStore';
import { t } from '../../i18n';
import { UserIcon } from './icons';
import { FOCUS_RING } from './styles';

// Top-right account menu (plan-4 fills the P1 placeholder): shows the signed-in
// operator identity + session expiry and hosts sign-out (moved here from the old
// ConnectionSettings login block). In local mode — or under break-glass, which is
// not a login — it states the mode instead.
export function UserMenu() {
  const language = useTopologyStore((s) => s.language);
  const mode = useControllerStore((s) => s.mode);
  const loggedIn = useControllerStore(selectLoggedIn);
  const operatorName = useControllerStore((s) => s.operatorName);
  const sessionExpiresAt = useControllerStore((s) => s.sessionExpiresAt);
  const controllerVersion = useControllerStore((s) => s.controllerVersion);
  const operatorToken = useControllerStore((s) => s.operatorToken);
  const logout = useControllerStore((s) => s.logout);
  const loading = useControllerStore((s) => s.loading);

  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const label = t(language, 'userMenuLabel');

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
        <div
          aria-label={label}
          className="absolute right-0 z-20 mt-2 w-64 rounded-xl border border-[var(--hairline)] bg-[var(--surface-elevated)] p-2 shadow-lg"
        >
          {mode !== 'controller' ? (
            <p className="px-2 py-1.5 text-sm text-[var(--content-muted)]">
              {t(language, 'userMenu.localModeNoSign')}
            </p>
          ) : loggedIn ? (
            <div className="space-y-2 px-2 py-1.5">
              <div>
                <p className="text-sm font-medium text-[var(--content)]">
                  {t(language, 'userMenu.signedInAs')}{' '}
                  <span className="font-mono">{operatorName ?? ''}</span>
                </p>
                {sessionExpiresAt && (
                  <p className="text-xs text-[var(--content-muted)]">
                    {t(language, 'userMenu.sessionUntil')}
                    {new Date(sessionExpiresAt).toLocaleString()}
                  </p>
                )}
              </div>
              <button
                type="button"
                onClick={() => {
                  setOpen(false);
                  void logout();
                }}
                disabled={loading}
                className={`w-full rounded-lg border border-[var(--hairline)] py-1.5 text-sm text-[var(--content)] transition-colors hover:bg-[var(--surface-sunken)] disabled:opacity-40 ${FOCUS_RING}`}
              >
                {t(language, 'userMenu.signOut')}
              </button>
            </div>
          ) : operatorToken !== '' ? (
            <p className="px-2 py-1.5 text-sm text-[var(--content-muted)]">
              {t(language, 'userMenu.usingABreakGlass')}
            </p>
          ) : (
            <p className="px-2 py-1.5 text-sm text-[var(--content-muted)]">
              {t(language, 'userMenu.notSignedIn')}
            </p>
          )}
          {/* Controller build version (plan-8): server truth from the /session + login probe.
              Rendered once here for any authed controller session (genuine login OR break-glass),
              so the identity-branch markup above stays a single source per case. */}
          {mode === 'controller' && controllerVersion !== '' && (
            <p className="mt-1 border-t border-[var(--hairline)] px-2 pt-1.5 text-xs text-[var(--content-muted)]">
              {t(language, 'shell.controllerVersion')}{' '}
              <span className="font-mono">{controllerVersion}</span>
            </p>
          )}
        </div>
      )}
    </div>
  );
}
