// @vitest-environment node

import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import { TelemetryDevicePanel } from '../deploy/TelemetryDevicePanel';
import { mapDeviceTelemetry, telemetryDevicePolicyUpdate } from '../../lib/deviceTelemetry';
import type { Node } from '../../types/topology';

const GPU_ID = 'a'.repeat(64);

function node(overrides: Partial<Node> = {}): Node {
  return {
    id: 'node-1',
    name: 'Node 1',
    role: 'router',
    domain_id: 'domain-1',
    capabilities: {
      can_accept_inbound: true,
      can_forward: true,
      can_relay: false,
      has_public_ip: true,
    },
    ...overrides,
  };
}

describe('Fleet automatic device telemetry surface', () => {
  it('maps the checkbox to only the closed successor policy member', () => {
    expect(telemetryDevicePolicyUpdate(true)).toEqual({
      telemetry_devices: { mode: 'all-eligible-v1' },
    });
    expect(telemetryDevicePolicyUpdate(false)).toEqual({ telemetry_devices: undefined });
  });

  it('co-locates signed draft ownership, readiness, inventory, and live readings in Fleet', () => {
    const updateNode = vi.fn();
    const html = renderToStaticMarkup(createElement(TelemetryDevicePanel, {
      node: node({ telemetry_devices: { mode: 'all-eligible-v1' } }),
      inventory: {
        devices: [{
          seriesId: GPU_ID,
          kind: 'gpu',
          label: 'NVIDIA accelerator',
          vendor: 'NVIDIA',
          model: 'L4',
          vramTotalBytes: 24 * 1024 * 1024 * 1024,
          status: 'ok',
        }],
        truncated: 2,
      },
      samples: {
        samples: [{
          seriesId: GPU_ID,
          kind: 'gpu',
          values: { gpu_utilization_pct: 42, gpu_vram_used_pct: 60 },
        }],
        truncated: 1,
      },
      agentCapabilities: ['device-telemetry-v1', 'telemetry-policy-v2'],
      keystonePinned: true,
      language: 'en',
      updateNode,
    }));

    expect(html).toContain('type="checkbox"');
    expect(html).toContain('checked=""');
    expect(html).toContain('Automatically discover eligible disks and GPUs');
    expect(html).toContain('Agent readiness: Ready');
    expect(html).toContain('Save stores this choice in the controller design draft');
    expect(html).toContain('Deploy separately signs and activates it');
    expect(html).toContain('NVIDIA accelerator');
    expect(html).toContain(GPU_ID);
    expect(html).toContain('42.0%');
    expect(html).toContain('60.0%');
    expect(html).toContain('2 additional device inventory entries');
    expect(html).toContain('1 current device sample entries');
  });

  it('defensively maps only bounded inventory-backed numeric rows', () => {
    const projection = mapDeviceTelemetry({
      devices: [{
        series_id: GPU_ID,
        kind: 'gpu',
        label: 'GPU 0',
        vendor: 'NVIDIA',
        status: 'ok',
      }, {
        series_id: 'raw-pci-address',
        kind: 'gpu',
        label: 'bad',
        vendor: 'NVIDIA',
        status: 'ok',
      }, {
        series_id: 'd'.repeat(64),
        kind: 'gpu',
        label: 'GPU\u202e0',
        vendor: 'NVIDIA',
        status: 'ok',
      }, {
        series_id: 'e'.repeat(64),
        kind: 'block_device',
        label: 'Very large disk',
        capacity_bytes: Number.MAX_SAFE_INTEGER + 2,
        status: 'ok',
      }],
    }, {
      samples: [{
        series_id: GPU_ID,
        kind: 'gpu',
        values: { gpu_utilization_pct: 0 },
      }, {
        series_id: 'b'.repeat(64),
        kind: 'gpu',
        values: { gpu_utilization_pct: 50 },
      }, {
        series_id: 'c'.repeat(64),
        kind: 'gpu',
        values: { disk_io_busy_pct: 10 },
      }],
    });

    expect(projection.inventory?.devices).toHaveLength(2);
    expect(projection.inventory?.devices.find((device) => device.label === 'Very large disk')?.capacityBytes)
      .toBeGreaterThan(Number.MAX_SAFE_INTEGER);
    expect(projection.samples?.samples).toEqual([{
      seriesId: GPU_ID,
      kind: 'gpu',
      values: { gpu_utilization_pct: 0 },
    }]);
    expect(projection.samples?.truncated).toBe(1);
  });
});
