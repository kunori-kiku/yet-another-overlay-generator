import { t, type MessageKey, type UILanguage } from '../../i18n';
import {
  DEVICE_NUMERIC_DEFINITIONS,
  formatDeviceBytes,
  formatDeviceNumeric,
  telemetryDevicePolicyUpdate,
  type DeviceInventoryMetric,
  type DeviceKind,
  type DeviceSamplesMetric,
  type DeviceStatus,
} from '../../lib/deviceTelemetry';
import { requiredTelemetryCapabilities } from '../../lib/deployPreview';
import type { Node } from '../../types/topology';

const KIND_KEYS: Record<DeviceKind, MessageKey> = {
  block_device: 'telemetryDevices.kind.blockDevice',
  filesystem: 'telemetryDevices.kind.filesystem',
  gpu: 'telemetryDevices.kind.gpu',
};

const STATUS_KEYS: Record<DeviceStatus, MessageKey> = {
  ok: 'telemetryDevices.status.ok',
  tool_missing: 'telemetryDevices.status.toolMissing',
  driver_unavailable: 'telemetryDevices.status.driverUnavailable',
  metrics_unavailable: 'telemetryDevices.status.metricsUnavailable',
  unsupported: 'telemetryDevices.status.unsupported',
  collection_error: 'telemetryDevices.status.collectionError',
};

const METRIC_KEYS = {
  disk_filesystem_used_pct: 'telemetryDevices.metric.filesystemUsed',
  disk_read_bytes_per_second: 'telemetryDevices.metric.readRate',
  disk_write_bytes_per_second: 'telemetryDevices.metric.writeRate',
  disk_io_busy_pct: 'telemetryDevices.metric.ioBusy',
  gpu_utilization_pct: 'telemetryDevices.metric.gpuUtilization',
  gpu_vram_used_pct: 'telemetryDevices.metric.vramUsed',
} satisfies Record<typeof DEVICE_NUMERIC_DEFINITIONS[number]['key'], MessageKey>;

type DeviceReadiness = 'ready' | 'upgrade-required' | 'not-confirmed';

const READINESS_KEYS: Record<DeviceReadiness, MessageKey> = {
  ready: 'fleetNodeDetailPage.telemetryPolicyReady',
  'upgrade-required': 'fleetNodeDetailPage.telemetryPolicyUpgradeRequired',
  'not-confirmed': 'fleetNodeDetailPage.telemetryPolicyNotConfirmed',
};

function deviceReadiness(
  node: Pick<Node, 'telemetry_devices'>,
  capabilities: readonly string[] | undefined,
): DeviceReadiness {
  if (capabilities === undefined) return 'not-confirmed';
  const required = requiredTelemetryCapabilities({
    telemetry_devices: node.telemetry_devices,
    telemetry_probes: undefined,
  });
  return required.every((capability) => capabilities.includes(capability))
    ? 'ready'
    : 'upgrade-required';
}

function statusClass(status: DeviceStatus): string {
  if (status === 'ok') return 'text-[var(--success)]';
  if (status === 'collection_error') return 'text-[var(--danger)]';
  return 'text-[var(--warning)]';
}

export function TelemetryDevicePanel({
  node,
  inventory,
  samples,
  agentCapabilities,
  keystonePinned,
  language,
  updateNode,
}: {
  node: Node;
  inventory: DeviceInventoryMetric | undefined;
  samples: DeviceSamplesMetric | undefined;
  agentCapabilities: readonly string[] | undefined;
  keystonePinned: boolean | null;
  language: UILanguage;
  updateNode: (id: string, updates: Partial<Node>) => void;
}) {
  const enabled = node.telemetry_devices?.mode === 'all-eligible-v1';
  const manual = node.deployment_mode === 'manual';
  const readiness = deviceReadiness(node, agentCapabilities);
  const samplesByID = new Map(samples?.samples.map((sample) => [sample.seriesId, sample]));

  return (
    <section
      className="space-y-3 rounded-lg border border-[var(--hairline)] bg-[var(--surface)] p-3"
      data-testid="telemetry-device-panel"
    >
      <div>
        <h3 className="text-sm font-semibold text-[var(--content)]">
          {t(language, 'telemetryDevices.heading')}
        </h3>
        <p id="telemetry-device-description" className="mt-1 text-xs text-[var(--content-muted)]">
          {t(language, 'telemetryDevices.description')}
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <label className="inline-flex items-center gap-2 text-sm text-[var(--content)]">
          <input
            type="checkbox"
            checked={enabled}
            disabled={manual}
            aria-describedby={`telemetry-device-description ${manual
              ? 'telemetry-device-manual-hint'
              : 'telemetry-device-activation-hint'}`}
            data-testid="telemetry-device-toggle"
            onChange={(event) => updateNode(node.id, telemetryDevicePolicyUpdate(event.target.checked))}
          />
          {t(language, 'telemetryDevices.enable')}
        </label>
        {enabled && !manual && (
          <span
            className={`text-xs ${readiness === 'ready'
              ? 'text-[var(--success)]'
              : readiness === 'upgrade-required'
                ? 'text-[var(--warning)]'
                : 'text-[var(--content-muted)]'}`}
            data-testid="telemetry-device-readiness"
          >
            {t(language, 'telemetryDevices.readiness', { status: t(language, READINESS_KEYS[readiness]) })}
          </span>
        )}
      </div>

      {manual ? (
        <p id="telemetry-device-manual-hint" className="text-xs text-[var(--content-muted)]">
          {t(language, 'telemetryDevices.manualUnsupported')}
        </p>
      ) : (
        <>
          <p id="telemetry-device-activation-hint" className="text-xs text-[var(--content-muted)]">
            {t(language, 'telemetryDevices.saveDeployHint')}
          </p>
          {enabled && (
            <p className={`text-xs ${keystonePinned === false
              ? 'text-[var(--warning)]'
              : keystonePinned === true
                ? 'text-[var(--success)]'
                : 'text-[var(--content-muted)]'}`}
            >
              {keystonePinned === false
                ? t(language, 'telemetryDevices.keystoneRequired')
                : keystonePinned === true
                  ? t(language, 'telemetryDevices.signed')
                  : t(language, 'telemetryDevices.keystoneChecking')}
            </p>
          )}
        </>
      )}

      <div className="border-t border-[var(--hairline)] pt-3" data-testid="telemetry-device-live">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h4 className="text-xs font-semibold text-[var(--content)]">
            {t(language, 'telemetryDevices.liveHeading')}
          </h4>
          {!enabled && inventory && inventory.devices.length > 0 && (
            <span className="text-xs text-[var(--warning)]">
              {t(language, 'telemetryDevices.previousDeployment')}
            </span>
          )}
        </div>

        {!inventory || inventory.devices.length === 0 ? (
          <p className="mt-2 text-xs text-[var(--content-muted)]" data-testid="telemetry-device-empty">
            {enabled
              ? t(language, 'telemetryDevices.waiting')
              : t(language, 'telemetryDevices.none')}
          </p>
        ) : (
          <div className="mt-2 space-y-2">
            {inventory.devices.map((device) => {
              const sample = samplesByID.get(device.seriesId);
              const definitions = DEVICE_NUMERIC_DEFINITIONS.filter((definition) =>
                definition.kind === device.kind && sample?.values[definition.key] !== undefined);
              return (
                <article
                  key={`${device.kind}:${device.seriesId}`}
                  className="rounded border border-[var(--hairline)] bg-[var(--surface-elevated)] p-2"
                  data-testid={`telemetry-device-${device.seriesId}`}
                >
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <div className="break-all text-sm font-medium text-[var(--content)]">{device.label}</div>
                      <div className="mt-0.5 text-xs text-[var(--content-muted)]">
                        {t(language, KIND_KEYS[device.kind])}
                        {device.mountPoint ? ` · ${device.mountPoint}` : ''}
                        {device.fsType ? ` · ${device.fsType}` : ''}
                        {device.vendor ? ` · ${device.vendor}` : ''}
                        {device.model ? ` · ${device.model}` : ''}
                      </div>
                      <div className="mt-0.5 break-all font-mono text-[10px] text-[var(--content-muted)]">
                        {device.seriesId}
                      </div>
                    </div>
                    <span className={`text-xs font-medium ${statusClass(device.status)}`}>
                      {t(language, STATUS_KEYS[device.status])}
                    </span>
                  </div>
                  {(device.capacityBytes !== undefined || device.vramTotalBytes !== undefined) && (
                    <p className="mt-1 text-xs text-[var(--content-muted)]">
                      {device.vramTotalBytes !== undefined
                        ? t(language, 'telemetryDevices.vramCapacity', {
                            capacity: formatDeviceBytes(device.vramTotalBytes),
                          })
                        : t(language, 'telemetryDevices.diskCapacity', {
                            capacity: formatDeviceBytes(device.capacityBytes ?? 0),
                          })}
                    </p>
                  )}
                  {definitions.length === 0 ? (
                    <p className="mt-2 text-xs text-[var(--content-muted)]">
                      {t(language, 'telemetryDevices.noCurrentReading')}
                    </p>
                  ) : (
                    <dl className="mt-2 flex flex-wrap gap-x-5 gap-y-1 text-xs">
                      {definitions.map((definition) => (
                        <div key={definition.key} className="flex gap-1.5">
                          <dt className="text-[var(--content-muted)]">{t(language, METRIC_KEYS[definition.key])}</dt>
                          <dd className="font-mono text-[var(--content)]">
                            {formatDeviceNumeric(definition.key, sample?.values[definition.key] ?? Number.NaN)}
                          </dd>
                        </div>
                      ))}
                    </dl>
                  )}
                </article>
              );
            })}
          </div>
        )}

        {(inventory?.truncated ?? 0) > 0 && (
          <p className="mt-2 text-xs text-[var(--warning)]" data-testid="telemetry-device-inventory-truncated">
            {t(language, 'telemetryDevices.inventoryTruncated', { count: inventory?.truncated ?? 0 })}
          </p>
        )}
        {(samples?.truncated ?? 0) > 0 && (
          <p className="mt-1 text-xs text-[var(--warning)]" data-testid="telemetry-device-samples-truncated">
            {t(language, 'telemetryDevices.samplesTruncated', { count: samples?.truncated ?? 0 })}
          </p>
        )}
      </div>
    </section>
  );
}
