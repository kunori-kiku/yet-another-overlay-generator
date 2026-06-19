import { useEffect, useState } from 'react';

// The SINGLE canonical Subject-2 (phone-UX) responsive boundary. plan-10's
// Tailwind `lg:` utility classes and plan-12's read-only-canvas gate both key off
// the same 1024px / min-width line, so CSS and JS can never disagree about where
// "desktop" starts. Tailwind's default `lg` breakpoint is 1024px; this is a
// CSS-first config (no tailwind.config.js), so the literal lives here.
export const LG = '(min-width: 1024px)';

/**
 * Subscribe to a CSS media query and re-render on match changes.
 *
 * Mirrors the only existing matchMedia pattern in the codebase
 * (theme/ThemeProvider.tsx): the initial value is read SYNCHRONOUSLY from
 * `window.matchMedia(query).matches` so the first paint is already correct (no
 * flash), and a `useEffect` subscribes to `change` with a cleanup that removes
 * the listener. Client-only — this is an SPA with no SSR, so there is no
 * `typeof window` guard (same assumption as ThemeProvider).
 */
export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches);

  useEffect(() => {
    const media = window.matchMedia(query);
    // Subscribe only — setState fires from the listener callback (a subscription
    // update from an external system), never synchronously in the effect body.
    // The synchronous useState initializer already captured the correct value
    // for the current `query` (the Subject-2 boundary is a constant), so there
    // is no first-paint flash and no need to re-sync here.
    const onChange = (event: MediaQueryListEvent) => setMatches(event.matches);
    media.addEventListener('change', onChange);
    return () => media.removeEventListener('change', onChange);
  }, [query]);

  return matches;
}

/**
 * True at the `lg` breakpoint and up (≥1024px). Polarity is min-width /
 * isDesktop (matching plan-12); consumers needing "below lg" negate it
 * (`!useIsDesktop()`).
 */
export function useIsDesktop(): boolean {
  return useMediaQuery(LG);
}
