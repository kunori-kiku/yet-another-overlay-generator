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
import { Code, validate, validateSchema } from './validator';

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

describe('validateSchema WireGuard public key (plan-4)', () => {
  // The WG public key is rendered VERBATIM into peers' root-parsed wg configs, so validateSchema must
  // reject a malformed value (mirrors validator.ValidWGPublicKey / the Go schema check).
  const topoWith = (key: string): Topology => ({
    project: { id: 'p', name: 'P' },
    domains: [{ id: 'd1', name: 'mesh', cidr: '10.55.0.0/24', allocation_mode: 'auto', routing_mode: 'babel' }],
    nodes: [
      {
        id: 'router-a',
        name: 'router-a',
        role: 'router',
        domain_id: 'd1',
        wireguard_public_key: key,
        capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true },
      },
    ],
    edges: [],
  });
  const wgKeyCodes = (key: string): string[] =>
    validateSchema(topoWith(key))
      .errors.filter((e) => e.field === 'nodes[0].wireguard_public_key')
      .map((e) => e.code);

  it('accepts a valid 32-byte standard-base64 key', () => {
    expect(wgKeyCodes('AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=')).toEqual([]);
  });
  it('skips an empty key (a managed node gets its key from the registry)', () => {
    expect(wgKeyCodes('')).toEqual([]);
  });
  it.each([
    ['not base64 (hyphen)', 'not-a-valid-key'],
    ['valid base64 but wrong length', 'QUJD'],
    ['embedded newline (config-injection vector)', 'AetxbtqeRdq7xOMpbaVK3St4\nvAoSMsCzTSLvtqs8BTw='],
    ['surrounding whitespace', '  AetxbtqeRdq7xOMpbaVK3St4vAoSMsCzTSLvtqs8BTw=  '],
  ])('rejects %s', (_label, key) => {
    expect(wgKeyCodes(key)).toEqual([Code.NodeWGPublicKeyInvalid]);
  });
});

describe('validateSchema node ID charset (plan-7)', () => {
  // A node ID is a path/file/interface-name component, so it is stricter than a name (no space, no '/').
  const topoWith = (id: string): Topology => ({
    project: { id: 'p', name: 'P' },
    domains: [{ id: 'd1', name: 'mesh', cidr: '10.55.0.0/24', allocation_mode: 'auto', routing_mode: 'babel' }],
    nodes: [
      { id, name: 'router-a', role: 'router', domain_id: 'd1', capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true } },
    ],
    edges: [],
  });
  const idCodes = (id: string): string[] =>
    validateSchema(topoWith(id)).errors.filter((e) => e.field === 'nodes[0].id').map((e) => e.code);

  it('accepts a clean slug/uuid', () => {
    expect(idCodes('node-8f3a1c2e.4b5d_6')).toEqual([]);
  });
  it.each([
    ['space', 'node alpha'],
    ['path traversal', '../etc/passwd'],
    ['command substitution', 'node$(whoami)'],
    ['slash', 'a/b'],
  ])('rejects %s', (_label, id) => {
    expect(idCodes(id)).toEqual([Code.NodeIDIllegalChars]);
  });
});

describe('validateSchema edge endpoint port requires host (plan-1)', () => {
  // require-explicit-host: a port override with no host is rejected (mirrors schema.go).
  const topoWith = (host: string, port: number): Topology => ({
    project: { id: 'p', name: 'P' },
    domains: [{ id: 'd1', name: 'mesh', cidr: '10.55.0.0/24', allocation_mode: 'manual', routing_mode: 'babel' }],
    nodes: [
      { id: 'a', name: 'a', role: 'router', domain_id: 'd1', capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true } },
      { id: 'b', name: 'b', role: 'router', domain_id: 'd1', capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true } },
    ],
    edges: [
      { id: 'e', from_node_id: 'a', to_node_id: 'b', type: 'public-endpoint', endpoint_host: host, endpoint_port: port, transport: 'udp', is_enabled: true },
    ],
  });
  const portCodes = (host: string, port: number): string[] =>
    validateSchema(topoWith(host, port)).errors.filter((e) => e.field === 'edges[0].endpoint_port').map((e) => e.code);

  it('rejects a port override with no host', () => {
    expect(portCodes('', 51999)).toContain(Code.EdgeEndpointPortWithoutHost);
  });
  it('accepts a port override WITH a host', () => {
    expect(portCodes('host.example.com', 51999)).not.toContain(Code.EdgeEndpointPortWithoutHost);
  });
  it('accepts no port (auto) with no host', () => {
    expect(portCodes('', 0)).not.toContain(Code.EdgeEndpointPortWithoutHost);
  });
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

// rc.4 relay-path advisory: mimic (transport=tcp) needs a direct path; a relay-path edge (through an
// L7/UDP-accelerator relay that can't carry the fake-TCP) gets a WARNING, not an error.
describe('validate mimic relay-path warning (rc.4)', () => {
  const topo = (transport: string, type: string): Topology =>
    ({
      project: { id: 'p', name: 'P' },
      domains: [{ id: 'd1', name: 'net', cidr: '10.55.0.0/24', allocation_mode: 'auto', routing_mode: 'babel' }],
      nodes: [
        { id: 'a', name: 'a', platform: 'debian', role: 'router', domain_id: 'd1', capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true }, public_endpoints: [{ id: 'ae', host: 'a.example', port: 51820 }] },
        { id: 'b', name: 'b', platform: 'debian', role: 'router', domain_id: 'd1', capabilities: { can_accept_inbound: true, can_forward: true, has_public_ip: true }, public_endpoints: [{ id: 'be', host: 'b.example', port: 51820 }] },
      ],
      edges: [{ id: 'e1', from_node_id: 'a', to_node_id: 'b', type, transport, is_enabled: true }],
    }) as unknown as Topology;
  const warns = (transport: string, type: string): boolean =>
    validate(topo(transport, type)).warnings.some((w) => w.code === Code.EdgeMimicRelayPath);

  it('warns on a tcp + relay-path edge', () => {
    expect(warns('tcp', 'relay-path')).toBe(true);
  });
  it('does not warn on tcp + direct', () => {
    expect(warns('tcp', 'direct')).toBe(false);
  });
  it('does not warn on udp + relay-path (no mimic on a udp edge)', () => {
    expect(warns('udp', 'relay-path')).toBe(false);
  });
});
