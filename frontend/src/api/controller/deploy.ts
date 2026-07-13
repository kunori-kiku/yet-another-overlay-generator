// Deploy-pipeline client routes (operator-only): the server-authoritative compile preview, the
// stage (delta/force) step, the pre-deploy dry-run, and the promote flip. Each POSTs the current
// public-keys-only design and maps the snake_case response at the boundary.

import { postJSON, type ControllerConfig } from './transport';
import type { CompileResponse } from '../../types/topology';
import type { StageResult } from '../../types/controller';
import type { DeployPreview, DeployPreviewNode, DeployForceArg } from '../../lib/deployPreview';

interface StageResponseJSON {
  staged: string[] | null;
  // unchanged (plan-5): delta-skipped nodes that kept their generation. omitempty/absent from an
  // older controller → mapped to [] so the result type field is always present.
  unchanged: string[] | null;
  skipped_unenrolled: string[] | null;
  generation: number;
}

// deployPreviewNodeJSON / deployPreviewResponseJSON mirror the plan-6 dry-run wire shape
// (deployPreviewResponseJSON in wire_controller.go). changed=true ⇒ an unforced Deploy re-stages it.
interface deployPreviewNodeJSON {
  node_id: string;
  name: string;
  changed: boolean;
}

interface deployPreviewResponseJSON {
  keystone_full_restage: boolean;
  nodes: deployPreviewNodeJSON[] | null;
  skipped_unenrolled: string[] | null;
}

interface GenerationResponseJSON {
  generation: number;
}

// compilePreview is a read-only, server-authoritative compile preview (operator-only): it
// POSTs the current design, the server renders the enrolled subgraph (no staging, no
// persistence, no side effects), and returns the configs plus the IDs of skipped (not yet
// enrolled) nodes. Zero-knowledge — the rendered wg configs contain only placeholder private
// keys. The response is the air-gap CompileResponse shape plus skipped_unenrolled.
export async function compilePreview(
  cfg: ControllerConfig,
  topoJSON: string
): Promise<CompileResponse> {
  const res = await postJSON(cfg, 'compile-preview', topoJSON);
  return (await res.json()) as CompileResponse;
}

// stage compiles the enrolled subgraph and stages it into the next generation (operator-only). An
// OPTIONAL force argument (plan-6) re-stages nodes even when their digest is unchanged: forceAll
// re-stages the whole fleet, forceNodes re-stages named nodes — the escape hatch around the plan-5
// delta-skip. No force (the default) ⇒ an empty body (a plain delta stage). The result now carries
// the delta-skipped `unchanged` set alongside `staged`.
export async function stage(cfg: ControllerConfig, force?: DeployForceArg): Promise<StageResult> {
  // Empty body when no force is requested (the backend decodes an empty body as "no force"); only a
  // real force serializes the force_all / force_nodes body.
  let body = '';
  if (force?.forceAll) {
    body = JSON.stringify({ force_all: true });
  } else if (force?.forceNodes && force.forceNodes.length > 0) {
    body = JSON.stringify({ force_nodes: force.forceNodes });
  }
  const res = await postJSON(cfg, 'stage', body);
  const data = (await res.json()) as StageResponseJSON;
  return {
    staged: data.staged ?? [],
    unchanged: data.unchanged ?? [],
    skippedUnenrolled: data.skipped_unenrolled ?? [],
    generation: data.generation,
  };
}

// deployPreview is the plan-6 read-only dry-run (POST .../deploy-preview, operator-only): it POSTs the
// CURRENT canvas (public-keys-only, EXACTLY what a Deploy pushes via update-topology then stages) and
// reports which enrolled nodes a Deploy WOULD re-stage (changed) vs skip (unchanged), plus the keystone
// full-restage flag — WITHOUT staging or any side effect. Previewing the POSTed canvas rather than the
// stored design means an unsaved edit is reflected, so the preview never lies about the blast radius.
// The deploy dialog calls it on open so the operator sees "N update / M unchanged" (and any pending
// keystone full restage) before confirming. Live-only by contract: the caller renders it and NEVER
// persists it (a transient operator action — the stripLiveTelemetry custody rule). topoJSON is the
// serialized public-keys-only model.Topology, posted verbatim exactly like compilePreview /
// updateTopology (the caller strips private keys first).
export async function deployPreview(cfg: ControllerConfig, topoJSON: string): Promise<DeployPreview> {
  const res = await postJSON(cfg, 'deploy-preview', topoJSON);
  const d = (await res.json()) as deployPreviewResponseJSON;
  const nodes: DeployPreviewNode[] = (d.nodes ?? []).map((n) => ({
    nodeId: n.node_id,
    name: n.name,
    changed: n.changed,
  }));
  return {
    keystoneFullRestage: d.keystone_full_restage,
    nodes,
    skippedUnenrolled: d.skipped_unenrolled ?? [],
  };
}

// promote flips the staged bundle to current and bumps the generation (operator-only),
// waking the /poll waiters.
export async function promote(
  cfg: ControllerConfig
): Promise<{ generation: number }> {
  const res = await postJSON(cfg, 'promote', '');
  const data = (await res.json()) as GenerationResponseJSON;
  return { generation: data.generation };
}
