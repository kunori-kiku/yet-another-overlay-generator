import { type Page, type Route } from '@playwright/test'

// faults.ts — the data-driven network-fault interceptor for the Engine A adversarial suite
// (plan-16 / 3.4, Phase 5). It registers ONE page.route over the controller operator API, counts
// every matched request by `${METHOD} ${route}`, and applies the first matching FaultRule. Faults
// key on the call COUNT (the Nth call to a route), never on timing — so the suite stays
// deterministic under the harness's retries:0 / workers:1 policy (a flake must red the gate).
//
// It matches the operator routes built by controllerClient.ts's ctlURL (`**/api/v1/operator/<route>`)
// and does NOT import controllerClient/controllerStore — the hard custody boundary (specs drive the
// UI + the wire only). Install AFTER any seeding (enroll/import) so those requests are not counted
// or faulted; the rule counts start fresh at the action under test.

// routeOf returns the operator route segment of a controller API URL — the part after
// /api/v1/operator/, trimmed at the first '?' or '/'. Returns '' for non-operator URLs. It
// distinguishes sibling routes exactly: 'trustlist' vs 'trustlist-signature'.
export function routeOf(url: string): string {
  const marker = '/api/v1/operator/'
  const i = url.indexOf(marker)
  if (i < 0) return ''
  const rest = url.slice(i + marker.length)
  return rest.split(/[?/]/, 1)[0]
}

export interface FaultRule {
  route: string // operator route segment, e.g. 'update-topology', 'trustlist', 'promote'
  method?: string // restrict to this HTTP method (default: any)
  nth?: number // 1-based: fault ONLY the Nth matching call (default: every matching call)
  after?: string // only fault once at least one request to this route segment has been seen
  status?: number // fulfill with this status (default 500); ignored when abort is set
  body?: string // response body for a fulfilled fault (default a coded-error JSON envelope)
  abort?: boolean // route.abort('failed') (a torn connection) instead of an HTTP status
  delayMs?: number // hold the request in-flight this long, then CONTINUE to the real server (no
  // fault) — used to keep an action's first request pending so a second submit can be probed
}

export interface FaultController {
  // count returns how many matched requests hit `route` (optionally restricted to `method`).
  count(route: string, method?: string): number
  // counts returns a snapshot of all `${METHOD} ${route}` tallies (for diagnostics).
  counts(): Record<string, number>
}

// installFaults wires the interceptor and returns a controller for asserting call counts. Requests
// matching no rule are passed through to the real controller (route.continue), so success paths use
// the real server and only the injected steps fail.
export async function installFaults(page: Page, rules: FaultRule[]): Promise<FaultController> {
  const tally: Record<string, number> = {}

  await page.route('**/api/v1/operator/**', async (route: Route) => {
    const req = route.request()
    const seg = routeOf(req.url())
    const method = req.method().toUpperCase()
    const key = `${method} ${seg}`
    tally[key] = (tally[key] ?? 0) + 1
    const n = tally[key]

    const rule = rules.find(
      (r) =>
        r.route === seg &&
        (r.method === undefined || r.method.toUpperCase() === method) &&
        (r.nth === undefined || r.nth === n) &&
        (r.after === undefined || (tally[`POST ${r.after}`] ?? tally[`GET ${r.after}`] ?? 0) > 0),
    )
    if (!rule) {
      await route.continue()
      return
    }
    if (rule.delayMs !== undefined) {
      // Hold the request pending, then let the REAL server handle it — keeps the action's first
      // request in-flight (loading=true) so a re-entrancy probe can fire during the window.
      await new Promise((resolve) => setTimeout(resolve, rule.delayMs))
      await route.continue()
      return
    }
    if (rule.abort) {
      await route.abort('failed')
      return
    }
    await route.fulfill({
      status: rule.status ?? 500,
      contentType: 'application/json',
      // A coded-shape envelope so the panel's tError path localizes it (not a bare string).
      body: rule.body ?? JSON.stringify({ error: { code: 'injected_fault', message: 'injected fault' } }),
    })
  })

  return {
    count: (route, method) =>
      Object.entries(tally)
        .filter(([k]) => {
          const sp = k.indexOf(' ')
          const m = k.slice(0, sp)
          const r = k.slice(sp + 1)
          return r === route && (method === undefined || m === method.toUpperCase())
        })
        .reduce((sum, [, v]) => sum + v, 0),
    counts: () => ({ ...tally }),
  }
}

// stripCsrfHeader removes the X-CSRF-Token header from every state-changing operator request — the
// S10 CSRF positive-contract probe (error-render.spec). The double-submit token is gone, so a
// correctly-guarded backend must reject the mutation. Read-only methods pass untouched.
export async function stripCsrfHeader(page: Page): Promise<void> {
  await page.route('**/api/v1/operator/**', async (route: Route) => {
    const headers = { ...route.request().headers() }
    delete headers['x-csrf-token']
    await route.continue({ headers })
  })
}

// stripAuthHeader removes the Authorization bearer from every operator request, simulating a
// stale tab whose in-memory session bearer is gone — so the request must authenticate on the
// persisted httpOnly cookie alone (request()'s credentials:'include'). Used by stale-tab.spec.
export async function stripAuthHeader(page: Page): Promise<void> {
  await page.route('**/api/v1/operator/**', async (route: Route) => {
    const headers = { ...route.request().headers() }
    delete headers['authorization']
    await route.continue({ headers })
  })
}

// stripAuthAndCsrf removes BOTH the Authorization bearer AND the X-CSRF-Token from every operator
// request — forcing the cookie auth path with NO CSRF token. The backend exempts bearer auth from
// CSRF (auth_controller.go), so the S10 positive contract is only exercised on the cookie path:
// a state-changing cookie-auth request with no CSRF must be rejected (403). Used by error-render.spec.
export async function stripAuthAndCsrf(page: Page): Promise<void> {
  await page.route('**/api/v1/operator/**', async (route: Route) => {
    const headers = { ...route.request().headers() }
    delete headers['authorization']
    delete headers['x-csrf-token']
    await route.continue({ headers })
  })
}
