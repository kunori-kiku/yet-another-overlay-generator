// Pure logic + types for the plan-6 pre-deploy preview and the Force-selection surface. Kept
// dependency-free (no React, no store, no fetch) so it unit-tests under the node-env vitest glob
// (src/lib/**/*.test.ts). The wire→typed parse lives in ../api/controllerClient.ts (deployPreview);
// this module owns the VIEW mapping (preview → counts + per-node rows) and the transient
// Force-selection state reducer. None of this is ever persisted — a deploy preview + the operator's
// Force ticks are a one-shot transient action (the same custody rule as stripLiveTelemetry).

// DeployPreviewNode is one node's dry-run verdict. changed=true ⇒ an UNFORCED Deploy would
// re-stage it (its digest differs from what it is served); changed=false ⇒ it would be skipped
// (unchanged — it keeps its current config).
export interface DeployPreviewNode {
  nodeId: string;
  name: string;
  changed: boolean;
}

// DeployPreview is the read-only dry-run of what a Deploy WOULD do (POST .../deploy-preview with the
// CURRENT canvas as the body), with no side effects. keystoneFullRestage=true ⇒ a keystone rotation /
// first-pin pends, so EVERY node re-stages regardless of its digest (the per-node Force is then moot —
// see keystoneFullRestage in deployPreviewRows). The preview compiles EXACTLY the canvas a Deploy would
// push (update-topology) then stage, so an unsaved canvas edit is reflected in the changed/unchanged
// split — the preview can never lie about the blast radius against a stale stored design.
export interface DeployPreview {
  keystoneFullRestage: boolean;
  nodes: DeployPreviewNode[];
  skippedUnenrolled: string[];
}

// ForceSelection is the operator's transient Force choices in the deploy dialog. forceAll re-stages
// every node; forceNodes names the individual unchanged nodes the operator ticked. Held as a Set
// for O(1) membership. Immutable-by-convention — the reducers below return a fresh object so React
// state updates are change-detectable.
export interface ForceSelection {
  forceAll: boolean;
  forceNodes: ReadonlySet<string>;
}

export function emptyForceSelection(): ForceSelection {
  return { forceAll: false, forceNodes: new Set<string>() };
}

// setForceAll returns a new selection with forceAll set. Turning it on PRESERVES any per-node ticks
// (harmless — resolveDeployForce sends force_all alone when it is on; turning it back off restores
// the individual ticks the operator had already made).
export function setForceAll(sel: ForceSelection, on: boolean): ForceSelection {
  return { forceAll: on, forceNodes: sel.forceNodes };
}

// toggleForceNode returns a new selection with nodeId's per-node Force flipped.
export function toggleForceNode(sel: ForceSelection, nodeId: string): ForceSelection {
  const next = new Set(sel.forceNodes);
  if (next.has(nodeId)) next.delete(nodeId);
  else next.add(nodeId);
  return { forceAll: sel.forceAll, forceNodes: next };
}

// DeployPreviewSummary is the headline count ("N will update / M unchanged"). It reflects the
// UNFORCED verdict — Force choices are layered per-row by deployPreviewRows / into the stage arg by
// resolveDeployForce, never folded into this baseline. When keystoneFullRestage pends the count is
// moot (the dialog shows the rotation note instead), so callers gate on that flag first.
export interface DeployPreviewSummary {
  changed: number;
  unchanged: number;
  total: number;
}

export function summarizeDeployPreview(p: DeployPreview): DeployPreviewSummary {
  let changed = 0;
  for (const n of p.nodes) if (n.changed) changed++;
  return { changed, unchanged: p.nodes.length - changed, total: p.nodes.length };
}

// DeployPreviewRow is one rendered per-node row. willStage folds in the EFFECTIVE outcome (changed
// OR a keystone full-restage OR Force); forceable is whether the per-node Force checkbox is
// meaningful at all (an already-staging node cannot be "forced" further, and a full restage / a
// Force-all makes every per-node tick moot); forced is the checkbox's current state.
export interface DeployPreviewRow {
  nodeId: string;
  name: string;
  changed: boolean;
  forced: boolean;
  forceable: boolean;
  willStage: boolean;
}

export function deployPreviewRows(p: DeployPreview, sel: ForceSelection): DeployPreviewRow[] {
  return p.nodes.map((n) => {
    const forced = sel.forceNodes.has(n.nodeId);
    // A per-node tick is meaningful ONLY for an unchanged node that is not already being staged by
    // a full restage or a Force-all.
    const forceable = !p.keystoneFullRestage && !sel.forceAll && !n.changed;
    const willStage = p.keystoneFullRestage || n.changed || sel.forceAll || forced;
    return { nodeId: n.nodeId, name: n.name, changed: n.changed, forced, forceable, willStage };
  });
}

// DeployForceArg is the optional Force argument the stage() call maps onto the wire
// (force_all / force_nodes). An empty object = no force (a plain delta Deploy).
export interface DeployForceArg {
  forceAll?: boolean;
  forceNodes?: string[];
}

// resolveDeployForce collapses the Force selection into the stage arg. force_all WINS and subsumes
// any per-node picks (we never send both — force_all already re-stages everything). Otherwise only
// the ticked node ids ride, sorted for a deterministic payload. Nothing selected ⇒ {} (an unforced
// Deploy). Note keystoneFullRestage is NOT reflected here: a pending full restage is enforced
// server-side regardless of the force arg, so the dialog need send no force for it.
export function resolveDeployForce(sel: ForceSelection): DeployForceArg {
  if (sel.forceAll) return { forceAll: true };
  if (sel.forceNodes.size > 0) return { forceNodes: [...sel.forceNodes].sort() };
  return {};
}
