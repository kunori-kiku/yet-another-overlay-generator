import { useEffect } from 'react';
import type { ReactNode } from 'react';
import { useUiStore, type ThemePref } from '../stores/uiStore';

const DARK_QUERY = '(prefers-color-scheme: dark)';

/** Resolve a preference to a concrete boolean and toggle the `.dark` class.
 *  Client-only (runs from a React effect in this SPA) — no SSR half-guard. */
function applyTheme(theme: ThemePref) {
  const prefersDark = window.matchMedia(DARK_QUERY).matches;
  const dark = theme === 'dark' || (theme === 'system' && prefersDark);
  document.documentElement.classList.toggle('dark', dark);
}

/**
 * Owns appearance side effects on <html>: the `.dark` theme class and the
 * `.no-translucency` vibrancy class. The initial classes are set pre-paint by
 * the inline script in index.html (anti-FOUC); this keeps them in sync as the
 * user toggles, and live-tracks the OS when the theme preference is `system`.
 * Renders children untouched.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const theme = useUiStore((state) => state.theme);
  const translucency = useUiStore((state) => state.translucency);

  useEffect(() => {
    applyTheme(theme);
    if (theme !== 'system') return;
    const media = window.matchMedia(DARK_QUERY);
    const onChange = () => applyTheme('system');
    media.addEventListener('change', onChange);
    return () => media.removeEventListener('change', onChange);
  }, [theme]);

  useEffect(() => {
    // `.no-translucency` is consumed by the P6 vibrancy CSS to swap blur/opacity
    // for solid surfaces. Toggling it here makes the plumbing live now.
    document.documentElement.classList.toggle('no-translucency', !translucency);
  }, [translucency]);

  return <>{children}</>;
}
