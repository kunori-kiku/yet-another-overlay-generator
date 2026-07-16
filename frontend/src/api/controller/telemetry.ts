// Telemetry-history client route (telemetry-history plan-3/4): the operator node resource-history
// read. LIVE-ONLY by contract — the caller renders the result and NEVER persists it (the
// stripLiveTelemetry custody rule).

import { request, type ControllerConfig } from './transport';
import { historyQueryString, parseNodeHistory, type NodeHistory } from '../../lib/telemetryHistory';

// nodeHistory fetches a node's resource history (telemetry-history plan-3/4): GET
// node-history?node=<id>&from=<RFC3339>&to=<RFC3339>[&step=<Go-duration>]. Operator-only,
// credentialed like every other operator read. LIVE-ONLY by contract: the caller renders the result
// and NEVER persists it (no store/localStorage write) — the same custody rule as stripLiveTelemetry.
// The wire buckets are parsed into a typed NodeHistory at the boundary (parseNodeHistory is defensive
// so a garbled bucket never throws). Omitting `step` lets the server pick a step that fits the window.
export async function nodeHistory(
  cfg: ControllerConfig,
  nodeId: string,
  from: string,
  to: string,
  step?: string,
): Promise<NodeHistory> {
  const res = await request(
    cfg,
    `node-history?${historyQueryString(nodeId, from, to, step)}`,
    { cache: 'no-store' },
  );
  return parseNodeHistory(await res.json());
}
