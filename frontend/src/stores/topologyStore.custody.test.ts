// @vitest-environment node
//
// topologyStore.custody.test.ts — re-homes the two custody guards that
// topologyStore.local-engine.test.ts (deleted by framework-refactor plan-5, which removed the
// hand-mirrored TS compiler along with its test twin) was the ONLY coverage for. Both guards are
// byte-UNCHANGED by plan-5 (see topologyStore.ts):
//
//   (6.2) controller-mode boundary (:816 compile / :897 exportArtifacts / :950
//         downloadDeployScript): each of the three key-needing compute actions REFUSES in
//         controller mode — sets an error matching /controller mode/i, issues NO fetch, and
//         performs NO local compute — because controller mode is zero-knowledge (public-keys-only;
//         the controller compiles server-side on Deploy) and a local compile there would need to
//         generate/reconstruct private keys.
//   (6.3) in-flight local->controller mode-flip guard (:857, inside compile()): if the operator
//         flips to controller mode WHILE a local compile is in flight, the reconstructed-
//         private-key CompileResponse must be DROPPED rather than persisted into store state — the
//         store must never leak reconstructed private keys into a controller-mode session.
//
// Since plan-5 deleted the TS compiler, LOCAL-mode compute is now always the in-browser Go/WASM
// engine (src/wasm/wasmEngine.ts, reached via src/lib/localEngine.ts's loadWasmEngine() dynamic
// import). This suite mocks ../wasm/wasmEngine (the CURRENT local-compute path) — NOT the deleted
// ../compiler — so it stays pure-node and fast (no real wasm instantiation), and so 6.2 can assert
// ZERO calls into local compute, not just zero fetches.
//
// Out of scope (covered elsewhere, not re-homed here): the seam-routing/parity groups
// (6.1/6.4/6.5/6.6 in the deleted suite) are exercised live by wasmEngine.test.ts and the
// permanent WASM-vs-golden conformance gate; validate()'s controller-mode in-browser-verify
// behavior carries no refusal guard by design (it is key-free) — not a custody boundary, so not
// re-homed here either.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// wasmEngine is the local-compute path post-plan-5 (lib/localEngine.ts's loadWasmEngine()
// dynamically imports this exact specifier). Mocking the whole module keeps the suite pure-node
// and lets 6.2/6.3 assert directly on calls into it.
vi.mock('../wasm/wasmEngine', () => ({
  ensureWasm: vi.fn(),
  compile: vi.fn(),
  validate: vi.fn(),
  deployScript: vi.fn(),
  deployScripts: vi.fn(),
  exportArtifacts: vi.fn(),
}));

import { useTopologyStore } from './topologyStore';
import { useControllerStore } from './controllerStore';
import * as wasmEngine from '../wasm/wasmEngine';
import type { CompileResponse, Topology } from '../types/topology';

const mockCompile = vi.mocked(wasmEngine.compile);
const mockExportArtifacts = vi.mocked(wasmEngine.exportArtifacts);
const mockDeployScripts = vi.mocked(wasmEngine.deployScripts);

// A minimal but VALID 2-node topology carrying real WireGuard private keys — lifted verbatim from
// the deleted topologyStore.local-engine.test.ts (itself lifted from the conformance `peer-role`
// golden fixture). baseTopology() returns a fresh deep copy each call so a test that mutates it
// cannot leak into the next.
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

// A CompileResponse shaped like a real local compile: topology.nodes carries the RECONSTRUCTED
// private keys (AirGap custody — the wasm engine derives them exactly like the server does),
// which is precisely the payload 6.3 must prove never reaches store state after a mid-flight
// controller-mode flip.
function fakeCompileResponse(topo: Topology): CompileResponse {
  return {
    topology: topo,
    wireguard_configs: { 'router-pub:wg-peer-leaf': '# wg config' },
    babel_configs: {},
    sysctl_configs: {},
    install_scripts: {},
    deploy_scripts: {},
    manifest: {
      project_id: topo.project.id,
      project_name: topo.project.name,
      version: '1',
      compiled_at: new Date().toISOString(),
      node_count: topo.nodes.length,
      checksum: 'deadbeef',
    },
  };
}

// loadStore installs a topology directly into the topology store's slices — bypassing
// healCollidingPins (irrelevant here: the fixture has no pins) — so getTopology() echoes exactly
// what was put in. Mirrors the deleted suite's helper.
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

// A captured object-URL download name. exportArtifacts()/downloadDeployScript() create an <a> and
// click it on success; a controller-mode refusal must never reach that path, so 6.2 asserts this
// stays null.
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
  // NOTE: the Zustand persist middleware emits a benign "storage currently unavailable" warning in
  // the node environment (no localStorage). It is harmless — persistence is not under test here.
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
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// ── 6.2 — controller-mode boundary ──
describe('6.2 controller-mode boundary', () => {
  it('controller compile/export/deploy refuse — no local compute, no fetch', async () => {
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

    // No local compute either: the refusal fires BEFORE any local-engine adapter runs.
    expect(mockCompile).not.toHaveBeenCalled();
    expect(mockExportArtifacts).not.toHaveBeenCalled();
    expect(mockDeployScripts).not.toHaveBeenCalled();
  });
});

// ── 6.3 — in-flight mode-flip guard ──
describe('6.3 in-flight mode-flip guard', () => {
  it('a local compile that flips to controller mid-flight drops the result', async () => {
    const topo = baseTopology();
    mockCompile.mockResolvedValue(fakeCompileResponse(topo));
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('must not fetch');
      }),
    );

    // Flip to controller mode the instant the local compile resolves but before the post-compile
    // set() runs. Simplest deterministic trigger: spy on useControllerStore.getState so the FIRST
    // read (front-door guard) sees 'local' and every LATER read (the in-flight guard) sees
    // 'controller'.
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

    // The local compile DID run (the mock resolved with reconstructed private keys) — this is
    // what distinguishes the in-flight drop (:857) from the front-door refusal (:816) above.
    expect(mockCompile).toHaveBeenCalledTimes(1);

    // ...but the mode flip mid-flight means the reconstructed-private-key result must NOT have
    // been persisted into store state.
    expect(useTopologyStore.getState().compileResult).toBeNull();
    expect(useTopologyStore.getState().history).toHaveLength(0);
    expect(useTopologyStore.getState().isCompiling).toBe(false);
  });
});
