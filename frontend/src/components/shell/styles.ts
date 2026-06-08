// Shared class fragments for app-shell controls. Keeping the focus-visible ring
// in one place guarantees a consistent, token-based keyboard affordance across
// the sidebar, topbar, theme toggle, and account menu without restyling the
// legacy (non-shell) UI.
export const FOCUS_RING =
  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]';
