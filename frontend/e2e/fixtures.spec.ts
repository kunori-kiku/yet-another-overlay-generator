import { test, expect } from '@playwright/test'
import { totpNow } from './fixtures/totp'
import { assertNoFleetSecrets, FIXTURE_SENTINELS, type PersistedStores } from './fixtures/leakOracle'

// Phase-0 primitive self-checks (plan-14 DoD #9): prove the in-test helpers cannot silently
// drift from their Go counterparts / the custody contract before any spec relies on them. Pure
// (no page / harness) — they assert the primitives directly.

test.describe('plan-14 fixture self-checks', () => {
  test('totpNow matches the RFC-6238 SHA1 reference vector', () => {
    // RFC-6238 Appendix B: secret = ASCII "12345678901234567890", SHA1. At T=59s the 8-digit
    // value is 94287082, so the 6-digit TOTP is "287082". base32("12345678901234567890") =
    // "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ".
    expect(totpNow('GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ', 59)).toBe('287082')
    // A second vector at T=1111111109 → 8-digit 07081804 → 6-digit "081804".
    expect(totpNow('GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ', 1111111109)).toBe('081804')
  })

  test('leakOracle CATCHES a planted fleet sentinel', () => {
    const sentinel = FIXTURE_SENTINELS[0]
    expect(sentinel, 'seed-topology.json must yield at least one sentinel').toBeTruthy()
    const leaky: PersistedStores = {
      topology: { state: { project: { name: `box at ${sentinel}` } } },
      controller: null,
      ui: null,
    }
    expect(() => assertNoFleetSecrets(leaky)).toThrow()
  })

  test('leakOracle CATCHES a planted session secret in controller-storage', () => {
    const leaky: PersistedStores = {
      topology: null,
      controller: { state: { mode: 'controller', sessionToken: 'super-secret' } },
      ui: null,
    }
    expect(() => assertNoFleetSecrets(leaky)).toThrow()
  })

  test('leakOracle CATCHES a non-ControllerNode field in the node cache', () => {
    const leaky: PersistedStores = {
      topology: null,
      controller: { state: { mode: 'controller', nodes: [{ nodeId: 'node-1', endpoint: '1.2.3.4:51820' }] } },
      ui: null,
    }
    expect(() => assertNoFleetSecrets(leaky)).toThrow()
  })

  test('leakOracle CATCHES a leaked fleet domain CIDR in the server-held project/domains', () => {
    const leaky: PersistedStores = {
      topology: {
        state: {
          nodes: [],
          edges: [],
          canvasFromServer: true,
          allocSchemaVersion: 0,
          project: { id: 'project-1', name: 'New Project' },
          // domains NOT blanked: a real fleet CIDR survived (the regression the structural check catches).
          domains: [{ id: 'd1', cidr: '203.0.113.0/24' }],
        },
      },
      controller: null,
      ui: null,
    }
    expect(() => assertNoFleetSecrets(leaky, { expectServerHeldBlank: true })).toThrow()
  })

  test('leakOracle PASSES a clean allowlist-only snapshot with a legitimate nodeId', () => {
    const clean: PersistedStores = {
      topology: {
        state: {
          nodes: [],
          edges: [],
          canvasFromServer: true,
          allocSchemaVersion: 0,
          // project/domains blanked to the defaults (topologyStore partialize server-held branch).
          project: { id: 'project-1', name: 'New Project', version: '0.1.0' },
          domains: [{ id: 'domain-default', name: 'overlay', cidr: '10.20.0.0/24' }],
        },
      },
      controller: {
        state: {
          mode: 'controller',
          baseURL: 'http://localhost:8080',
          agentBaseURL: 'http://localhost:9090',
          nodes: [{ nodeId: 'node-1', status: 'approved', appliedGeneration: 1, hasWGPublicKey: true }],
        },
      },
      ui: null,
    }
    expect(() => assertNoFleetSecrets(clean, { expectServerHeldBlank: true })).not.toThrow()
  })
})
