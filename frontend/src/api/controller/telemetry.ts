// Telemetry-history client route: the operator node resource, active-probe, and exact-device history read. LIVE-ONLY
// by browser contract — the caller renders the result and NEVER persists it (the
// stripLiveTelemetry custody rule).

import { request, type ControllerConfig } from './transport';
import {
  historyQueryString,
  parseNodeHistory,
  type NodeHistory,
  type NodeHistoryRequestOptions,
} from '../../lib/telemetryHistory';

// nodeHistory fetches a node's resource, active-probe, and optional exact-device history: GET
// node-history?node=<id>&from=<RFC3339>&to=<RFC3339>[&step=<Go-duration>]. Operator-only,
// credentialed like every other operator read. LIVE-ONLY by contract: the caller renders the result
// and NEVER persists it (no store/localStorage write) — the same custody rule as stripLiveTelemetry.
// Resource buckets and the additive probe/device series are parsed into a typed NodeHistory at the
// boundary (defensively, so a garbled row never throws). Omitting `step` lets the server pick one.
export async function nodeHistory(
  cfg: ControllerConfig,
  nodeId: string,
  from: string,
  to: string,
  step?: string,
  options: NodeHistoryRequestOptions = {},
): Promise<NodeHistory> {
  const res = await request(
    cfg,
    `node-history?${historyQueryString(nodeId, from, to, step, options)}`,
    { cache: 'no-store', signal: options.signal },
  );
  return parseNodeHistory(await res.json());
}
