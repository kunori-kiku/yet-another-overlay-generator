import { create } from 'zustand';
import { persist } from 'zustand/middleware';

// App-shell UI preferences. Kept in a dedicated store (and a dedicated
// `ui-storage` localStorage key) so chrome state stays orthogonal to the
// topology/controller domain stores. All fields are non-secret and additive.
export type ThemePref = 'system' | 'light' | 'dark';

interface UiState {
  /** Theme preference. `system` follows the OS via prefers-color-scheme. */
  theme: ThemePref;
  setTheme: (theme: ThemePref) => void;
  /** Cycle system → light → dark → system (drives the top-right toggle). */
  cycleTheme: () => void;
  /** Whether the left sidebar is collapsed to icon-only. Default expanded. */
  sidebarCollapsed: boolean;
  setSidebarCollapsed: (collapsed: boolean) => void;
  toggleSidebar: () => void;
}

export const useUiStore = create<UiState>()(
  persist(
    (set) => ({
      theme: 'system',
      setTheme: (theme) => set({ theme }),
      cycleTheme: () =>
        set((state) => ({
          theme:
            state.theme === 'system' ? 'light' : state.theme === 'light' ? 'dark' : 'system',
        })),
      sidebarCollapsed: false,
      setSidebarCollapsed: (sidebarCollapsed) => set({ sidebarCollapsed }),
      toggleSidebar: () => set((state) => ({ sidebarCollapsed: !state.sidebarCollapsed })),
    }),
    { name: 'ui-storage' },
  ),
);
