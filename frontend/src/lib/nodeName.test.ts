import { describe, it, expect } from 'vitest';
import type { Node } from '../types/topology';
import { nodeNameMap, nodeDisplayName } from './nodeName';

// A minimal Node factory — only id + name matter to these helpers.
function node(id: string, name: string): Node {
  return { id, name, role: 'peer', domain_id: 'd1', capabilities: {} } as Node;
}

describe('nodeName', () => {
  it('nodeNameMap indexes name by id', () => {
    const m = nodeNameMap([node('n-alpha', 'alpha'), node('n-beta', 'beta')]);
    expect(m.get('n-alpha')).toBe('alpha');
    expect(m.get('n-beta')).toBe('beta');
    expect(m.size).toBe(2);
  });

  it('prefers the friendly name when one is known', () => {
    const m = nodeNameMap([node('n-alpha', 'alpha')]);
    expect(nodeDisplayName('n-alpha', m)).toBe('alpha');
  });

  it('falls back to the id for an orphan node (not in the design)', () => {
    const m = nodeNameMap([node('n-alpha', 'alpha')]);
    expect(nodeDisplayName('n-orphan', m)).toBe('n-orphan');
  });

  it('falls back to the id when no design is loaded (empty map)', () => {
    expect(nodeDisplayName('n-alpha', nodeNameMap([]))).toBe('n-alpha');
  });

  it('treats a blank or whitespace-only name as absent (falls back to the id)', () => {
    const m = nodeNameMap([node('n-blank', ''), node('n-space', '   ')]);
    expect(nodeDisplayName('n-blank', m)).toBe('n-blank');
    expect(nodeDisplayName('n-space', m)).toBe('n-space');
  });
});
