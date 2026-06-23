import type { BrowserContext } from '@playwright/test'

// seedStore.ts seeds the Zustand `controller-storage` persist entry BEFORE the panel loads,
// via context.addInitScript, so the store hydrates in the desired mode pointed at the
// OS-assigned :0 ports. It writes ONLY allowlist keys (controllerStore's partialize:
// mode/baseURL/agentBaseURL among them); the rest come from the store's own defaults, and
// the persist merge() shallow-merges this over the freshly-built initial state.
//
// The persist version is 0 (controller-storage declares no explicit version → Zustand's
// default). The shape is { state: <partial>, version: 0 } — the persist middleware's
// on-disk envelope.
//
// This is the documented seed contract any later Subject-3 spec reuses (see e2e/README.md):
// inject mode + endpoints here, never depend on the static default port (controllerStore's
// http://localhost:8080).

const STORAGE_KEY = 'controller-storage'

// seedControllerMode points the panel at the controller boot (controller mode + its panel
// and agent base URLs). The panel then gates on login (the Shell's controller-mode gate).
export async function seedControllerMode(
  context: BrowserContext,
  opts: { baseURL: string; agentBaseURL: string },
): Promise<void> {
  await context.addInitScript(
    ([key, baseURL, agentBaseURL]) => {
      localStorage.setItem(
        key,
        JSON.stringify({ state: { mode: 'controller', baseURL, agentBaseURL }, version: 0 }),
      )
    },
    [STORAGE_KEY, opts.baseURL, opts.agentBaseURL] as const,
  )
}

// seedTheme writes the `ui-storage` persist entry BEFORE navigation so the inline anti-FOUC script
// (index.html) adds the `dark` class on first paint and a snapshot does NOT race ThemeProvider's
// post-mount class application. The shape is `{ state: { theme }, version: 1 }` (uiStore's nested
// `.state.theme`, persist version 1 — distinct from controller-storage's version 0). Used by the
// visual-regression corpus (plan-17 / 3.5) to pin both light and dark surfaces deterministically.
export async function seedTheme(context: BrowserContext, theme: 'light' | 'dark'): Promise<void> {
  await context.addInitScript(
    ([key, t]) => {
      localStorage.setItem(key, JSON.stringify({ state: { theme: t }, version: 1 }))
    },
    ['ui-storage', theme] as const,
  )
}

// seedLocalMode forces local (no-controller) mode so the panel renders without the login
// gate — used by the air-gap design canary for order-independence (it must not depend on a
// prior spec having left local mode in storage).
export async function seedLocalMode(context: BrowserContext): Promise<void> {
  await context.addInitScript((key) => {
    localStorage.setItem(key, JSON.stringify({ state: { mode: 'local' }, version: 0 }))
  }, STORAGE_KEY)
}

// seedCanvasTopology seeds the `topology-storage` persist entry with a design BEFORE the panel
// loads, so the /design canvas hydrates a non-empty topology. The BottomBar Validate button is
// disabled until nodes.length > 0, so a spec that drives Validate must pre-load a design.
// canvasFromServer is false: this is the operator's OWN local design (not a confidential server
// mirror), so the controller-mode login gate preserves it instead of wiping it. The shape mirrors
// the topology store's partialize (topology-storage declares no persist version → default 0).
export async function seedCanvasTopology(
  context: BrowserContext,
  topology: { project: unknown; domains: unknown[]; nodes: unknown[]; edges: unknown[] },
): Promise<void> {
  await context.addInitScript(
    ([key, topo]) => {
      const t = topo as { project: unknown; domains: unknown[]; nodes: unknown[]; edges: unknown[] }
      localStorage.setItem(
        key,
        JSON.stringify({
          state: {
            project: t.project,
            domains: t.domains,
            nodes: t.nodes,
            edges: t.edges,
            allocSchemaVersion: 0,
            canvasFromServer: false,
            language: 'en',
            showInterfaces: false,
          },
          version: 0,
        }),
      )
    },
    ['topology-storage', topology] as const,
  )
}
