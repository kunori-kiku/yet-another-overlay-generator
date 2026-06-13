import { useTopologyStore } from '../../stores/topologyStore';
import { FOCUS_RING } from './styles';

// Shared zh/EN segmented toggle. Extracted from the Topbar so the LoginPage (which
// renders before any chrome) reuses the exact same control — the markup, focus ring,
// and active/inactive token classes live in one place now (review: was duplicated).
export function LanguageToggle() {
  const language = useTopologyStore((s) => s.language);
  const setLanguage = useTopologyStore((s) => s.setLanguage);
  return (
    <div className="flex items-center overflow-hidden rounded-lg border border-[var(--hairline)]">
      <button
        type="button"
        onClick={() => setLanguage('zh')}
        className={`px-2 py-1 text-xs transition-colors ${FOCUS_RING} ${
          language === 'zh'
            ? 'bg-[var(--accent)] text-[var(--accent-fg)]'
            : 'text-[var(--content-muted)] hover:bg-[var(--surface-sunken)]'
        }`}
      >
        中文
      </button>
      <button
        type="button"
        onClick={() => setLanguage('en')}
        className={`px-2 py-1 text-xs transition-colors ${FOCUS_RING} ${
          language === 'en'
            ? 'bg-[var(--accent)] text-[var(--accent-fg)]'
            : 'text-[var(--content-muted)] hover:bg-[var(--surface-sunken)]'
        }`}
      >
        EN
      </button>
    </div>
  );
}
