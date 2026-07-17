// Shared parsing/formatting layer for automatic-device telemetry. The leaf contract and closed
// numeric authority live under types/ so type modules never acquire a back-edge into lib/.

import {
  DEVICE_NUMERIC_DEFINITIONS,
  type DeviceInventoryEntry,
  type DeviceInventoryMetric,
  type DeviceKind,
  type DeviceNumericKey,
  type DeviceSample,
  type DeviceSamplesMetric,
  type DeviceStatus,
} from '../types/deviceTelemetry';
import type { Node } from '../types/topology';

export {
  DEVICE_NUMERIC_DEFINITIONS,
  type DeviceInventoryEntry,
  type DeviceInventoryMetric,
  type DeviceKind,
  type DeviceNumericKey,
  type DeviceNumericUnit,
  type DeviceSample,
  type DeviceSamplesMetric,
  type DeviceStatus,
} from '../types/deviceTelemetry';

export interface DeviceTelemetryProjection {
  inventory?: DeviceInventoryMetric;
  samples?: DeviceSamplesMetric;
}

const DEVICE_KINDS = new Set<DeviceKind>(['block_device', 'filesystem', 'gpu']);
const DEVICE_STATUSES = new Set<DeviceStatus>([
  'ok',
  'tool_missing',
  'driver_unavailable',
  'metrics_unavailable',
  'unsupported',
  'collection_error',
]);
const SERIES_ID = /^[0-9a-f]{64}$/;
const MAX_DISK_ENTRIES = 64;
const MAX_GPU_ENTRIES = 16;
const MAX_TRUNCATED = 1_000_000;
const MAX_LABEL_BYTES = 128;
const MAX_MOUNT_POINT_BYTES = 256;
const MAX_FS_TYPE_BYTES = 64;
const MAX_VENDOR_BYTES = 64;
const MAX_MODEL_BYTES = 128;

const definitionByKey = new Map<DeviceNumericKey, typeof DEVICE_NUMERIC_DEFINITIONS[number]>(
  DEVICE_NUMERIC_DEFINITIONS.map((definition) => [definition.key, definition]),
);

export function isDeviceKind(value: unknown): value is DeviceKind {
  return typeof value === 'string' && DEVICE_KINDS.has(value as DeviceKind);
}

export function isDeviceSeriesID(value: unknown): value is string {
  return typeof value === 'string' && SERIES_ID.test(value);
}

export function deviceDefinitionsForKind(kind: DeviceKind) {
  return DEVICE_NUMERIC_DEFINITIONS.filter((definition) => definition.kind === kind);
}

export function telemetryDevicePolicyUpdate(enabled: boolean): Pick<Node, 'telemetry_devices'> {
  return { telemetry_devices: enabled ? { mode: 'all-eligible-v1' } : undefined };
}

function boundedDisplay(value: unknown, maxBytes: number, required = false): string | undefined {
  if (value === undefined && !required) return undefined;
  if (typeof value !== 'string') return undefined;
  if (required && value.trim() === '') return undefined;
  if (new TextEncoder().encode(value).length > maxBytes) return undefined;
  // Mirrors Go's unicode.IsGraphic categories: letters, marks, numbers, punctuation, symbols, and
  // ordinary spaces only. In particular, reject invisible formatting controls such as bidi
  // overrides. React still escapes every accepted value at render time.
  if (/[\p{C}\p{Zl}\p{Zp}]/u.test(value)) return undefined;
  return value;
}

function boundedUint(value: unknown): number | undefined {
  // Go capacities are uint64 and may exceed JavaScript's exact-integer range. They are optional
  // display metadata, never identities or chart values, so retain a finite nonnegative integer
  // approximately instead of discarding the otherwise-valid device row.
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value >= 0
    ? value
    : undefined;
}

function truncatedCount(value: unknown): number {
  const parsed = boundedUint(value);
  return parsed !== undefined && parsed <= MAX_TRUNCATED ? parsed : 0;
}

function parseInventoryEntry(raw: unknown): DeviceInventoryEntry | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const wire = raw as Record<string, unknown>;
  if (
    !isDeviceSeriesID(wire.series_id) ||
    !isDeviceKind(wire.kind) ||
    typeof wire.status !== 'string' ||
    !DEVICE_STATUSES.has(wire.status as DeviceStatus)
  ) {
    return null;
  }
  const label = boundedDisplay(wire.label, MAX_LABEL_BYTES, true);
  if (label === undefined) return null;

  const optionalText = (
    key: 'mount_point' | 'fs_type' | 'vendor' | 'model',
    max: number,
  ): string | undefined | null => {
    if (wire[key] === undefined) return undefined;
    return boundedDisplay(wire[key], max) ?? null;
  };
  const mountPoint = optionalText('mount_point', MAX_MOUNT_POINT_BYTES);
  const fsType = optionalText('fs_type', MAX_FS_TYPE_BYTES);
  const vendor = optionalText('vendor', MAX_VENDOR_BYTES);
  const model = optionalText('model', MAX_MODEL_BYTES);
  if (mountPoint === null || fsType === null || vendor === null || model === null) return null;

  let parentSeriesId: string | undefined;
  if (wire.parent_series_id !== undefined) {
    if (!isDeviceSeriesID(wire.parent_series_id) || wire.parent_series_id === wire.series_id) return null;
    parentSeriesId = wire.parent_series_id;
  }
  const capacityBytes = boundedUint(wire.capacity_bytes);
  const vramTotalBytes = boundedUint(wire.vram_total_bytes);
  if (wire.capacity_bytes !== undefined && capacityBytes === undefined) return null;
  if (wire.vram_total_bytes !== undefined && vramTotalBytes === undefined) return null;

  // Keep the frontend boundary closed in the same places as the Go DTO. A malformed kind-specific
  // row is omitted rather than displayed under the wrong device category.
  if (wire.kind === 'block_device') {
    if (mountPoint !== undefined || fsType !== undefined || vramTotalBytes !== undefined) return null;
  } else if (wire.kind === 'filesystem') {
    if (
      parentSeriesId === undefined ||
      mountPoint === undefined || mountPoint.trim() === '' ||
      fsType === undefined || fsType.trim() === '' ||
      vendor !== undefined || model !== undefined || vramTotalBytes !== undefined
    ) {
      return null;
    }
  } else if (
    parentSeriesId !== undefined || mountPoint !== undefined || fsType !== undefined ||
    capacityBytes !== undefined || vendor === undefined || vendor.trim() === ''
  ) {
    return null;
  }

  return {
    seriesId: wire.series_id,
    kind: wire.kind,
    label,
    ...(parentSeriesId === undefined ? {} : { parentSeriesId }),
    ...(mountPoint === undefined ? {} : { mountPoint }),
    ...(fsType === undefined ? {} : { fsType }),
    ...(vendor === undefined ? {} : { vendor }),
    ...(model === undefined ? {} : { model }),
    ...(capacityBytes === undefined || capacityBytes === 0 ? {} : { capacityBytes }),
    ...(vramTotalBytes === undefined || vramTotalBytes === 0 ? {} : { vramTotalBytes }),
    status: wire.status as DeviceStatus,
  };
}

function parseInventory(raw: unknown): DeviceInventoryMetric | undefined {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return undefined;
  const wire = raw as Record<string, unknown>;
  if (!Array.isArray(wire.devices)) return undefined;
  const devices: DeviceInventoryEntry[] = [];
  const seen = new Set<string>();
  let diskCount = 0;
  let gpuCount = 0;
  for (const candidate of wire.devices) {
    const entry = parseInventoryEntry(candidate);
    if (!entry || seen.has(entry.seriesId)) continue;
    if (entry.kind === 'gpu') {
      if (gpuCount >= MAX_GPU_ENTRIES) continue;
      gpuCount++;
    } else {
      if (diskCount >= MAX_DISK_ENTRIES) continue;
      diskCount++;
    }
    seen.add(entry.seriesId);
    devices.push(entry);
  }
  return { devices, truncated: truncatedCount(wire.truncated) };
}

function parseSample(raw: unknown): DeviceSample | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const wire = raw as Record<string, unknown>;
  if (!isDeviceSeriesID(wire.series_id) || !isDeviceKind(wire.kind)) return null;
  if (!wire.values || typeof wire.values !== 'object' || Array.isArray(wire.values)) return null;
  const values: Partial<Record<DeviceNumericKey, number>> = {};
  let accepted = 0;
  for (const [rawKey, rawValue] of Object.entries(wire.values as Record<string, unknown>)) {
    const definition = definitionByKey.get(rawKey as DeviceNumericKey);
    if (!definition || definition.kind !== wire.kind) return null;
    if (typeof rawValue !== 'number' || !Number.isFinite(rawValue) || rawValue < 0) return null;
    if (definition.unit === '%' && rawValue > 100) return null;
    values[definition.key] = rawValue;
    accepted++;
  }
  if (accepted === 0) return null;
  return { seriesId: wire.series_id, kind: wire.kind, values };
}

function parseSamples(raw: unknown): DeviceSamplesMetric | undefined {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return undefined;
  const wire = raw as Record<string, unknown>;
  if (!Array.isArray(wire.samples)) return undefined;
  const samples: DeviceSample[] = [];
  const seen = new Set<string>();
  let diskCount = 0;
  let gpuCount = 0;
  for (const candidate of wire.samples) {
    const sample = parseSample(candidate);
    if (!sample || seen.has(sample.seriesId)) continue;
    if (sample.kind === 'gpu') {
      if (gpuCount >= MAX_GPU_ENTRIES) continue;
      gpuCount++;
    } else {
      if (diskCount >= MAX_DISK_ENTRIES) continue;
      diskCount++;
    }
    seen.add(sample.seriesId);
    samples.push(sample);
  }
  return { samples, truncated: truncatedCount(wire.truncated) };
}

// mapDeviceTelemetry is the one defensive live boundary. Numeric rows are retained only when the
// same heartbeat projection contains an inventory row with the exact opaque identity and kind.
export function mapDeviceTelemetry(inventoryRaw: unknown, samplesRaw: unknown): DeviceTelemetryProjection {
  const inventory = parseInventory(inventoryRaw);
  if (inventory === undefined) return {};
  const parsedSamples = parseSamples(samplesRaw);
  if (parsedSamples === undefined) return { inventory };
  const admitted = new Map(inventory.devices.map((entry) => [entry.seriesId, entry.kind]));
  const samples = parsedSamples.samples.filter((sample) => admitted.get(sample.seriesId) === sample.kind);
  return {
    inventory,
    samples: {
      samples,
      truncated: parsedSamples.truncated + (parsedSamples.samples.length - samples.length),
    },
  };
}

export function formatDeviceBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let scaled = value;
  let unit = 0;
  while (scaled >= 1024 && unit < units.length - 1) {
    scaled /= 1024;
    unit++;
  }
  const digits = scaled >= 100 || unit === 0 ? 0 : scaled >= 10 ? 1 : 2;
  return `${scaled.toFixed(digits)} ${units[unit]}`;
}

export function formatDeviceNumeric(key: DeviceNumericKey, value: number): string {
  const definition = definitionByKey.get(key);
  if (!definition || !Number.isFinite(value)) return '—';
  return definition.unit === '%'
    ? `${value.toFixed(1)}%`
    : `${formatDeviceBytes(value)}/s`;
}
