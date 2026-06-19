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

// seedLocalMode forces local (no-controller) mode so the panel renders without the login
// gate — used by the air-gap design canary for order-independence (it must not depend on a
// prior spec having left local mode in storage).
export async function seedLocalMode(context: BrowserContext): Promise<void> {
  await context.addInitScript((key) => {
    localStorage.setItem(key, JSON.stringify({ state: { mode: 'local' }, version: 0 }))
  }, STORAGE_KEY)
}
