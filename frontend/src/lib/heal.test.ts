import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import type { Edge, Node, Topology } from '../types/topology'
import { healCollidingPins } from './normalizeEdges'

// Conformance canary: pin the FE heal (src/lib/normalizeEdges.ts healCollidingPins) byte-for-byte
// against the Go heal (internal/normalize.HealCollidingPins + internal/linkid) over the SHARED
// fixture corpus. It survives the framework-refactor plan-5 TS-compiler deletion because its subject
// — the FE healCollidingPins — is FE canvas/save heal logic, NOT the deleted compiler.
//
// The Go oracle (internal/localcompile BuildManifest, re-homed from internal/conformance in plan-5)
// freezes, for every corpus fixture, a canonical manifest whose `healed_edges` field is
// `normalize.HealCollidingPins` run over the fixture's INPUT topology, projected to {id +
// compiled_port + the six pinned_* fields} and sorted by id. This test re-runs the FE
// healCollidingPins over the SAME inputs and asserts the SAME projection — so a divergence in the FE
// linkKey / pinKey / allocation-field catalog / canonicalIP mirror reds the vitest run.

// Repo layout: this file lives at <repo>/frontend/src/lib/heal.conformance.test.ts, so the repo root
// is three directories up from frontend/, i.e. ../../.. from src/lib.
const thisDir = dirname(fileURLToPath(import.meta.url))
const repoRoot = join(thisDir, '..', '..', '..')

// The TWO corpus directories the Go harness reads, each paired with the golden dir holding its
// per-fixture manifests. SUCCESS fixtures are plan-3's shared contract corpus (consumed directly, not
// duplicated); FAIL fixtures are the conformance-only failing topologies — they never compile, but
// healed_edges is computed for every fixture regardless of the verdict, and the fail corpus carries
// the REAL-repair heal input (heal-collision-reenable) the canary most needs to exercise.
const corpora = [
  {
    label: 'success',
    fixturesDir: join(repoRoot, 'internal/localcompile/testdata/contract/topologies'),
    goldenDir: join(repoRoot, 'internal/localcompile/testdata/golden'),
  },
  {
    label: 'fail',
    fixturesDir: join(repoRoot, 'internal/localcompile/testdata/fail'),
    goldenDir: join(repoRoot, 'internal/localcompile/testdata/golden/fail'),
  },
] as const

// HealedEdge mirrors internal/localcompile.HealedEdge: edge identity, compiled_port, and six
// pinned_* fields the heal strips/keeps. The Go manifest emits zero values (0 / "") for absent pins; the FE Edge type has
// them optional, so projectEdge fills the same zero values to make the two byte-comparable.
interface HealedEdge {
  id: string
  compiled_port: number
  pinned_from_port: number
  pinned_to_port: number
  pinned_from_transit_ip: string
  pinned_to_transit_ip: string
  pinned_from_link_local: string
  pinned_to_link_local: string
}

// onDiskFixture is the JSON shape of a corpus fixture (the same `fixture` shape plan-3's loader and
// the Go conformance loader read): a name plus the topology under test.
interface OnDiskFixture {
  name?: string
  topology: Topology
}

// goldenManifest is the slice of the Go-oracle manifest this canary asserts against: only
// healed_edges is load-bearing here (the full topology/files/checksums diff is plan-4's job).
interface GoldenManifest {
  healed_edges: HealedEdge[]
}

// projectEdge maps an FE Edge to the Go HealedEdge projection, defaulting absent optional pins to the
// Go zero value (numbers -> 0, strings -> "") so a healed (stripped) edge and a never-pinned edge both
// serialize the way the Go oracle froze them.
function projectEdge(e: Edge): HealedEdge {
  return {
    id: e.id,
    compiled_port: e.compiled_port ?? 0,
    pinned_from_port: e.pinned_from_port ?? 0,
    pinned_to_port: e.pinned_to_port ?? 0,
    pinned_from_transit_ip: e.pinned_from_transit_ip ?? '',
    pinned_to_transit_ip: e.pinned_to_transit_ip ?? '',
    pinned_from_link_local: e.pinned_from_link_local ?? '',
    pinned_to_link_local: e.pinned_to_link_local ?? '',
  }
}

// loadFixtures reads every *.json fixture in dir, sorted by file name (matching the Go loader's stable
// order), and resolves the fixture name the SAME way the Go loader does: the explicit `name` field, or
// the file name minus ".json" as the fallback. The fixture name is the golden file's primary key.
function loadFixtures(dir: string): Array<{ name: string; fixture: OnDiskFixture }> {
  return readdirSync(dir)
    .filter((f) => f.endsWith('.json'))
    .sort()
    .map((file) => {
      const fixture = JSON.parse(readFileSync(join(dir, file), 'utf8')) as OnDiskFixture
      const name = fixture.name && fixture.name !== '' ? fixture.name : file.replace(/\.json$/, '')
      return { name, fixture }
    })
}

for (const corpus of corpora) {
  const fixtures = loadFixtures(corpus.fixturesDir)

  describe(`heal canary: FE healCollidingPins == Go normalize.HealCollidingPins (${corpus.label} corpus)`, () => {
    // Guard against an empty corpus silently passing the whole suite (a deleted-fixtures regression
    // would otherwise look green).
    it('reads a non-empty corpus', () => {
      expect(fixtures.length).toBeGreaterThan(0)
    })

    for (const { name, fixture } of fixtures) {
      it(`${name}`, () => {
        const topo = fixture.topology
        const edges: Edge[] = topo.edges ?? []
        const nodes: Node[] = topo.nodes ?? []

        // Run the production FE heal over the fixture's INPUT edges/nodes — exactly the surface the Go
        // oracle's healed_edges captured (normalize.HealCollidingPins over the input topology).
        const healed = healCollidingPins(edges, nodes)
        const got = healed
          .map(projectEdge)
          .sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))

        const golden = JSON.parse(
          readFileSync(join(corpus.goldenDir, `${name}.json`), 'utf8'),
        ) as GoldenManifest

        expect(got).toEqual(golden.healed_edges)
      })
    }
  })
}
