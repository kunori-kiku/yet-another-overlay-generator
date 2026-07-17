// Shared support for the controllerStore slices: the design-diff / canonicalization helpers, the
// configOf connection projection, the session-flush gate, the server-keystone reset constant, the
// exported selectors, and the localization shims. These are cross-slice utilities (moved verbatim
// from the single controllerStore.ts), exported so each slice imports exactly what it uses; the
// omitempty bookkeeping stays module-private to canonicalDesign.

import type { ControllerState } from './types';
import { getSession, type ControllerConfig } from '../../api/controllerClient';
import type { Topology } from '../../types/topology';
import { localizeError as localizeErrorFor } from '../../lib/localizeError';
import { SERVER_ALLOCATION_FIELDS } from '../../lib/allocationFields';
import { useTopologyStore } from '../topologyStore';
import { t, type MessageKey, type TParams } from '../../i18n';
import {
  controllerPreservesSuccessorTelemetryPolicy,
  requiresSuccessorTelemetryPolicy,
} from '../../lib/deployPreview';

// localizeError localizes a caught error at the live UI language via the shared localizer (a
// ControllerError -> tError so no raw "<status> <JSON>" reaches the UI; else its message; else the
// fallback key). A thin wrapper that supplies the language so the store's catch sites stay terse;
// the same shared localizer is used by the components that surface re-thrown errors.
export function localizeError(err: unknown, fallbackKey: MessageKey): string {
  return localizeErrorFor(err, useTopologyStore.getState().language, fallbackKey);
}

// tLocal localizes a catalog key against the live UI language — for store-set notices that are not
// derived from a caught error, so zh operators see translated strings instead of English literals.
export function tLocal(key: MessageKey, params?: TParams): string {
  return t(useTopologyStore.getState().language, key, params);
}

// assertControllerCanWriteTopology is the single fail-closed guard around the old-controller
// canonicalization boundary. Every whole-topology mutation calls it immediately before the write;
// preview route presence and controller semver are intentionally not treated as schema evidence.
export async function assertControllerCanWriteTopology(
  topology: Pick<Topology, 'nodes'>,
  config: ControllerConfig,
): Promise<void> {
  if (!requiresSuccessorTelemetryPolicy(topology)) return;
  // Re-probe immediately before each successor-bearing mutation. A cached login capability can
  // outlive an in-place controller rollback/restart; only this fresh authenticated response is safe
  // evidence that the process about to receive update-topology preserves the successor schema.
  const session = await getSession(config);
  if (!session || !controllerPreservesSuccessorTelemetryPolicy(session.controllerCapabilities)) {
    throw new Error(tLocal('controllerStore.successorTelemetryRequiresNewController'));
  }
}

// loadSlices projects a Topology down to exactly the fields loadTopology consumes
// (project/domains/nodes/edges + the schema version), so the hydration diff compares
// what an overwrite would actually change — nothing else.
export function loadSlices(t: Topology): Record<string, unknown> {
  return {
    project: t.project,
    domains: t.domains,
    nodes: t.nodes,
    edges: t.edges,
    alloc_schema_version: t.alloc_schema_version ?? 0,
  };
}

// stableStringify serializes with recursively-sorted object keys so two structurally
// equal designs compare equal regardless of key order (the server's canonical JSON vs
// the panel's getTopology() key order). Array order is preserved (significant). Used
// only for the hydration diff; being conservative (a reorder reads as "differs") is
// safe — it backs up + reloads, never loses data.
export function stableStringify(value: unknown): string {
  if (value === null || typeof value !== 'object') return JSON.stringify(value);
  if (Array.isArray(value)) return '[' + value.map(stableStringify).join(',') + ']';
  const obj = value as Record<string, unknown>;
  return (
    '{' +
    Object.keys(obj)
      .sort()
      .map((k) => JSON.stringify(k) + ':' + stableStringify(obj[k]))
      .join(',') +
    '}'
  );
}

// Go `omitempty`-tagged fields (internal/model/topology.go): the server DROPS these on
// marshal when zero/empty, so a client design and its server round-trip only compare equal
// if the client drops them too. NON-omitempty fields (ids/names, capabilities bools,
// is_enabled, public_endpoint.port, domain cidr/modes) are PRESERVED even when zero. Listed
// per-slice because `role` collides (Node.role is required, Edge.role is omitempty) — a flat
// name set would wrongly drop a node's role.
//
// Each list MUST equal, exactly, its model struct's set of `,omitempty` json tags — the
// internal/wiredrift drift gate (a REQUIRED CI check) reds the build on any divergence, in
// EITHER direction (a model omitempty field absent here, or a stale entry the model does not
// mark omitempty). A missing entry phantoms a save-conflict; an extra entry wrongly drops a
// required field. So a new model omitempty field is added here in the SAME change.
const PROJECT_OMITEMPTY = ['description', 'version'];
const DOMAIN_OMITEMPTY = ['description', 'reserved_ranges', 'transit_cidr'];
const NODE_OMITEMPTY = [
  'hostname', 'platform', 'deployment_mode', 'overlay_ip', 'mtu', 'xdp_mode', 'mimic_egress_interface', 'router_id',
  'fixed_private_key', 'wireguard_private_key', 'wireguard_public_key', 'public_endpoints',
  'extra_prefixes', 'ssh_alias', 'ssh_host', 'ssh_port', 'ssh_user', 'ssh_key_path', 'telemetry_probes',
  'telemetry_devices',
];
const EDGE_OMITEMPTY = [
  'endpoint_host', 'endpoint_port', 'compiled_port', 'priority', 'weight', 'role', 'transport',
  'mimic_fallback', 'link_direction', 'notes', 'pinned_from_port', 'pinned_to_port', 'pinned_from_transit_ip',
  'pinned_to_transit_ip', 'pinned_from_link_local', 'pinned_to_link_local',
];
// PublicEndpoint nests inside node.public_endpoints; id/host/port are required, note is omitempty.
const PUBLIC_ENDPOINT_OMITEMPTY = ['note'];
// TelemetryProbe nests inside node.telemetry_probes. Its optional display name, typed destination
// fields, URL status default, and schedule defaults disappear when empty/zero in Go's topology form.
const TELEMETRY_PROBE_OMITEMPTY = [
  'name', 'host', 'port', 'url', 'expected_status', 'interval_seconds', 'timeout_milliseconds',
];

// isEmptyVal mirrors Go's encoding/json `omitempty` "empty" definition: false, 0, "", nil,
// and zero-length slices. (Empty objects/structs are NOT omitted by Go, and aren't dropped here.)
function isEmptyVal(v: unknown): boolean {
  return (
    v === undefined || v === null || v === '' || v === 0 || v === false ||
    (Array.isArray(v) && v.length === 0)
  );
}

function dropOmitempty(obj: Record<string, unknown>, keys: readonly string[]): Record<string, unknown> {
  const out = { ...obj };
  for (const k of keys) if (k in out && isEmptyVal(out[k])) delete out[k];
  return out;
}

// canonicalDesign serializes a design exactly as the server stores it, for equality
// comparison: the loadSlices projection, key-order-insensitive (stableStringify), with Go
// `omitempty` zero-values dropped (so a save/hydrate round-trip compares equal) and every
// node's wireguard_private_key dropped unconditionally (controller mode is zero-knowledge —
// the server never stores a private key). Used for the dirty-state indicator + save-time
// conflict check (plan-10 / T2). Comparison is conservative: any residual asymmetry reads as
// "differs", which only over-warns (extra backup/conflict), never silently overwrites.
export function canonicalDesign(t: Topology): string {
  const s = loadSlices(t);
  const norm: Record<string, unknown> = {
    project: dropOmitempty(s.project as Record<string, unknown>, PROJECT_OMITEMPTY),
    domains: (s.domains as Array<Record<string, unknown>>).map((d) => dropOmitempty(d, DOMAIN_OMITEMPTY)),
    nodes: (s.nodes as Array<Record<string, unknown>>).map((n) => {
      const x = dropOmitempty(n, NODE_OMITEMPTY);
      delete x.wireguard_private_key; // always dropped (even if non-empty): never on the server
      // public_endpoints survives when non-empty (kept by dropOmitempty); mirror its OWN nested
      // omitempty (note) element-wise too, else an empty endpoint note ('' from the endpoint
      // editor) the server drops would phantom a save-conflict (review).
      if (Array.isArray(x.public_endpoints)) {
        x.public_endpoints = (x.public_endpoints as Array<Record<string, unknown>>).map((pe) =>
          dropOmitempty(pe, PUBLIC_ENDPOINT_OMITEMPTY),
        );
      }
      // telemetry_probes likewise survives when non-empty, but each nested probe has its own
      // `omitempty` fields. Normalizing those zeros prevents a successful save from later looking like
      // a concurrent server edit after Go's json.Marshal drops the explicit defaults.
      if (Array.isArray(x.telemetry_probes)) {
        x.telemetry_probes = (x.telemetry_probes as Array<Record<string, unknown>>).map((probe) =>
          dropOmitempty(probe, TELEMETRY_PROBE_OMITEMPTY),
        );
      }
      return x;
    }),
    edges: (s.edges as Array<Record<string, unknown>>).map((e) => dropOmitempty(e, EDGE_OMITEMPTY)),
  };
  // alloc_schema_version is omitempty: present only when > 0 (mirrors loadSlices + the server).
  if (s.alloc_schema_version) norm.alloc_schema_version = s.alloc_schema_version;
  return stableStringify(norm);
}

// sameIdSet reports whether two edge/node collections carry exactly the same set of ids
// (order-independent). Post-deploy it decides between an in-place pin overlay (set unchanged →
// preserve selection / open EdgeEditor) and a full hydrate (set diverged → overlay would be wrong).
export function sameIdSet(a: Array<{ id: string }>, b: Array<{ id: string }>): boolean {
  if (a.length !== b.length) return false;
  const ids = new Set(a.map((x) => x.id));
  for (const x of b) if (!ids.has(x.id)) return false;
  return true;
}

// canonicalDesignIgnoringPins is canonicalDesign with every server-derived allocation field
// dropped from edges, so the save-time conflict check tells a genuine concurrent edit
// (nodes/edges/non-pin fields changed under us) apart from a benign server-side pin addition
// (a deploy on another tab) — the latter is ADOPTED onto the canvas, not flagged as a conflict.
export function canonicalDesignIgnoringPins(t: Topology): string {
  const stripped: Topology = {
    ...t,
    edges: t.edges.map((e) => {
      const x = { ...e } as Record<string, unknown>;
      for (const f of SERVER_ALLOCATION_FIELDS) delete x[f];
      return x as unknown as typeof e;
    }),
  };
  return canonicalDesign(stripped);
}

// isDesignDirty: does the current design differ from the last server-synced snapshot
// (plan-10 / T2)? snapshot===null (no server design synced yet) → a non-empty design is
// dirty (unsaved work), empty is not. A PURE helper, not a store method, deliberately:
// a synchronous store method calling useTopologyStore.getState() inside the
// create<ControllerState>() literal forces TS to eagerly resolve the cross-store type
// cycle (topologyStore imports controllerStore) and breaks state inference — async
// methods defer that via Promise, but this is sync. Components pass the two values they
// already subscribe to, which also makes the dirty indicator reactive.
export function isDesignDirty(t: Topology, lastSyncedSnapshot: string | null): boolean {
  if (lastSyncedSnapshot === null) return t.nodes.length > 0 || t.edges.length > 0;
  return canonicalDesign(t) !== lastSyncedSnapshot;
}

// configOf slices the ControllerConfig the controllerClient needs out of the connection fields
// (without agentBaseURL). The EFFECTIVE bearer = the login session if present, else the
// break-glass operatorToken. This way the client layer need not know session vs. token apart —
// it just attaches the operatorToken field as the Bearer.
export function configOf(state: ControllerState): ControllerConfig {
  return {
    baseURL: state.baseURL,
    pathPrefix: state.pathPrefix,
    operatorToken: state.sessionToken || state.operatorToken,
    csrfToken: state.csrfToken,
  };
}

// ControllerActionContext binds an asynchronous controller action to the exact endpoint/auth
// context that authorized its first request. `authGeneration` advances whenever that context is
// replaced (endpoint/token change, login/logout, session loss, identity replacement, or workflow-
// mode transition). Capturing the config alongside the generation also prevents a multi-step
// action from accidentally issuing later legs against whatever target happens to be current after
// an await.
export interface ControllerActionContext {
  readonly generation: number;
  readonly config: ControllerConfig;
}

export function captureControllerActionContext(
  get: () => ControllerState,
): ControllerActionContext {
  const state = get();
  return {
    generation: state.authGeneration,
    config: configOf(state),
  };
}

// The single stale-continuation guard for every controller slice. Store-owned background actions
// use the boolean form and stop quietly; direct-return actions call requireControllerActionContext
// so their caller cannot mistake an old target's result for current data.
export function controllerActionContextIsCurrent(
  get: () => ControllerState,
  context: ControllerActionContext,
): boolean {
  return get().authGeneration === context.generation;
}

export function requireControllerActionContext(
  get: () => ControllerState,
  context: ControllerActionContext,
): void {
  if (!controllerActionContextIsCurrent(get, context)) {
    throw new Error(tLocal('controllerStore.controllerContextChanged'));
  }
}

// Security (controller server-authoritative): when the session probe fails and we are about to
// fall back to the login gate, if the canvas is a server secret mirror (canvasFromServer), wipe
// it — otherwise, while logged out, anyone with the browser could read the fleet's public IPs
// and SSH targets out of the canvas/localStorage. Only effective in controller mode; local
// original work is untouched (that is the user's own data).
//
// plan-10 / T2: before flushing, if the mirror has unsaved changes (dirty — differs from the
// last sync snapshot), export a backup first, otherwise logout / session loss / switching back
// to local would silently drop those undeployed edits. In steady state (already Saved or
// unchanged) dirty=false and no download fires. It takes only the two primitives it needs (mode
// + the sync-snapshot baseline), not the whole ControllerState type.
export function clearServerCanvasAtGate(mode: 'local' | 'controller', lastSyncedSnapshot: string | null): void {
  if (mode !== 'controller') return;
  const topo = useTopologyStore.getState();
  if (!topo.canvasFromServer) return;
  if (isDesignDirty(topo.getTopology(), lastSyncedSnapshot)) {
    const stamp = new Date().toISOString().slice(0, 10);
    topo.exportProject(`unsaved-changes-backup-${stamp}.json`);
  }
  topo.flushWorkspace();
}

// Derived selector: whether the user is logged in via password (holds a valid session).
// DeployPanel uses it to switch the login area between "login form / logged in as X". A
// break-glass operatorToken does not count as "logged in" (it is a recovery path).
export function selectLoggedIn(state: ControllerState): boolean {
  // sessionToken is the in-memory bearer (this tab's login); loggedIn is derived from the
  // GET /session cookie probe (survives a refresh that drops the in-memory token).
  return state.sessionToken !== '' || state.loggedIn;
}

// Derived selector: whether any usable operator credential is held (a login session or a
// break-glass token). configOf's EFFECTIVE bearer is exactly sessionToken || operatorToken, so
// either being non-empty allows issuing operator requests. DeployBar uses it to decide whether
// to disable Deploy/Roll-keys (it can no longer look at operatorToken alone, or a logged-in
// operator would be wrongly blocked).
export function selectHasAuth(state: ControllerState): boolean {
  // A cookie-restored session (loggedIn) can make operator requests too — the cookie is
  // attached automatically — so it counts as auth even when the in-memory token is empty.
  return state.sessionToken !== '' || state.loggedIn || state.operatorToken.trim() !== '';
}

// Derived selector: the number of fleet nodes still in rekey_requested (rotation requested, new
// public key not yet re-registered). Drives the "advisory" rotation experience — DeployBar's
// Deploy confirm + title hint, and the visibility of each node's "Cancel rekey" entry in
// NodeRegistry; it no longer hard-disables Deploy. Handy for echoing "N nodes still rotating
// keys".
export function selectRekeyingCount(state: ControllerState): number {
  // Only APPROVED nodes can re-register (a revoked node never clears its flag), so
  // exclude non-approved to avoid permanently gating Deploy on a stale flag.
  return state.nodes.filter((n) => n.rekeyRequested && n.status === 'approved').length;
}

// selectKeystoneStatusKnown reports whether the server keystone status has been probed yet, so the
// UI can show "checking…" (null) instead of a premature "Not enrolled" (false). The live UI reads
// serverOperatorPinned directly (server-authoritative — never the browser-local cache, which a
// browser-data wipe clears even though the server still holds the credential, the false "Not
// enrolled" that invited a fleet-stranding re-pin) together with this "known" gate.
export function selectKeystoneStatusKnown(state: ControllerState): boolean {
  return state.serverOperatorPinned !== null;
}

// selectHasLocalSigningKey reports whether THIS browser has a complete public WebAuthn invocation
// handle (credential ID, algorithm, and public-key PEM) for deploy(). It does not imply that
// private-key material is browser-local or non-exportable: the authenticator/provider owns that
// key and YAOG requests no attestation. A credential can be enrolled on the server yet lack a
// usable handle here until authenticated status hydration restores the public tuple.
export function selectHasLocalSigningKey(state: ControllerState): boolean {
  return (
    state.operatorCredentialId !== null &&
    state.operatorCredentialAlg !== null &&
    !!state.operatorPublicKeyPEM
  );
}

// serverKeystoneReset is the "unknown" state of the SERVER-authoritative keystone fields. It is the
// single reset used at every session-flush point (initial state, logout, and both session-loss
// branches of checkSession) so they stay in lockstep — a session expiry must not strand a stale
// "enrolled / redeploy-required" status that a not-noAuth-gated DeployBar chip could render before
// the next probe (mirrors the clearServerCanvasAtGate single-definition discipline).
export const serverKeystoneReset = {
  serverOperatorPinned: null,
  serverOperatorAlg: null,
  serverOperatorRpId: null,
  serverOperatorOrigin: null,
  serverOperatorPublicKeyPEM: null,
  serverOperatorFingerprint: null,
  serverRedeployRequired: false,
  pendingKeystoneRotate: false,
} as const;
