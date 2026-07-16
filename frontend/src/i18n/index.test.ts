import { describe, expect, it } from 'vitest';
import { tError } from './index';

describe('tError topology validation envelope', () => {
  const body = {
    error: {
      code: 'topology_validation_failed',
      message: 'topology validation failed',
      params: {
        field: 'nodes[8].telemetry_probes',
        validation_code: 'validation_node_telemetry_probes_invalid',
        validation_message: 'Invalid active telemetry configuration',
        validation_param_detail: 'probe "unfinished" has invalid host ""',
      },
    },
  };

  it('localizes the nested validator finding in English', () => {
    expect(tError(body, 'en')).toBe(
      'Deployment blocked at nodes[8].telemetry_probes: Invalid active telemetry configuration: probe "unfinished" has invalid host "". Set one host (an IP literal or DNS hostname) per probe; TCP also requires one port.',
    );
  });

  it('localizes the nested validator finding in Chinese', () => {
    const message = tError(body, 'zh');
    expect(message).toContain('nodes[8].telemetry_probes');
    expect(message).toContain('主动遥测');
    expect(message).toContain('probe "unfinished" has invalid host ""');
  });
});
