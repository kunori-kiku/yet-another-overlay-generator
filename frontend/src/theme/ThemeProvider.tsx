import { useEffect } from 'react';
import type { ReactNode } from 'react';
import { useUiStore, type ThemePref } from '../stores/uiStore';

const DARK_QUERY = '(prefers-color-scheme: dark)';

/** Resolve a preference to a concrete boolean and toggle the `.dark` class. */
function applyTheme(theme: ThemePref) {
  const prefersDark =
    typeof window !== 'undefined' && window.matchMedia(DARK_QUERY).matches;
  const dark = theme === 'dark' || (theme === 'system' && prefersDark);
  document.documentElement.classList.toggle('dark', dark);
}

/**
 * Owns the side effect of reflecting the theme preference onto <html>. The
 * initial class is set pre-paint by the inline script in index.html (anti-FOUC);
 * this keeps it in sync as the user toggles, and live-tracks the OS when the
 * preference is `system`. Renders children untouched.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const theme = useUiStore((state) => state.theme);

  useEffect(() => {
    applyTheme(theme);
    if (theme !== 'system') return;
    const media = window.matchMedia(DARK_QUERY);
    const onChange = () => applyTheme('system');
    media.addEventListener('change', onChange);
    return () => media.removeEventListener('change', onChange);
  }, [theme]);

  return <>{children}</>;
}
