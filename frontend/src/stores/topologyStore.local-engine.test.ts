// @vitest-environment node
//
// topologyStore.local-engine.test.ts — the FIRST store-level vitest suite (plan-6, milestone
// 1.6; report §7: "FRONTEND HAS ZERO AUTOMATED TESTS"). It pins the local-engine SEAM: that
// in LOCAL mode with the flag unset or set to 'local' (default-ON, plan-7 Phase 0.5) the four
// compute actions (validate/compile/exportArtifacts/downloadDeployScript) run the in-browser
// plan-4 TS compiler and never fetch; that ONLY the explicit 'backend' opt-out makes them POST
// to the backend (the retained escape-hatch path); that the CONTROLLER-mode boundary is
// untouched (authenticated same-origin validate fetch with Bearer+CSRF and NO
// credentials:'include'; compile/export/deploy refuse); that an in-flight controller mode-flip
// drops a local compile's reconstructed private keys; and that the local CompileResponse is the
// exact air-gap shape downstream consumers expect.
//
// The suite uses the REAL compiler (no compiler mock) so the parity groups (6.4/6.5/6.6) pin
// the actual library output, and uses a node environment with a minimal DOM stub for the
// object-URL download path. It runs under vitest.config.ts (the include glob was extended to
// pick up this *.test.ts alongside the conformance + compiler unit suites).

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useTopologyStore } from './topologyStore';
import { useControllerStore } from './controllerStore';
import * as compilerIndex from '../compiler/index';
import { resolveNodeInterfaces } from '../lib/compiledInterfaces';
import type { Topology } from '../types/topology';

// A minimal but VALID 2-node topology carrying real WireGuard private keys (so the AirGap
// keygen pre-pass derives public keys and the full pipeline succeeds). Lifted from the
// conformance `peer-role` golden fixture. baseTopology() returns a fresh deep copy each call
// so a test that mutates it (e.g. clears project.id) cannot leak into the next.
function baseTopology(): Topology {
  return JSON.parse(
    JSON.stringify({
      project: { id: 'seam-test', name: 'Seam Test' },
      domains: [
        {
          id: 'domain-1',
          name: 'mesh',
          cidr: '10.33.0.0/24',
          allocation_mode: 'auto',
          routing_mode: 'babel',
        },
      ],
      nodes: [
        {
          id: 'router-pub',
          name: 'router-pub',
          hostname: 'router-pub.example.com',
          platform: 'debian',
          role: 'router',
          domain_id: 'domain-1',
          capabilities: {
            can_accept_inbound: true,
            can_forward: true,
            can_relay: false,
            has_public_ip: true,
          },
          wireguard_private_key: '2gJWNfXhZ4I4QKU/haCH6FBFPF5i4j8y5ZLMjOun6P8=',
          wireguard_public_key: 'svWGEYV40jtCOlfpv7HvRAh63230ETzfJzwMWO9oR3Q=',
          public_endpoints: [
            { id: 'router-pub-ep', host: 'router-pub.example.com', port: 51820 },
          ],
        },
        {
          id: 'peer-leaf',
          name: 'peer-leaf',
          hostname: 'peer-leaf.example.com',
          platform: 'ubuntu',
          role: 'peer',
          domain_id: 'domain-1',
          capabilities: {
            can_accept_inbound: false,
            can_forward: false,
            can_relay: false,
            has_public_ip: false,
          },
          wireguard_private_key: 'q8WXqb7bkAdTe3fsKUpbVlTUTgumt07Xl/9YceA5qLU=',
          wireguard_public_key: 'DbhG/Q5E9B/UcJJbCXxBB9wBiKNZViEPAUjCywHf2Ag=',
        },
      ],
      edges: [
        {
          id: 'e-peer',
          from_node_id: 'peer-leaf',
          to_node_id: 'router-pub',
          type: 'public-endpoint',
          endpoint_host: 'router-pub.example.com',
          transport: 'udp',
          is_enabled: true,
        },
      ],
    }),
  ) as Topology;
}

// loadStore installs a topology directly into the topology store's slices (bypassing the
// healCollidingPins on loadTopology is unnecessary — there are no pins — but we set slices
// straight so getTopology() echoes exactly what we put in).
function loadStore(topo: Topology): void {
  useTopologyStore.setState({
    project: topo.project,
    domains: topo.domains,
    nodes: topo.nodes,
    edges: topo.edges,
    allocSchemaVersion: 0,
    history: [],
    validateResult: null,
    compileResult: null,
    error: null,
  });
}

// A captured object-URL download (filename) — the export/deploy paths create an <a> and click
// it. In a node environment we stub the few DOM bits those paths touch and record the
// download filename so 6.6 can assert it.
let lastDownloadName: string | null = null;

function installDomStub(): void {
  lastDownloadName = null;
  const anchor = {
    href: '',
    _download: '',
    set download(v: string) {
      this._download = v;
      lastDownloadName = v;
    },
    get download(): string {
      return this._download;
    },
    click: () => {},
  };
  vi.stubGlobal('URL', {
    createObjectURL: () => 'blob:stub',
    revokeObjectURL: () => {},
  });
  vi.stubGlobal('document', {
    createElement: () => anchor,
    body: { appendChild: () => {}, removeChild: () => {} },
  });
  // NOTE: the Zustand persist middleware emits a benign "storage currently unavailable" warning
  // in the node environment (no localStorage). It is harmless — persistence is not under test
  // here, and the in-memory store state the assertions read is unaffected. (A late localStorage
  // stub does not silence it: persist captures its storage reference at module-init time.)
}

// makeFetchOk builds a fetch mock returning a 200 with a JSON or blob body, recording calls so
// a test can assert the URL/headers/credentials of a backend POST.
function makeFetchOk(jsonBody: unknown): ReturnType<typeof vi.fn> {
  return vi.fn(async () =>
    ({
      ok: true,
      status: 200,
      statusText: 'OK',
      json: async () => jsonBody,
      blob: async () => new Blob(['x']),
      text: async () => JSON.stringify(jsonBody),
      headers: { get: () => '' },
    }) as unknown as Response,
  );
}

beforeEach(() => {
  // Reset both stores to a known LOCAL baseline before each test.
  useControllerStore.setState({
    mode: 'local',
    sessionToken: '',
    operatorToken: '',
    csrfToken: '',
    loggedIn: false,
  });
  loadStore(baseTopology());
  installDomStub();
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// ── 6.1 — seam routing (default-ON + explicit opt-out) ──
describe('6.1 seam routing', () => {
  it('with the flag = local, all four actions run the TS compiler and never fetch', async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    const fetchSpy = vi.fn(async () => {
      throw new Error('fetch must not be called when the local engine is on');
    });
    vi.stubGlobal('fetch', fetchSpy);

    await useTopologyStore.getState().validate();
    expect(useTopologyStore.getState().validateResult).not.toBeNull();
    expect(useTopologyStore.getState().validateResult?.valid).toBe(true);

    await useTopologyStore.getState().compile();
    expect(useTopologyStore.getState().compileResult).not.toBeNull();
    expect(useTopologyStore.getState().error).toBeNull();

    await useTopologyStore.getState().exportArtifacts();
    await useTopologyStore.getState().downloadDeployScript('sh');

    // The whole point of the seam: zero network calls in the local-engine path.
    expect(fetchSpy).toHaveBeenCalledTimes(0);
  });

  it('with the flag unset (default-ON), all four actions run the TS compiler and never fetch', async () => {
    // No stubEnv ⇒ VITE_YAOG_LOCAL_ENGINE undefined ⇒ default-ON (plan-7 Phase 0.5) ⇒ the
    // in-browser compiler runs; nothing POSTs to the backend.
    const fetchSpy = vi.fn(async () => {
      throw new Error('fetch must not be called when the local engine is default-ON');
    });
    vi.stubGlobal('fetch', fetchSpy);

    await useTopologyStore.getState().validate();
    expect(useTopologyStore.getState().validateResult).not.toBeNull();
    expect(useTopologyStore.getState().validateResult?.valid).toBe(true);

    await useTopologyStore.getState().compile();
    expect(useTopologyStore.getState().compileResult).not.toBeNull();
    expect(useTopologyStore.getState().error).toBeNull();

    await useTopologyStore.getState().exportArtifacts();
    await useTopologyStore.getState().downloadDeployScript('sh');

    // Default-ON: the single localEngineEnabled() predicate gates all four actions identically,
    // so each runs the client-side compiler and none falls through to a backend route.
    expect(fetchSpy).toHaveBeenCalledTimes(0);
  });

  it("with the flag = backend (explicit opt-out), local-mode actions still fetch", async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'backend');
    const fetchSpy = makeFetchOk({ valid: true, errors: [], warnings: [] });
    vi.stubGlobal('fetch', fetchSpy);

    await useTopologyStore.getState().validate();
    await useTopologyStore.getState().compile().catch(() => {});
    await useTopologyStore.getState().exportArtifacts().catch(() => {});
    await useTopologyStore.getState().downloadDeployScript('sh').catch(() => {});
    // Explicit 'backend' opt-out: the single localEngineEnabled() predicate gates all four
    // actions identically, so each falls through to its backend route (no client-side compile).
    // This escape-hatch path is functional only against a `-tags airgap` server (plan-7 gates
    // these routes off the default controller build).
    const urls = fetchSpy.mock.calls.map((c) => String(c[0]));
    expect(urls).toContain('/api/validate');
    expect(urls).toContain('/api/compile');
    expect(urls).toContain('/api/export');
    expect(urls).toContain('/api/deploy-script?format=sh');
  });
});

// ── 6.2 — controller-mode boundary ──
describe('6.2 controller-mode boundary', () => {
  it('controller validate fetches /api/validate with Bearer+CSRF and NO credentials:include', async () => {
    // Even with the local-engine flag ON, controller mode must keep the server-authoritative fetch.
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    useControllerStore.setState({
      mode: 'controller',
      sessionToken: 'sess-abc',
      csrfToken: 'csrf-xyz',
    });
    const fetchSpy = makeFetchOk({ valid: true, errors: [], warnings: [] });
    vi.stubGlobal('fetch', fetchSpy);

    await useTopologyStore.getState().validate();

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/validate');
    const headers = init.headers as Record<string, string>;
    expect(headers['Authorization']).toBe('Bearer sess-abc');
    expect(headers['X-CSRF-Token']).toBe('csrf-xyz');
    // Intentional same-origin (Design §2): the httpOnly cookie rides automatically; a later
    // reviewer MUST NOT add credentials:'include' (that is the F1 cross-origin class, not this).
    expect(init.credentials).toBeUndefined();
  });

  it('controller validate post-refresh (cookie+CSRF only, no in-memory bearer) still fetches', async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    // Simulate a page-refresh state: the in-memory bearer is gone, only the cookie session +
    // CSRF token survive. loggedIn marks the cookie-backed session.
    useControllerStore.setState({
      mode: 'controller',
      sessionToken: '',
      operatorToken: '',
      csrfToken: 'csrf-refreshed',
      loggedIn: true,
    });
    const fetchSpy = makeFetchOk({ valid: true, errors: [], warnings: [] });
    vi.stubGlobal('fetch', fetchSpy);

    await useTopologyStore.getState().validate();

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/validate');
    const headers = init.headers as Record<string, string>;
    expect(headers['Authorization']).toBeUndefined(); // no in-memory bearer post-refresh
    expect(headers['X-CSRF-Token']).toBe('csrf-refreshed');
    expect(init.credentials).toBeUndefined();
  });

  it('controller compile/export/deploy refuse — no compiler call, no fetch', async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    useControllerStore.setState({ mode: 'controller' });
    const fetchSpy = vi.fn(async () => {
      throw new Error('controller refusal must not fetch');
    });
    vi.stubGlobal('fetch', fetchSpy);

    await useTopologyStore.getState().compile();
    expect(useTopologyStore.getState().compileResult).toBeNull();
    expect(useTopologyStore.getState().error).toMatch(/controller mode/i);

    await useTopologyStore.getState().exportArtifacts();
    expect(useTopologyStore.getState().error).toMatch(/controller mode/i);

    await useTopologyStore.getState().downloadDeployScript('sh');
    expect(useTopologyStore.getState().error).toMatch(/controller mode/i);

    expect(fetchSpy).toHaveBeenCalledTimes(0);
    expect(lastDownloadName).toBeNull(); // no download triggered
  });
});

// ── 6.3 — in-flight mode-flip guard ──
describe('6.3 in-flight mode-flip guard', () => {
  it('a local compile that flips to controller mid-flight drops the result', async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('must not fetch');
      }),
    );

    // Flip to controller mode the instant the local compile resolves but before the
    // post-compile set() runs, by hooking a microtask. Simplest deterministic trigger: spy on
    // useControllerStore.getState so the FIRST read (front-door guard) sees 'local' and a LATER
    // read (the in-flight guard) sees 'controller'.
    const real = useControllerStore.getState;
    let calls = 0;
    const spy = vi.spyOn(useControllerStore, 'getState').mockImplementation(() => {
      calls += 1;
      // Front-door guard is the first getState() call → local (allow the compile to start).
      // Every subsequent call (the in-flight guard) → controller (force the drop).
      const base = real();
      return { ...base, mode: calls <= 1 ? 'local' : 'controller' };
    });

    await useTopologyStore.getState().compile();

    spy.mockRestore();

    // The reconstructed-private-key result must NOT have been persisted.
    expect(useTopologyStore.getState().compileResult).toBeNull();
    expect(useTopologyStore.getState().history).toHaveLength(0);
    expect(useTopologyStore.getState().isCompiling).toBe(false);
  });
});

// ── 6.4 — reconciliation parity + air-gap shape ──
describe('6.4 reconciliation parity + air-gap shape', () => {
  it('a local compile pushes history, writes back alloc version, produces wg configs, and leaves skipped_unenrolled undefined', async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('must not fetch');
      }),
    );

    await useTopologyStore.getState().compile();
    const st = useTopologyStore.getState();

    // History entry pushed.
    expect(st.history).toHaveLength(1);
    expect(st.history[0].compileResult).toBe(st.compileResult);

    // alloc_schema_version written back (the compiler stamps 1).
    expect(st.allocSchemaVersion).toBe(1);
    expect(st.compileResult?.topology.alloc_schema_version).toBe(1);

    // A wireguard config chip is resolvable (the result is consumable downstream) — drive the
    // REAL downstream consumer (resolveNodeInterfaces), not just a non-empty-map check, so the
    // wg key format (<nodeID>:<interfaceName>) and the chip resolution are pinned end-to-end.
    const wg = st.compileResult?.wireguard_configs ?? {};
    expect(Object.keys(wg).length).toBeGreaterThan(0);
    const wgNodeId = Object.keys(wg)[0].split(':')[0];
    const chips = resolveNodeInterfaces(
      wgNodeId,
      wg,
      st.compileResult!.topology.nodes,
      st.compileResult!.topology.edges,
    );
    expect(chips.length).toBeGreaterThan(0);
    expect(chips[0].interfaceName).toBeTruthy();

    // Air-gap shape: /api/compile does NOT return skipped_unenrolled (topology.ts:156).
    expect(st.compileResult?.skipped_unenrolled).toBeUndefined();

    // The reconstructed topology carries private keys in data.topology.nodes (AirGap custody),
    // exactly as the server path produces — local export/deploy bundles need them.
    const nodes = st.compileResult?.topology.nodes ?? [];
    expect(nodes.length).toBe(2);
    expect(nodes.every((n) => !!n.wireguard_private_key)).toBe(true);
  });
});

// ── 6.5 — router_id round-trip (F2) ──
describe('6.5 router_id round-trip', () => {
  it('a pinned router_id survives a local compile (no GenerateRouterID regeneration)', async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('must not fetch');
      }),
    );

    const topo = baseTopology();
    topo.nodes[0].router_id = '02:11:22:33:44:55';
    loadStore(topo);

    await useTopologyStore.getState().compile();

    const compiledNodes = useTopologyStore.getState().compileResult?.topology.nodes ?? [];
    const router = compiledNodes.find((n) => n.id === 'router-pub');
    expect(router?.router_id).toBe('02:11:22:33:44:55');
  });
});

// ── 6.6 — export-filename parity ──
describe('6.6 export-filename parity', () => {
  it('a local export downloads `${project.id}-artifacts.zip`', async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('must not fetch');
      }),
    );

    await useTopologyStore.getState().exportArtifacts();
    expect(lastDownloadName).toBe('seam-test-artifacts.zip');
  });

  it('with project.id === "" the name is "-artifacts.zip" (no || "project" fallback, mirroring handler.go:240)', async () => {
    vi.stubEnv('VITE_YAOG_LOCAL_ENGINE', 'local');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('must not fetch');
      }),
    );

    // An empty project.id makes the topology fail SCHEMA validation, so a real compile would
    // never reach the filename step — but the store's filename is computed from project.id
    // independently of the compiler. Isolate the filename LOGIC by stubbing the compiler's
    // exportArtifacts to return a blob without compiling (localExport's loadCompiler() returns
    // this same module namespace, so the spy intercepts the store's call). This pins that the
    // store uses `${project.id}-artifacts.zip` with NO `|| 'project'` fallback (an empty id ⇒
    // `-artifacts.zip`, the same byte the Go `fmt.Sprintf("%s-artifacts.zip", ...)` produces).
    vi.spyOn(compilerIndex, 'exportArtifacts').mockResolvedValue(new Blob(['x']));

    const topo = baseTopology();
    topo.project.id = '';
    loadStore(topo);

    await useTopologyStore.getState().exportArtifacts();
    expect(useTopologyStore.getState().error).toBeNull();
    expect(lastDownloadName).toBe('-artifacts.zip');
  });
});
