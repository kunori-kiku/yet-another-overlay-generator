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
  /** Vibrancy/translucency on the shell chrome. Default on; off = solid surfaces
   *  ("plainer minimalism"). This is the EFFECTIVE value the ThemeProvider reads. In
   *  controller mode the server is the source of truth and drives it via
   *  applyServerTranslucency(); in local mode the user's setTranslucency() drives it. */
  translucency: boolean;
  /** The user's LOCAL preference, persisted independently of the controller server-pushed
   *  value (plan-10 / A3). setTranslucency() (local-mode toggle) updates it;
   *  applyServerTranslucency() (server push / controller-mode toggle) does NOT — so a
   *  controller→local switch can restore the local preference instead of inheriting the
   *  server's fleet appearance. */
  localTranslucency: boolean;
  /** Local-mode toggle: set the effective value AND remember it as the local preference. */
  setTranslucency: (on: boolean) => void;
  /** Server push (controller mode): set the effective value ONLY; leave the local pref intact. */
  applyServerTranslucency: (on: boolean) => void;
  /** controller→local boundary: revert the effective value to the local preference (A3). */
  restoreLocalTranslucency: () => void;
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
      translucency: true,
      localTranslucency: true,
      setTranslucency: (translucency) => set({ translucency, localTranslucency: translucency }),
      applyServerTranslucency: (translucency) => set({ translucency }),
      restoreLocalTranslucency: () => set((state) => ({ translucency: state.localTranslucency })),
    }),
    {
      name: 'ui-storage',
      // localTranslucency was added in plan-10 (A3). For users upgrading from a blob that
      // only had `translucency`, seed localTranslucency from it so their pre-upgrade local
      // preference survives the first load (otherwise it would default to true and a
      // translucency-OFF user could see it flip on after a controller round-trip).
      version: 1,
      migrate: (persisted, fromVersion) => {
        const p = (persisted ?? {}) as Partial<UiState>;
        if (fromVersion < 1 && p.localTranslucency === undefined && typeof p.translucency === 'boolean') {
          p.localTranslucency = p.translucency;
        }
        return p as UiState;
      },
      // Explicit allowlist: only non-secret UI prefs are persisted. Locks the
      // zero-knowledge custody invariant in for future fields added to this store.
      partialize: (state) => ({
        theme: state.theme,
        sidebarCollapsed: state.sidebarCollapsed,
        translucency: state.translucency,
        localTranslucency: state.localTranslucency,
      }),
    },
  ),
);
