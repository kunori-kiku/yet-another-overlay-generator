import { expect, type Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { e2eDir } from './harness'

// leakOracle is the runner-level gate on YAOG's zero-knowledge fleet-secret custody invariant
// — flagged by the pre-rc.1 investigation as a SECURITY invariant with zero automated tests.
// It reads the panel's persisted Zustand stores from localStorage and asserts NO confidential
// fleet data (public IPs / SSH targets / WG endpoints / key material) and NO session secrets
// ever survive there. Run at three checkpoints (post-deploy, post-refresh, post-logout).
//
// It mirrors three production contracts (hand-checked against the source, so a regression in
// either side trips this):
//  - topology-storage server-held BLANKING (topologyStore.ts partialize): in controller mode a
//    server-hydrated canvas persists only default/empty slices.
//  - controller-storage persist ALLOWLIST (controllerStore.ts partialize) + never a session secret.
//  - the ControllerNode non-secret node-cache field set (types/controller.ts).

// The controller-storage persist allowlist (controllerStore.ts partialize). leakOracle asserts
// the persisted keys are a SUBSET of this — a new persisted key must be added here deliberately.
const CONTROLLER_STORAGE_ALLOWLIST = new Set([
  'baseURL',
  'pathPrefix',
  'agentBaseURL',
  'operatorCredentialId',
  'operatorCredentialAlg',
  'operatorRpId',
  'operatorPublicKeyPEM',
  'mode',
  'nodes',
  'settings',
  'lastSyncedAt',
])

// Session secrets that must NEVER appear in controller-storage.
const FORBIDDEN_CONTROLLER_KEYS = ['sessionToken', 'csrfToken', 'operatorToken']

// The ControllerNode non-secret field set (types/controller.ts). A persisted node-cache entry
// may carry ONLY these — never an endpoint / public_endpoints / private* / preshared* / raw WG
// key bytes. nodeId is an allowed VALUE even though it can be hostname-derived.
const CONTROLLER_NODE_ALLOWED_FIELDS = new Set([
  'nodeId',
  'status',
  'hasWGPublicKey',
  'desiredGeneration',
  'appliedGeneration',
  'lastChecksum',
  'lastHealth',
  'agentVersion',
  'lastSeen',
  'enrolledAt',
  'rekeyRequested',
  'inRollout',
])

export interface PersistedStores {
  topology: { state?: Record<string, unknown> } | null
  controller: { state?: Record<string, unknown> } | null
  ui: { state?: Record<string, unknown> } | null
}

// readPersisted snapshots the three persist entries from the page's localStorage.
export async function readPersisted(page: Page): Promise<PersistedStores> {
  return page.evaluate(() => {
    const read = (k: string): { state?: Record<string, unknown> } | null => {
      const raw = localStorage.getItem(k)
      return raw ? JSON.parse(raw) : null
    }
    return {
      topology: read('topology-storage'),
      controller: read('controller-storage'),
      ui: read('ui-storage'),
    }
  })
}

// FIXTURE_SENTINELS are the seed topology's confidential strings (public IPs / SSH targets / WG
// endpoints) sourced from plan-13's single seed-topology.json — never duplicated — so the value
// grep is precise and cannot collide with the seeded nodeId.
function loadSentinels(): string[] {
  const topo = JSON.parse(
    fs.readFileSync(path.join(e2eDir, 'fixtures', 'seed-topology.json'), 'utf8'),
  ) as { nodes?: Array<{ hostname?: string }>; edges?: Array<{ endpoint_host?: string }> }
  const out = new Set<string>()
  for (const n of topo.nodes ?? []) if (n.hostname) out.add(n.hostname)
  for (const e of topo.edges ?? []) if (e.endpoint_host) out.add(e.endpoint_host)
  return [...out]
}

export const FIXTURE_SENTINELS = loadSentinels()

export interface LeakCheckOptions {
  // When true, the topology-storage state must match the server-held blanked shape (controller
  // mode, after a server hydrate/deploy). When false, only the value + allowlist checks run.
  expectServerHeldBlank?: boolean
  sentinels?: string[]
}

// assertNoFleetSecrets runs the three custody checks against a persisted-store snapshot.
export function assertNoFleetSecrets(stores: PersistedStores, opts: LeakCheckOptions = {}): void {
  const sentinels = opts.sentinels ?? FIXTURE_SENTINELS

  // (1) STRUCTURAL — server-held design slices are blanked (topologyStore.ts partialize).
  if (opts.expectServerHeldBlank) {
    const t = stores.topology?.state
    expect(t, 'topology-storage.state must exist for the server-held blank check').toBeTruthy()
    expect(t!.nodes, 'server-held topology must persist nodes:[]').toEqual([])
    expect(t!.edges, 'server-held topology must persist edges:[]').toEqual([])
    expect(t!.canvasFromServer, 'server-held topology must mark canvasFromServer:true').toBe(true)
    expect(t!.allocSchemaVersion, 'server-held topology must reset allocSchemaVersion:0').toBe(0)
  }

  // (2) VALUE — no fleet sentinel and no key-material shape anywhere in the serialized stores.
  const blob = JSON.stringify(stores)
  for (const s of sentinels) {
    expect(blob.includes(s), `persisted stores must not contain fleet sentinel ${JSON.stringify(s)}`).toBe(
      false,
    )
  }
  expect(blob.includes('-----BEGIN'), 'persisted stores must not contain a PEM/key block').toBe(false)

  // (3) CUSTODY + ALLOWLIST — controller-storage keys ⊆ allowlist, no session secrets, and each
  // node-cache entry carries only ControllerNode non-secret fields.
  const c = stores.controller?.state
  if (c) {
    for (const k of Object.keys(c)) {
      expect(
        CONTROLLER_STORAGE_ALLOWLIST.has(k),
        `controller-storage key ${JSON.stringify(k)} is not in the persist allowlist`,
      ).toBe(true)
    }
    for (const forbidden of FORBIDDEN_CONTROLLER_KEYS) {
      expect(forbidden in c, `controller-storage must never persist ${forbidden}`).toBe(false)
    }
    const nodes = c.nodes
    if (Array.isArray(nodes)) {
      for (const node of nodes) {
        for (const field of Object.keys(node as Record<string, unknown>)) {
          expect(
            CONTROLLER_NODE_ALLOWED_FIELDS.has(field),
            `persisted node-cache entry has non-ControllerNode field ${JSON.stringify(field)}`,
          ).toBe(true)
        }
      }
    }
  }
}
