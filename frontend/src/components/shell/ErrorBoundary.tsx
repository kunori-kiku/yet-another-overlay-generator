import { Component, type ErrorInfo, type ReactNode } from 'react';
import { t } from '../../i18n';
import { useTopologyStore } from '../../stores/topologyStore';

// ErrorBoundary catches render/lifecycle exceptions anywhere below it and shows a recoverable
// fallback (a reload affordance) instead of React's blank white screen — the panel-resilience half
// of plan-5. Error boundaries MUST be class components. The language is read from the store at
// render time (a hook cannot run in a class); the boundary is a terminal recovery screen, so it
// does not need to re-render on a language change.
interface State {
  error: Error | null;
}

export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Surface to the console for diagnosis; the UI stays recoverable (no white screen).
    console.error('Panel render error:', error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      const lang = useTopologyStore.getState().language;
      return (
        <div
          role="alert"
          className="flex min-h-screen flex-col items-center justify-center gap-4 bg-[var(--surface)] p-8 text-center text-[var(--content)]"
        >
          <h1 className="text-lg font-semibold">{t(lang, 'errorBoundary.title')}</h1>
          <p className="max-w-md text-sm text-[var(--content-muted)]">{t(lang, 'errorBoundary.body')}</p>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="rounded bg-[var(--accent)] px-4 py-2 text-sm text-[var(--accent-fg)] hover:bg-[var(--accent-hover)]"
          >
            {t(lang, 'errorBoundary.reload')}
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}
