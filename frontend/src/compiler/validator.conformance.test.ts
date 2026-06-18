import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import type { Topology } from '../types/topology'
import { validate } from './validator'

// Validator-channel conformance gate (plan-4 substep 12). Pin the TS validator (validateSchema +
// validateSemantic, run in the /api/validate order by the top-level validate()) byte-for-behavior
// against the Go oracle's verdict.validator channel — the SORTED, DEDUPLICATED set of finding Codes
// across BOTH errors[] and warnings[] of BOTH passes (internal/conformance/oracle.go validatorVerdict +
// BuildManifest). Every TS divergence from this channel is a port bug fixed at root, never shimmed here.
//
// The Go oracle (internal/conformance) freezes, for every corpus fixture, a canonical manifest whose
// verdict.validator field is exactly that sorted-dedup code set run on the fixture's INPUT topology
// (validate() mutates the routing_mode/transport defaults in place exactly as the Go validator does, so
// the channel sees the same normalized topology the Go oracle does). This test re-runs the TS validate
// over the SAME inputs and asserts the SAME sorted-dedup code set.

// Repo layout: this file lives at <repo>/frontend/src/compiler/validator.conformance.test.ts, so the
// repo root is three directories up from frontend/, i.e. ../../.. from src/compiler.
const thisDir = dirname(fileURLToPath(import.meta.url))
const repoRoot = join(thisDir, '..', '..', '..')

// The TWO corpus directories the Go harness reads, each paired with its golden dir. SUCCESS fixtures are
// plan-3's shared contract corpus; FAIL fixtures are the conformance-only failing topologies. The
// verdict.validator channel is populated for EVERY fixture regardless of whether the compile succeeds.
const corpora = [
  {
    label: 'success',
    fixturesDir: join(repoRoot, 'internal/localcompile/testdata/contract/topologies'),
    goldenDir: join(repoRoot, 'internal/conformance/testdata/golden'),
  },
  {
    label: 'fail',
    fixturesDir: join(repoRoot, 'internal/conformance/testdata/fail'),
    goldenDir: join(repoRoot, 'internal/conformance/testdata/golden/fail'),
  },
] as const

// onDiskFixture is the JSON shape of a corpus fixture (the same shape plan-3's loader and the Go
// conformance loader read): an optional name plus the topology under test.
interface OnDiskFixture {
  name?: string
  topology: Topology
}

// goldenManifest is the slice of the Go-oracle manifest this gate asserts against: only verdict.validator
// is load-bearing here (the full topology/files/checksums diff is a later plan-4 phase).
interface GoldenManifest {
  verdict: {
    validator: string[]
    apierr: string[]
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

// sortedSet mirrors internal/conformance/manifest.go sortedSet: dedup then sort.Strings (lexicographic
// byte order). For the ASCII snake_case Codes here, JS Array.sort()'s default UTF-16 code-unit order
// coincides with Go's sort.Strings byte order.
function sortedSet(codes: string[]): string[] {
  return [...new Set(codes)].sort()
}

for (const corpus of corpora) {
  const fixtures = loadFixtures(corpus.fixturesDir)

  describe(`validator gate: TS validate() == Go verdict.validator (${corpus.label} corpus)`, () => {
    // Guard against an empty corpus silently passing the whole suite.
    it('reads a non-empty corpus', () => {
      expect(fixtures.length).toBeGreaterThan(0)
    })

    for (const { name, fixture } of fixtures) {
      it(`${name}`, () => {
        // validate() runs schema-then-semantic and mutates the topology in place (routing_mode/transport
        // defaults), exactly as the Go oracle does over its own copy. Collect the sorted-dedup Code set
        // across errors ∪ warnings — the verdict.validator channel.
        const res = validate(fixture.topology)
        const got = sortedSet([...res.errors, ...res.warnings].map((f) => f.code))

        const golden = JSON.parse(
          readFileSync(join(corpus.goldenDir, `${name}.json`), 'utf8'),
        ) as GoldenManifest

        expect(got).toEqual(golden.verdict.validator)
      })
    }
  })
}
