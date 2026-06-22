import { describe, it, expect } from 'vitest';
import { resolveMimicFallback } from './peers';

// TS half of the mimic-fallback resolution CONTRACT (plan-4) — must mirror Go
// compiler.resolveMimicFallback (peers.go) byte-for-byte so the Go↔TS conformance of the policy
// holds. The edge is `string | undefined` here (TS omits the field for "inherit").
describe('resolveMimicFallback (Go↔TS parity)', () => {
  const cases: Array<[string | undefined, string, string]> = [
    // explicit edge choice wins regardless of the default
    ['udp', '', 'udp'], ['udp', 'none', 'udp'], ['udp', 'udp', 'udp'],
    ['none', '', 'none'], ['none', 'udp', 'none'], ['none', 'none', 'none'],
    // edge inherits (undefined or '') → take the default, flooring non-'udp' to 'none'
    [undefined, '', 'none'], [undefined, 'udp', 'udp'], [undefined, 'none', 'none'],
    ['', '', 'none'], ['', 'udp', 'udp'],
    // defensive: an unrecognized EDGE inherits (follows the default); an unrecognized DEFAULT floors to 'none'
    ['garbage', '', 'none'], ['', 'garbage', 'none'], ['garbage', 'udp', 'udp'],
  ];
  it.each(cases)('resolveMimicFallback(%o, %o) === %o', (edge, def, want) => {
    expect(resolveMimicFallback(edge, def)).toBe(want);
  });
});
