import type { Node } from '../types/topology';

// nodeDeploymentModeUpdate keeps the Design custody transition fail-safe. Manual nodes have no
// resident agent, so switching to manual must clear every agent-executed telemetry policy together.
export function nodeDeploymentModeUpdate(
  node: Pick<Node, 'telemetry_probes' | 'telemetry_devices'>,
  mode: 'managed' | 'manual',
): Pick<Node, 'deployment_mode' | 'telemetry_probes' | 'telemetry_devices'> {
  const manual = mode === 'manual';
  return {
    deployment_mode: manual ? 'manual' : undefined,
    telemetry_probes: manual ? undefined : node.telemetry_probes,
    telemetry_devices: manual ? undefined : node.telemetry_devices,
  };
}
