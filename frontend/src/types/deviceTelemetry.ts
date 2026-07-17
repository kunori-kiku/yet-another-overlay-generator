// Leaf contract for automatic-device telemetry. Go/TypeScript wiredrift reads the closed literal
// here; live mapping, history parsing, and chart rendering all consume this same authority.

export const DEVICE_NUMERIC_DEFINITIONS = [
  { key: 'disk_filesystem_used_pct', kind: 'filesystem', unit: '%' },
  { key: 'disk_read_bytes_per_second', kind: 'block_device', unit: 'B/s' },
  { key: 'disk_write_bytes_per_second', kind: 'block_device', unit: 'B/s' },
  { key: 'disk_io_busy_pct', kind: 'block_device', unit: '%' },
  { key: 'gpu_utilization_pct', kind: 'gpu', unit: '%' },
  { key: 'gpu_vram_used_pct', kind: 'gpu', unit: '%' },
] as const;

export type DeviceKind = typeof DEVICE_NUMERIC_DEFINITIONS[number]['kind'];
export type DeviceNumericKey = typeof DEVICE_NUMERIC_DEFINITIONS[number]['key'];
export type DeviceNumericUnit = typeof DEVICE_NUMERIC_DEFINITIONS[number]['unit'];

export type DeviceStatus =
  | 'ok'
  | 'tool_missing'
  | 'driver_unavailable'
  | 'metrics_unavailable'
  | 'unsupported'
  | 'collection_error';

export interface DeviceInventoryEntry {
  seriesId: string;
  kind: DeviceKind;
  label: string;
  parentSeriesId?: string;
  mountPoint?: string;
  fsType?: string;
  vendor?: string;
  model?: string;
  capacityBytes?: number;
  vramTotalBytes?: number;
  status: DeviceStatus;
}

export interface DeviceSample {
  seriesId: string;
  kind: DeviceKind;
  values: Partial<Record<DeviceNumericKey, number>>;
}

export interface DeviceInventoryMetric {
  devices: DeviceInventoryEntry[];
  truncated: number;
}

export interface DeviceSamplesMetric {
  samples: DeviceSample[];
  truncated: number;
}
