// Schema-validator conformance test — pins validateSchema (the SCHEMA half of the validator port)
// against the Go oracle (internal/validator.ValidateSchema).
//
// The expected codes + post-normalization mutations in testdata/schema_expected.json are NOT
// hand-authored: they are emitted by a throwaway Go harness that runs the SAME ValidateSchema over
// testdata/schema_fixtures.json and prints, per fixture, the sorted/deduped code set across both
// errors[] and warnings[] (the verdict.validator channel restricted to the schema pass) plus the
// per-domain routing_mode and per-edge transport AFTER the in-place normalization. So this test pins
// the TS port to the authoritative Go bytes, branch by branch.
//
// (The full /api/validate verdict channel — schema ∪ semantic — is gated by the plan-5 conformance
// harness once the semantic half lands; the corpus fixtures' validator verdicts are all semantic-pass
// codes, so the schema pass is pinned here against its own Go-derived oracle.)

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

import type { Topology } from '../types/topology';
import { validateSchema } from './validator';

const thisDir = dirname(fileURLToPath(import.meta.url));

interface Fixture {
  name: string;
  topology: Topology;
}

interface Expected {
  codes: string[];
  routing_modes: string[];
  transports: string[];
}

const fixtures = JSON.parse(
  readFileSync(join(thisDir, 'testdata', 'schema_fixtures.json'), 'utf8'),
) as Fixture[];

const expected = JSON.parse(
  readFileSync(join(thisDir, 'testdata', 'schema_expected.json'), 'utf8'),
) as Record<string, Expected>;

// sortedSet mirrors the Go oracle's verdict projection: the sorted, deduplicated set of finding codes
// across BOTH errors[] and warnings[].
function sortedSet(codes: string[]): string[] {
  return [...new Set(codes)].sort();
}

describe('validateSchema conformance (Go ValidateSchema oracle)', () => {
  it('covers every fixture in the corpus', () => {
    // Guard against a fixture being added to one file but not the other.
    expect(fixtures.map((f) => f.name).sort()).toEqual(Object.keys(expected).sort());
    expect(fixtures.length).toBeGreaterThan(10);
  });

  for (const fx of fixtures) {
    const exp = expected[fx.name];

    it(`${fx.name}: emits the Go schema code set`, () => {
      // Deep-copy the fixture: validateSchema MUTATES the topology in place (routing_mode/transport
      // normalization), so each assertion runs on a pristine input — matching the oracle's per-fixture
      // fresh copy (conformance/oracle.go:copyTopology).
      const topo = structuredClone(fx.topology);
      const res = validateSchema(topo);
      const codes = sortedSet([
        ...res.errors.map((e) => e.code),
        ...res.warnings.map((w) => w.code),
      ]);
      expect(codes).toEqual(exp.codes);
    });

    it(`${fx.name}: normalizes routing_mode + transport in place (round-trip)`, () => {
      const topo = structuredClone(fx.topology);
      validateSchema(topo);
      // After validateSchema, every domain.routing_mode and edge.transport must equal the value the Go
      // validator left behind (empty → "babel" / "udp"), observable downstream.
      expect(topo.domains.map((d) => d.routing_mode as string)).toEqual(exp.routing_modes);
      expect(topo.edges.map((e) => e.transport as string)).toEqual(exp.transports);
    });
  }
});

describe('validateSchema finding shape', () => {
  it('returns coded findings with field, code, message, level', () => {
    const topo: Topology = {
      project: { id: '', name: '' },
      domains: [],
      nodes: [],
      edges: [],
    };
    const res = validateSchema(topo);
    // project.id + project.name missing, then no-domains. Every finding carries the four shape fields.
    expect(res.errors.length).toBeGreaterThan(0);
    for (const e of res.errors) {
      expect(typeof e.field).toBe('string');
      expect(typeof e.code).toBe('string');
      expect(typeof e.message).toBe('string');
      expect(e.message.length).toBeGreaterThan(0);
      expect(e.level).toBe('error');
    }
    expect(res.warnings).toEqual([]);
  });

  it('interpolates {param} placeholders into the English message', () => {
    const topo: Topology = {
      project: { id: 'p', name: 'P' },
      domains: [
        {
          id: 'd1',
          name: 'D',
          cidr: '10.0.0.0/24',
          allocation_mode: 'auto',
          routing_mode: 'ospf' as 'babel',
        },
      ],
      nodes: [],
      edges: [],
    };
    const res = validateSchema(topo);
    const finding = res.errors.find((e) => e.code === 'validation_domain_routing_mode_invalid');
    expect(finding).toBeDefined();
    // The {mode} placeholder is substituted with the offending value.
    expect(finding!.message).toContain('ospf');
    expect(finding!.params).toEqual({ mode: 'ospf' });
  });
});
