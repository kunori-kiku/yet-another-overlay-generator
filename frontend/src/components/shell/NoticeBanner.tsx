import { FOCUS_RING } from './styles';

// A dismissible, full-width status banner below the Topbar. Shared by the
// hydration-overwrite and import-placeholder notices (plan-5 review: they were
// duplicated). The message is passed in already-localized (callers use txt()) so the
// banner never freezes a language.
export function NoticeBanner({
  message,
  onDismiss,
  dismissLabel,
}: {
  message: string;
  onDismiss: () => void;
  dismissLabel: string;
}) {
  return (
    <div
      className="flex items-start justify-between gap-3 border-b border-[var(--hairline)] bg-[var(--surface-sunken)] px-4 py-2 text-sm text-[var(--content)]"
      role="status"
    >
      <span>{message}</span>
      <button
        type="button"
        onClick={onDismiss}
        aria-label={dismissLabel}
        className={`shrink-0 rounded px-2 text-[var(--content-muted)] hover:text-[var(--content)] ${FOCUS_RING}`}
      >
        ✕
      </button>
    </div>
  );
}
