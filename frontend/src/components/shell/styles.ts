// Shared class fragments for app-shell controls. Keeping the focus-visible ring
// in one place guarantees a consistent, token-based keyboard affordance across
// the sidebar, topbar, theme toggle, and account menu without restyling the
// legacy (non-shell) UI.
export const FOCUS_RING =
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]';

// BTN_CTA is the fill for the single PRIMARY call-to-action on a surface (Deploy, Compile). It uses
// the dedicated --cta family (vivid teal in BOTH themes) rather than --accent, whose dark value is a
// restrained graphite that reads grey on a button. Centralized so the handful of primary CTAs stay
// in lockstep. Status actions (Roll-keys/rotate/enroll) keep their own *-solid families.
export const BTN_CTA =
  'bg-[var(--cta)] hover:bg-[var(--cta-hover)] text-[var(--cta-fg)]';
