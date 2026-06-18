import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type {
  ControllerNode,
  ControllerAuditEntry,
  StageResult,
} from '../types/controller';
import type {
  ControllerConfig,
  WebAuthnAlg,
  ControllerSettings,
  TOTPEnrollment,
  MintTokenResult,
} from '../api/controllerClient';
import {
  getNodes,
  getAudit,
  mintEnrollmentToken,
  updateTopology,
  stage,
  compilePreview as ctlCompilePreview,
  promote,
  revoke,
  rekeyAll,
  clearRekey,
  getTrustlist,
  postTrustlistSignature,
  postOperatorCredential,
  login as ctlLogin,
  logout as ctlLogout,
  getSettings,
  postSettings,
  fetchPins as ctlFetchPins,
  type AgentPinFetchRequest,
  type AgentPinFetchResult,
  getTOTPStatus,
  enrollTOTP as ctlEnrollTOTP,
  confirmTOTP as ctlConfirmTOTP,
  disableTOTP as ctlDisableTOTP,
  getPasskeyStatus,
  registerPasskey as ctlRegisterPasskey,
  disablePasskeyBegin,
  disablePasskeyFinish,
  passkeyLoginBegin,
  passkeyLoginFinish,
  getSession,
  getOperatorCredentialStatus,
  controllerErrorCode,
  getTopology as ctlGetTopology,
} from '../api/controllerClient';
import type { Topology } from '../types/topology';
import { enrollOperatorCredential, signManifest, assertLogin } from '../lib/webauthn';
import { stripPrivateKeys, dropAllKeys } from '../lib/custody';
import { localizeError as localizeErrorFor } from '../lib/localizeError';
import { useTopologyStore, ALLOCATION_PIN_FIELDS } from './topologyStore';
import { useUiStore } from './uiStore';
import { t, type MessageKey, type TParams } from '../i18n';

// localizeError localizes a caught error at the live UI language via the shared localizer (a
// ControllerError -> tError so no raw "<status> <JSON>" reaches the UI; else its message; else the
// fallback key). A thin wrapper that supplies the language so the store's catch sites stay terse;
// the same shared localizer is used by the components that surface re-thrown errors.
function localizeError(err: unknown, fallbackKey: MessageKey): string {
  return localizeErrorFor(err, useTopologyStore.getState().language, fallbackKey);
}

// tLocal localizes a catalog key against the live UI language — for store-set notices that are not
// derived from a caught error, so zh operators see translated strings instead of English literals.
function tLocal(key: MessageKey, params?: TParams): string {
  return t(useTopologyStore.getState().language, key, params);
}

// loadSlices projects a Topology down to exactly the fields loadTopology consumes
// (project/domains/nodes/edges + the schema version), so the hydration diff compares
// what an overwrite would actually change — nothing else.
function loadSlices(t: Topology): Record<string, unknown> {
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
function stableStringify(value: unknown): string {
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
// name set would wrongly drop a node's role. KEEP IN SYNC with the model's json tags.
const PROJECT_OMITEMPTY = ['description', 'version'];
const DOMAIN_OMITEMPTY = ['description', 'reserved_ranges', 'transit_cidr'];
const NODE_OMITEMPTY = [
  'hostname', 'platform', 'overlay_ip', 'mtu', 'xdp_mode', 'router_id',
  'fixed_private_key', 'wireguard_private_key', 'wireguard_public_key', 'public_endpoints',
  'extra_prefixes', 'ssh_alias', 'ssh_host', 'ssh_port', 'ssh_user', 'ssh_key_path',
];
const EDGE_OMITEMPTY = [
  'endpoint_host', 'endpoint_port', 'compiled_port', 'priority', 'weight', 'role', 'transport',
  'notes', 'pinned_from_port', 'pinned_to_port', 'pinned_from_transit_ip', 'pinned_to_transit_ip',
  'pinned_from_link_local', 'pinned_to_link_local',
];
// PublicEndpoint nests inside node.public_endpoints; id/host/port are required, note is omitempty.
const PUBLIC_ENDPOINT_OMITEMPTY = ['note'];

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
function sameIdSet(a: Array<{ id: string }>, b: Array<{ id: string }>): boolean {
  if (a.length !== b.length) return false;
  const ids = new Set(a.map((x) => x.id));
  for (const x of b) if (!ids.has(x.id)) return false;
  return true;
}

// canonicalDesignIgnoringPins is canonicalDesign with every server-derived allocation field
// dropped from edges, so the save-time conflict check tells a genuine concurrent edit
// (nodes/edges/non-pin fields changed under us) apart from a benign server-side pin addition
// (a deploy on another tab) — the latter is ADOPTED onto the canvas, not flagged as a conflict.
function canonicalDesignIgnoringPins(t: Topology): string {
  const stripped: Topology = {
    ...t,
    edges: t.edges.map((e) => {
      const x = { ...e } as Record<string, unknown>;
      for (const f of ALLOCATION_PIN_FIELDS) delete x[f];
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

// Controller panel (Mode B) state. It is the single source of truth for the controller
// connection + fleet view, independent of topologyStore (which remains the sole source of
// truth for topology data). On deploy() it reads the current topology from topologyStore and
// reuses the same model.Topology JSON shape that compile() sends.
interface ControllerState {
  // Connection config (baseURL/pathPrefix/operatorToken make up the ControllerConfig;
  // agentBaseURL is the agent-port address EnrollmentFlow hands to nodes — display-only, not
  // part of operator request construction).
  baseURL: string;
  pathPrefix: string;
  agentBaseURL: string;
  // operatorToken is the optional BREAK-GLASS token (for recovery), held in memory only.
  // Day-to-day auth uses the session (sessionToken) obtained via password login. Neither is
  // persisted (no secret material in localStorage).
  operatorToken: string;

  // Workflow mode: local (local/manual) or controller (controller fleet). P2 lifted it out of
  // DeployPanel's useState into the store so the route pages can read it (nav visibility /
  // landing page / deploy split). P2 kept it in memory only (a refresh returns to local,
  // matching the original useState behavior); P4 added it to partialize to persist it.
  mode: 'local' | 'controller';
  setMode: (mode: 'local' | 'controller') => void;

  // Login session (plan-5.2 + appshell-P5): the bearer session token the server issues after
  // password login, held in memory only, never written to localStorage. From P5 the session is
  // also written to an httpOnly cookie, so after a refresh checkSession() probes GET /session to
  // restore the logged-in state (loggedIn) without reading the token in JS.
  // operatorName/sessionExpiresAt are used only to echo "logged in as X, expires at ...".
  sessionToken: string;
  operatorName: string | null;
  sessionExpiresAt: string | null;
  // csrfToken is the double-submit CSRF token (from the login / GET /session response), in
  // memory only, never persisted. It is echoed as the X-CSRF-Token header on cookie-authed
  // state-mutating requests (see configOf).
  csrfToken: string;
  // loggedIn is derived from the GET /session probe: true when the cookie session is still
  // valid after a refresh (at which point sessionToken has been lost with memory).
  // selectLoggedIn = sessionToken !== '' || loggedIn.
  loggedIn: boolean;

  // TOTP 2FA (plan-5.2): totpRequired means the last login had the correct password but a
  // missing/wrong second-factor code (backend 401 totp_required); the login form shows the code
  // input accordingly. totpEnabled is whether the currently logged-in operator account has 2FA
  // enabled (null=unknown/not fetched; a break-glass token has no account, so the status stays
  // null). Both are in memory only, never persisted.
  totpRequired: boolean;
  totpEnabled: boolean | null;

  // Login passkey (plan-5.2): whether the currently logged-in operator has registered a login
  // passkey (null=unknown/not fetched; a break-glass token has no account, so it stays null).
  // The passkey 2FA step needs no separate *Required flag: on receiving passkey_required,
  // login() pops the authenticator in place and resubmits (the signing flag drives the UI).
  passkeyRegistered: boolean | null;

  // fleet view
  nodes: ControllerNode[];
  audit: ControllerAuditEntry[];
  auditVerified: boolean;
  lastDeploy: StageResult | null;

  // bootstrap settings (plan-5.2, server-persisted): public agent URL / GitHub proxy / agent
  // release base. null means not yet loaded from the server (fetched on refresh).
  settings: ControllerSettings | null;

  // Server-authoritative hydration (plan-4, D1): after each login/session restore, GET
  // /topology overwrites the local canvas. When an overwrite would discard a local design that
  // is "non-empty and differs from the server", first download pre-hydration-backup-<date>.json
  // as insurance (D9, review fix: back up on EVERY divergent overwrite, not once per browser —
  // the latter would silently drop undeployed local edits). When hydrationNotice is true the
  // Shell shows a dismissible notice (localized live via txt(), language not frozen).
  hydrationNotice: boolean;

  // Deploy custody and shrink guard (plan-5, D4 + the audit's "one-click destruction" scenario):
  // - lastStrippedKeys: how many private keys were stripped from the canvas before the last
  //   deploy upload (0=no notice). Controller mode is zero-knowledge — private keys are never
  //   uploaded (the client mirror of the server's plan-1 400) — and an info notice is shown
  //   after stripping.
  // - pendingShrink: when a deploy would shrink the server design substantially (emptying it,
  //   or dropping over half the existing nodes), the to-be-confirmed info is stored here and the
  //   deploy is held; DeployBar pops a "type the project name to confirm" dialog, and on confirm
  //   deploy is re-invoked with confirmedShrink. Version history (plan-2) is the after-the-fact
  //   backstop; this guard is the up-front prevention.
  lastStrippedKeys: number;
  // pendingShrink carries the typed-confirm phrase (project name, or a non-empty
  // sentinel when the project is unnamed — an empty phrase would let an empty input
  // match and bypass the gate), the node-count delta for the dialog copy, and a
  // SNAPSHOT of the exact stripped design the warning was computed from (so the
  // confirmed deploy binds to what the operator was warned about, not a since-changed
  // canvas) plus its stripped-key count.
  pendingShrink: {
    serverNodeCount: number;
    canvasNodeCount: number;
    confirmPhrase: string;
    snapshot: Topology;
    stripped: number;
  } | null;

  // KEYSTONE (plan-5.1d): the pinned off-host operator signing credential (passkey / YubiKey).
  // Only non-secret info is persisted — credential_id (base64url(rawId)), alg, rpId — none of it
  // is key material (the private key never leaves the authenticator), but remembering it lets the
  // panel drive later signatures across a refresh (allowCredentials) and echo "signing key
  // registered". All three are null when not enrolled. The pinned PEM is NOT persisted in the
  // browser: at signing time the public_key field is audit-only, and nodes trust only the
  // server-pinned PEM — so there is no need to keep it on the frontend.
  operatorCredentialId: string | null;
  operatorCredentialAlg: WebAuthnAlg | null;
  operatorRpId: string | null;
  // pinned public-key PEM: non-secret (a public key); it is persisted only to fill the signing
  // artifact's audit-only public_key field with the actual self-describing public key; nodes
  // always trust only the server-pinned PEM, never this field.
  operatorPublicKeyPEM: string | null;

  // SERVER-authoritative keystone status (NOT persisted): the panel derives "enrolled" from THIS,
  // never the browser-local operatorCredential* cache, so clearing browser data can no longer
  // falsely read "Not enrolled" and invite a fleet-stranding re-pin. null = not yet probed
  // ("checking"); true/false = the server's answer. serverOperatorFingerprint is the controller's
  // non-secret identifier for the pinned key, shown truncated in the DeployBar chip (the panel does
  // not derive a local fingerprint nor compare the two). serverRedeployRequired is the controller's
  // "rotated-but-not-redeployed" signal (every node stranded until a fresh signed deploy lands).
  // Re-probed on connect/login/refresh via hydrateKeystoneStatus.
  serverOperatorPinned: boolean | null;
  serverOperatorAlg: string | null;
  serverOperatorFingerprint: string | null;
  serverRedeployRequired: boolean;
  // pendingKeystoneRotate gates the dangerous re-pin: when a credential is ALREADY pinned on the
  // server, enrollOperator() refuses to start the WebAuthn ceremony and instead sets this true so
  // the UI can demand an explicit rotate confirmation (the pinned key is already shown in the
  // enrolled chip); only enrollOperator({rotate:true}) proceeds. (Mirrors pendingShrink as a gate.)
  pendingKeystoneRotate: boolean;

  // volatile UI state
  loading: boolean;
  error: string | null;
  lastSyncedAt: number | null;
  // The normalized snapshot (canonicalDesign) of the "server-authoritative design" recorded
  // after the last sync with the server (hydrate / save). Used for: (1) the dirty indicator —
  // the current canvas differing from it means unsaved changes; (2) the conflict-detection
  // baseline before save — at save time the server design is re-GET'd and compared against it,
  // so a server that has been changed is not blindly overwritten (plan-10 / T2, D13). null=no
  // server design has been synced yet (server empty / before the first deploy). Not persisted
  // (rebuilt by hydrate after a refresh).
  lastSyncedSnapshot: string | null;
  // lastSyncedTopology is the "server-authoritative design" Topology object (rather than the
  // normalized string) recorded at the same moment as lastSyncedSnapshot. saveDesign uses it as
  // the base of a 3-way comparison: distinguishing "the server added pins" (benign → adopt onto
  // the canvas, no conflict / not discarded by a force overwrite) from "the operator cleared a
  // pin / changed something else" (preserve operator intent). null=not yet synced. Not persisted
  // (not in the partialize allowlist — rebuilt by hydrate after a refresh, and never let a
  // fleet's public IPs hit disk).
  lastSyncedTopology: Topology | null;
  // save conflict flag (plan-10 / T2): set true when saveDesign detects the server design has
  // changed since the last sync; the UI then pops a "server changed: re-sync from server (auto
  // backup) / overwrite anyway / cancel" dialog.
  saveConflict: boolean;
  // save in progress (plan-11 review #1): specific to saveDesign, distinct from the global
  // loading. The Save button / conflict dialog show "saving / disabled" off it, so an unrelated
  // controller op (refresh/deploy/saveSettings, etc.) that sets the global loading does not
  // wrongly light up "saving".
  saving: boolean;
  // previewing in progress (PR6): specific to compilePreview, distinct from the global loading
  // (same rationale as saving). The Compile button shows "compiling / disabled" off it, so the
  // global loading set by an unrelated controller op does not light it up.
  previewing: boolean;
  // signing true means the WebAuthn prompt is up, waiting for the user to touch the security key
  // (during enroll or deploy). The UI shows the "touch your security key" prompt off it.
  // enrolling distinguishes the enroll ceremony from the deploy-sign ceremony. Note:
  // signing/enrolling are exclusive to the KEYSTONE flow (deploy signing / signing-key
  // registration), and DeployBar's "authorize this deploy" banner shows off them. The login
  // passkey ceremony uses the separate loginCeremony flag below, so login/registering/removing a
  // login passkey does not wrongly light up the "authorize deploy" banner.
  signing: boolean;
  enrolling: boolean;
  // loginCeremony true means a "login passkey" WebAuthn prompt is in progress (password+passkey
  // 2FA, passwordless login, or registering/removing a login passkey). Kept separate from
  // keystone's signing/enrolling, it drives the "touch your security key" prompt in the login /
  // account-security areas without triggering DeployBar's deploy banner.
  loginCeremony: boolean;

  // actions
  setConfig: (partial: Partial<ControllerConfig & { agentBaseURL: string }>) => void;
  loadSettings: () => Promise<void>;
  // saveSettings persists settings (full-replace) and updates the store. It also RETURNS the
  // outcome — null on success, the localized error string on failure — so a card that lives on a
  // page without the global ControllerErrorBanner (e.g. Settings) can surface the failure locally
  // and show a success notice only on a real success. The global `error` is still set too (for the
  // banner consumers); fire-and-forget callers ignore the return (void-return bivalence).
  saveSettings: (s: ControllerSettings) => Promise<string | null>;
  // fetchReleasePins runs the assisted release-pin fetch (POST release-pins) over the current
  // controller config. It returns the resolved pins for a config CARD to review — it neither
  // persists nor auto-trusts anything (custody) and does NOT touch global loading/error; the
  // caller surfaces its own busy/localError. Throws ControllerError on a coded failure.
  fetchReleasePins: (body: AgentPinFetchRequest) => Promise<AgentPinFetchResult>;
  refresh: () => Promise<void>;
  login: (username: string, password: string, totp?: string) => Promise<void>;
  logout: () => Promise<void>;
  // checkSession probes GET /session to restore login state from the httpOnly cookie
  // after a refresh (P5). Sets loggedIn + operator + expiry + csrfToken, or clears them.
  checkSession: () => Promise<void>;
  // Server-authoritative hydration (plan-4, D1): GET /topology → loadTopology overwrites the
  // local canvas. A 404 (no server topology yet — before the first deploy) keeps the local
  // canvas. The success path of login/loginWithPasskey/checkSession all call it. Before
  // overwriting, if the local design is "non-empty and differs from the server", export a backup
  // first (D9).
  hydrateFromServer: () => Promise<void>;
  dismissHydrationNotice: () => void;
  // Passwordless passkey login (plan-5.2): begin → assertLogin → finish.
  loginWithPasskey: (username: string) => Promise<void>;
  // Login-passkey self-service management (valid only for a password session).
  loadPasskeyStatus: () => Promise<void>;
  registerPasskey: () => Promise<void>;
  disablePasskey: () => Promise<void>;
  // Reset the pending second-factor step: when the operator changes the credential pair (or
  // after a hard failure), make the code box appear only for the username+password pair the
  // backend actually flagged, rather than sticking around after the account/password changed.
  resetTOTPChallenge: () => void;
  // TOTP 2FA self-service management (plan-5.2): valid only for a password session (a
  // break-glass token has no account).
  loadTOTPStatus: () => Promise<void>;
  enrollTOTP: () => Promise<TOTPEnrollment>;
  confirmTOTP: (secret: string, code: string) => Promise<void>;
  disableTOTP: (code: string) => Promise<void>;
  mintToken: (nodeId: string, ttl: number) => Promise<MintTokenResult>;
  // enrollOperator pins the off-host operator credential. With NO credential pinned it runs the
  // WebAuthn ceremony + first pin. When one is ALREADY pinned (server truth) it REFUSES to start
  // the ceremony unless opts.rotate is set, instead arming pendingKeystoneRotate so the UI demands
  // an explicit rotate confirmation (rotating strands the fleet until each node is re-provisioned).
  enrollOperator: (opts?: { rotate?: boolean }) => Promise<void>;
  // hydrateKeystoneStatus probes GET /operator-credential and sets the SERVER-authoritative
  // serverOperator* fields, so "enrolled" reflects the server, not a browser-local cache. Best-effort
  // (a transport failure leaves the fields unchanged). Called on connect/login/refresh.
  hydrateKeystoneStatus: () => Promise<void>;
  // cancelKeystoneRotate clears a pending rotate confirmation (the operator declined the rotation).
  cancelKeystoneRotate: () => void;
  // compilePreview (PR6): a server-authoritative read-only compile. POST the current canvas
  // (private keys stripped first) → the server compiles the enrolled subgraph (no stage, no
  // persist) → set the result as compileResult (for CompilePreview / EdgeEditor to display), and
  // merge the server-computed allocations (compiled_port + pinned_*) back onto the canvas so the
  // operator can see them and adjust the NAT ip:port accordingly, then Save persists and Deploy
  // reuses them stickily.
  compilePreview: () => Promise<void>;
  // deploy uploads the current canvas (private keys stripped first) → stage → (keystone
  // signing) → promote. When a deploy would shrink the server design substantially, unless
  // confirmedShrink it sets pendingShrink and holds the deploy (awaiting the typed project-name
  // confirmation).
  deploy: (opts?: { confirmedShrink?: boolean }) => Promise<void>;
  // Cancel a pending shrink-confirm deploy (the user clicked cancel in the confirm dialog).
  cancelShrinkConfirm: () => void;
  // Controller-mode lightweight "save" (plan-10 / T2): strip private keys → update-topology
  // (persist the authoritative copy + version history only, never stage/promote to reach the
  // live fleet) → mark canvasFromServer → refresh the sync snapshot. Before saving it runs a
  // client-side conflict check (re-GET and compare against lastSyncedSnapshot); force=true skips
  // the check and overwrites.
  saveDesign: (opts?: { force?: boolean }) => Promise<void>;
  // Controller-mode "import design" (server-authoritative): parse the JSON file → strip all key
  // material (the controller is the key authority) → update-topology writes to the server (lands
  // a version history entry; the server heals colliding pins along the way) → hydrateFromServer
  // refreshes the canvas from the authoritative copy. Never lands a fleet design in localStorage,
  // and never reaches the live fleet directly (deploy is still the separate stage/sign/promote).
  // Distinguished from local mode's importProject: the latter loads the file into the canvas as a
  // discardable local draft, which lands a secret mirror on disk — not allowed in controller mode.
  importDesignToServer: (file: File) => Promise<void>;
  // Dismiss the save conflict notice (the user cancelled, or it was resolved by re-syncing /
  // force-overwriting).
  dismissSaveConflict: () => void;
  // Dismiss the "stripped N private keys" notice.
  dismissStripNotice: () => void;
  // Clear the controller-mode transient notices (hydration / stripping / pending shrink). Called
  // on a controller→local switch so no controller-mode banner lingers in local mode (plan-5
  // review).
  clearModeNotices: () => void;
  // The unified controller→local switch (plan-10 / T1): converges the security fork ("server
  // mirror → wipe the whole canvas / local original → keep the design, drop keys") into one
  // place, shared by the login gate and the settings page, so two diverging implementations can
  // never leak fleet secrets. It also clears notices, restores the local translucency preference
  // (A3), and sets mode=local. The caller is responsible for the confirm dialog and navigation.
  switchToLocal: () => void;
  switchToController: () => void;
  revoke: (nodeId: string) => Promise<void>;
  rollKeys: () => Promise<void>;
  clearRekey: (nodeId: string) => Promise<void>;
}

// configOf slices the ControllerConfig the controllerClient needs out of the connection fields
// (without agentBaseURL). The EFFECTIVE bearer = the login session if present, else the
// break-glass operatorToken. This way the client layer need not know session vs. token apart —
// it just attaches the operatorToken field as the Bearer.
function configOf(state: ControllerState): ControllerConfig {
  return {
    baseURL: state.baseURL,
    pathPrefix: state.pathPrefix,
    operatorToken: state.sessionToken || state.operatorToken,
    csrfToken: state.csrfToken,
  };
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
function clearServerCanvasAtGate(mode: 'local' | 'controller', lastSyncedSnapshot: string | null): void {
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

// base64StdToBytes decodes STANDARD base64 (padded — the encoding of GET /trustlist's
// trustlist_json) back to raw bytes. Those bytes are the canonical manifest bytes, whose SHA-256
// is the WebAuthn challenge.
//
// FOOTGUN: the input MUST be STANDARD (padded) base64 because it pairs with Go's
// base64.StdEncoding on trustlist_json (handler_controller.go ~:1103) — this is a
// DIFFERENT dialect from the base64url (no-pad) used for every SignedTrustList
// field. atob() here requires the std alphabet + padding; if the Go side ever
// switches trustlist_json to base64url, this mis-decodes and the node rejects
// with ErrChallengeMismatch. Keep both sides on std base64 in lockstep.
function base64StdToBytes(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) {
    out[i] = bin.charCodeAt(i);
  }
  return out;
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

// selectHasLocalSigningKey reports whether THIS browser holds the local signing material (the
// public-key PEM cache) the deploy() signing path needs. A credential can be enrolled on the
// server (serverOperatorPinned) yet absent here — e.g. enrolled on another device or after a
// browser-data clear — in which case the operator must sign on the enrolling device.
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
const serverKeystoneReset = {
  serverOperatorPinned: null,
  serverOperatorAlg: null,
  serverOperatorFingerprint: null,
  serverRedeployRequired: false,
  pendingKeystoneRotate: false,
} as const;

export const useControllerStore = create<ControllerState>()(
  persist(
    (set, get) => ({
      // Default connection config (see DESIGN: operator defaults to :8080, agent to :9090).
      baseURL: 'http://localhost:8080',
      pathPrefix: '',
      agentBaseURL: 'http://localhost:9090',
      operatorToken: '',

      mode: 'local',

      sessionToken: '',
      operatorName: null,
      sessionExpiresAt: null,
      csrfToken: '',
      loggedIn: false,

      totpRequired: false,
      totpEnabled: null,
      passkeyRegistered: null,

      nodes: [],
      audit: [],
      auditVerified: false,
      lastDeploy: null,
      settings: null,

      hydrationNotice: false,
      lastStrippedKeys: 0,
      pendingShrink: null,

      operatorCredentialId: null,
      operatorCredentialAlg: null,
      operatorRpId: null,
      operatorPublicKeyPEM: null,

      // SERVER-authoritative keystone status (not persisted): null = not yet probed ("checking").
      ...serverKeystoneReset,

      loading: false,
      error: null,
      lastSyncedAt: null,
      lastSyncedSnapshot: null,
      lastSyncedTopology: null,
      saveConflict: false,
      saving: false,
      previewing: false,
      signing: false,
      enrolling: false,
      loginCeremony: false,

      setConfig: (partial) => set(partial),

      setMode: (mode) => set({ mode }),

      // Refresh the fleet view: fetch nodes + audit + bootstrap settings in parallel. If any
      // fails, record the error and leave the existing view unchanged. A settings-fetch failure
      // does not affect nodes/audit (best-effort, caught separately).
      refresh: async () => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());
          const [nodes, audit] = await Promise.all([getNodes(cfg), getAudit(cfg)]);
          set({
            nodes,
            audit: audit.entries,
            auditVerified: audit.verified,
            loading: false,
            lastSyncedAt: Date.now(),
          });
          // Also refresh the bootstrap settings (does not block the fleet view; keep the old
          // value on failure). In controller mode the server is the authority for translucency,
          // so once fetched sync it to the appearance store (same as loadSettings), keeping the
          // settings-page checkbox from diverging from the server value.
          try {
            const settings = await getSettings(cfg);
            set({ settings });
            if (get().mode === 'controller') {
              useUiStore.getState().applyServerTranslucency(settings.translucency);
            }
          } catch {
            /* Settings fetch failed: keep the existing settings, do not overwrite the fleet view's success state. */
          }
          // Keystone status is server-authoritative (the panel's "enrolled" source); refresh it
          // alongside the fleet so the display + the rotated-but-not-redeployed banner stay current.
          // Best-effort (hydrateKeystoneStatus swallows its own errors).
          await get().hydrateKeystoneStatus();
        } catch (err) {
          set({
            error: localizeError(err, 'error.generic'),
            loading: false,
          });
        }
      },

      // Load the bootstrap settings (a standalone entry point, used on the settings area's first
      // render).
      loadSettings: async () => {
        try {
          const settings = await getSettings(configOf(get()));
          set({ settings });
          // In controller mode the server is the source of truth for translucency; apply
          // it to the appearance store as the EFFECTIVE value only (applyServerTranslucency
          // leaves the user's local preference intact, so a later controller→local switch
          // restores it rather than inheriting the fleet's appearance — plan-10 / A3). In
          // local mode the client uiStore value stands.
          if (get().mode === 'controller') {
            useUiStore.getState().applyServerTranslucency(settings.translucency);
          }
        } catch (err) {
          set({ error: localizeError(err, 'error.generic') });
        }
      },

      // Save the bootstrap settings: POST /settings, then write back the server-normalized value.
      saveSettings: async (s) => {
        set({ loading: true, error: null });
        try {
          const saved = await postSettings(configOf(get()), s);
          set({ settings: saved, loading: false });
          return null;
        } catch (err) {
          const msg = localizeError(err, 'error.generic');
          set({ error: msg, loading: false });
          return msg;
        }
      },

      // Convenience pin-fetch for the rollout/mimic config cards: wraps the client over the current
      // auth config and rethrows so the card localizes its own error. No global state side effects.
      fetchReleasePins: (body) => ctlFetchPins(configOf(get()), body),

      // Operator password login (plan-5.2): POST /login to obtain a session token, held in memory
      // only. On success, immediately refresh the fleet view. The session takes precedence over a
      // break-glass token (see configOf). On failure, echo the controller's raw error (401
      // invalid username or password / 429 too many attempts).
      login: async (username, password, totp) => {
        set({ loading: true, error: null });
        try {
          const outcome = await ctlLogin(configOf(get()), username, password, totp);
          if (outcome.kind === 'passkey_required') {
            // Password correct but a passkey is required: pop the authenticator in place and
            // resubmit with the assertion (the password is still in the closure). The signing
            // flag drives the "touch your security key" prompt. The whole 2FA passkey step is
            // transparent to the UI — the login form needs no passkey input, the store completes
            // the ceremony automatically.
            const ch = outcome.challenge;
            if (!ch.credentialId || !ch.alg) {
              set({ error: tLocal('controllerStore.passkeyRequiredNoneRegistered'), loading: false });
              return;
            }
            set({ loginCeremony: true });
            try {
              const assertion = await assertLogin(
                ch.challenge,
                ch.credentialId,
                ch.alg,
                ch.rpid || window.location.hostname,
              );
              const after = await ctlLogin(configOf(get()), username, password, undefined, assertion);
              if (after.kind === 'success') {
                set({
                  sessionToken: after.result.sessionToken,
                  csrfToken: after.result.csrfToken,
                  loggedIn: true,
                  operatorName: after.result.operator,
                  sessionExpiresAt: after.result.expiresAt,
                  totpRequired: false,
                  loginCeremony: false,
                  loading: false,
                });
                await get().hydrateFromServer();
                await get().refresh();
                await get().loadTOTPStatus();
                await get().loadPasskeyStatus();
                return;
              }
              // A passkey resubmit should either succeed or throw; anything else is unexpected.
              set({ error: tLocal('controllerStore.passkeyDidNotComplete'), loginCeremony: false, loading: false });
            } catch (err) {
              set({
                error: localizeError(err, 'error.generic'),
                loginCeremony: false,
                loading: false,
              });
            }
            return;
          }
          if (outcome.kind === 'totp_required') {
            // Password correct but a second-factor code is required: let the login form collect a
            // TOTP code and retry. The backend returns the same totp_required for both "missing
            // code" and "wrong code" (no oracle); but locally we know whether a code was
            // submitted — if a code was sent and is still required, it was wrong/expired, so show
            // a gentle hint (the user is already in the 2FA step, so this is not info disclosure).
            // The first time (no code) writes no error, just expands the code box.
            const submittedCode = !!(totp && totp.trim() !== '');
            set({
              totpRequired: true,
              error: submittedCode
                ? tLocal('controllerStore.totpNotAccepted')
                : null,
              loading: false,
            });
            return;
          }
          set({
            sessionToken: outcome.result.sessionToken,
            csrfToken: outcome.result.csrfToken,
            loggedIn: true,
            operatorName: outcome.result.operator,
            sessionExpiresAt: outcome.result.expiresAt,
            totpRequired: false,
            loading: false,
          });
          await get().hydrateFromServer();
          await get().refresh();
          // Fetch this account's 2FA / passkey status (for the "account security" area to echo).
          // A failure does not block login.
          await get().loadTOTPStatus();
          await get().loadPasskeyStatus();
        } catch (err) {
          // Hard failure (wrong password / 429 lockout / network / 500, all thrown before
          // reaching "second-factor required"): reset totpRequired, back to a pure password form
          // — avoiding the mismatched prompt of "wrong username or password" while still showing
          // the code box. The next attempt that genuinely needs a second factor (correct
          // password) will cleanly re-trigger totp_required.
          set({
            error: localizeError(err, 'error.generic'),
            totpRequired: false,
            loading: false,
          });
        }
      },

      // Reset the second-factor step (see the interface comment): clear totpRequired only; the
      // code input's local value is cleared by the component.
      resetTOTPChallenge: () => set({ totpRequired: false }),

      // Server-authoritative hydration (plan-4, D1): the server's topology is the sole authority,
      // and the local cache is just a discardable mirror — overwritten after each login/session
      // restore. On failure (network/parse) it keeps the local canvas and returns quietly:
      // hydration is a side action of login and a single fetch failure must not block login
      // itself; the next login/refresh retries.
      hydrateFromServer: async () => {
        try {
          const raw = await ctlGetTopology(configOf(get()));
          if (raw === null) {
            return; // Server has no topology yet (before the first deploy): keep the local canvas.
          }
          const topo = raw as Topology;
          if (!topo || typeof topo !== 'object' || !topo.project || !topo.domains || !topo.nodes || !topo.edges) {
            return; // Shape mismatch: do not overwrite (the server bytes are guaranteed by update-topology's custody gate; this is just defensive).
          }
          // Record the server-authoritative design's normalized snapshot — the baseline for the
          // dirty indicator + save conflict check (plan-10 / T2). Update it whether or not the
          // local canvas is actually overwritten below (differs/!differs), so the baseline always
          // equals the current server state.
          set({ lastSyncedSnapshot: canonicalDesign(topo), lastSyncedTopology: topo });
          const topoStore = useTopologyStore.getState();
          const local = topoStore.getTopology();
          // Semantic comparison (plan-4 review): compare only the four slices loadTopology
          // actually consumes + the version, and sort object keys before comparing, to avoid
          // false differences from the "server canonical key order" differing from the "frontend
          // getTopology key order" (preserving array order = conservative, prefer over-backup to
          // a miss).
          const differs = stableStringify(loadSlices(local)) !== stableStringify(loadSlices(topo));
          if (!differs) {
            return; // Identical to the local copy: skip the overwrite (do not needlessly clear history/selection).
          }
          // Backup insurance (D9, review fix): whenever an overwrite would discard a local design
          // that is "non-empty and differs from the server", export a backup first — no longer
          // "once per browser" (which would silently drop undeployed local edits from the second
          // time on). In steady state differs=false, so this branch never fires and downloads do
          // not flood; only genuinely divergent undeployed changes are backed up, exactly the
          // scenario to protect. In controller mode "persist=deploy", so undeployed local changes
          // are volatile.
          const localHasWork = local.nodes.length > 0 || local.edges.length > 0;
          if (localHasWork) {
            const stamp = new Date().toISOString().slice(0, 10);
            topoStore.exportProject(`pre-hydration-backup-${stamp}.json`);
            set({ hydrationNotice: true });
          }
          // fromServer=true: this canvas is a server secret mirror, forbidden from hitting disk /
          // must be wiped after logout (see topologyStore.canvasFromServer's security invariant).
          topoStore.loadTopology(topo, true);
        } catch {
          // Fetch failed: keep the local canvas, do not block login (see the function comment).
        }
      },

      dismissHydrationNotice: () => set({ hydrationNotice: false }),

      // Logout: best-effort POST /logout to revoke the server session, then clear the local
      // session + fleet view regardless of success (local logout must take effect even if the
      // network/server revocation fails).
      logout: async () => {
        try {
          // Whether there is an in-memory session or a cookie session (loggedIn), call the server
          // revocation + clear the cookie.
          if (get().sessionToken || get().loggedIn) {
            await ctlLogout(configOf(get()));
          }
        } catch {
          // A revocation failure does not block local logout (the session still expires on the
          // server by its TTL).
        }
        // Capture the sync snapshot before clearing: the set() below sets lastSyncedSnapshot to
        // null, and set is synchronous, so a later get().lastSyncedSnapshot would read null —
        // then the gate's dirty check would treat any non-empty server canvas as dirty, so each
        // logout would wrongly trigger a backup download (plan-10 review). Save the baseline first.
        const snap = get().lastSyncedSnapshot;
        set({
          sessionToken: '',
          csrfToken: '',
          loggedIn: false,
          operatorName: null,
          sessionExpiresAt: null,
          // Clear the 2FA session state: reset totpRequired, return totpEnabled to "unknown", so
          // the next operator who logs in with a password re-fetches their own account's status
          // (TwoFactorSettings's guarded effect re-fires when it is null).
          totpRequired: false,
          totpEnabled: null,
          passkeyRegistered: null,
          nodes: [],
          audit: [],
          auditVerified: false,
          // Clear settings too, so a different operator signing in re-fetches them
          // (the guarded loadSettings effect re-fires on settings===null).
          settings: null,
          error: null,
          // The sync snapshot and conflict flag are derived from the current session/server
          // design: clear them on logout too; the next login's hydrate rebuilds them.
          lastSyncedSnapshot: null,
          lastSyncedTopology: null,
          saveConflict: false,
          // Server-authoritative keystone status is session-derived: reset to "unknown" on logout
          // so the next operator re-probes (never inherits a stale "enrolled" / redeploy banner).
          ...serverKeystoneReset,
        });
        // Security: if the post-logout canvas is a server secret mirror, wipe it immediately
        // (memory + persist also clears localStorage). Otherwise, while logged out, anyone could
        // read the fleet's public IPs and SSH targets out of the canvas/localStorage. Local
        // original work (canvasFromServer=false) is untouched — that is the user's own data.
        // Reuse clearServerCanvasAtGate so the three flush points (logout / session loss /
        // partialize) use the same predicate rather than each expanding it. Pass the snap captured
        // before logout (not the already-nulled get().lastSyncedSnapshot) so the dirty check is
        // accurate.
        clearServerCanvasAtGate(get().mode, snap);
        // A3: the session ended, so the appearance returns to the local preference — the
        // server-pushed fleet translucency should not linger at the logout/login gate.
        useUiStore.getState().restoreLocalTranslucency();
      },

      // Restore the logged-in state after a refresh (P5): GET /session probes the current session
      // via the httpOnly cookie. On a hit set loggedIn + identity + expiry + csrfToken
      // (subsequent state-mutating requests carry X-CSRF-Token off it); on a miss (401/403) clear
      // the logged-in state. A probe failure (network/not configured) also clears loggedIn.
      // Restores only the logged-in state — does not proactively fetch the fleet (the persisted
      // cache colors it instantly; the user presses "connect / refresh" for live state).
      checkSession: async () => {
        try {
          const info = await getSession(configOf(get()));
          // Authed (a cookie session OR a break-glass token both answer 200): refresh the
          // server-authoritative keystone status so the panel never renders a premature/false
          // "Not enrolled" on mount. Best-effort; null info (401/403) leaves it unprobed.
          if (info) {
            await get().hydrateKeystoneStatus();
          }
          // Only a GENUINE cookie session counts as "logged in". GET /session also answers
          // 200 for a break-glass Bearer token (it authenticates operator routes), but
          // break-glass mints no session/CSRF cookie, so its probe returns an EMPTY
          // csrf_token. Gate on a non-empty csrf_token to keep break-glass a recovery path
          // (selectHasAuth still enables Deploy via operatorToken), preserving the
          // "break-glass is not a login" invariant.
          if (info && info.csrfToken !== '') {
            const wasLoggedIn = get().loggedIn;
            set({
              loggedIn: true,
              operatorName: info.operator,
              sessionExpiresAt: info.expiresAt || null,
              csrfToken: info.csrfToken,
            });
            // Server-authoritative hydration (D1): session restore overwrites the local canvas.
            // Two triggers:
            //   (1) the logged-in state goes false→true (mount / refresh restore) — a first entry
            //       always fetches;
            //   (2) logged in but the canvas is not a server mirror (!canvasFromServer) — this is
            //       exactly the "while logged in, local→controller and back" scenario (plan-10 /
            //       A2): the Shell's mode-flip effect calls checkSession again, at which point
            //       wasLoggedIn is still true, so the old logic would not re-fetch and stale local
            //       state would masquerade as the server design. With this condition added,
            //       re-entering controller always re-fetches from the server authority.
            // In steady state (logged in + canvas already a server mirror) both conditions are
            // false, so no needless repeat overwrite.
            if (!wasLoggedIn || !useTopologyStore.getState().canvasFromServer) {
              await get().hydrateFromServer();
            }
          } else {
            // Session lost: capture the baseline before clearing (same ordering fix as logout) so
            // the gate uses the live baseline to judge dirty accurately.
            // Also flush the server-authoritative keystone status (lockstep with logout) so a stale
            // enrolled/redeploy status can't render before the next probe.
            const lostSnap = get().lastSyncedSnapshot;
            set({ loggedIn: false, csrfToken: '', lastSyncedSnapshot: null, lastSyncedTopology: null, saveConflict: false, ...serverKeystoneReset });
            clearServerCanvasAtGate(get().mode, lostSnap);
            useUiStore.getState().restoreLocalTranslucency(); // A3: back at the login gate, use the local appearance preference
          }
        } catch {
          const lostSnap = get().lastSyncedSnapshot;
          set({ loggedIn: false, lastSyncedSnapshot: null, lastSyncedTopology: null, saveConflict: false, ...serverKeystoneReset });
          clearServerCanvasAtGate(get().mode, lostSnap);
          useUiStore.getState().restoreLocalTranslucency();
        }
      },

      // Fetch this account's TOTP status. On a 403 (a break-glass token has no account) or a
      // network error, keep totpEnabled=null (the UI prompts "log in with a password to manage
      // 2FA" off it) without polluting the global error.
      loadTOTPStatus: async () => {
        try {
          set({ totpEnabled: await getTOTPStatus(configOf(get())) });
        } catch {
          set({ totpEnabled: null });
        }
      },

      // Begin enroll: mint a not-yet-activated secret + otpauth URI and return it for the
      // component to display (not persisted before confirmation, and the global state is
      // unchanged). Errors are thrown to the caller, displayed in place by TwoFactorSettings.
      enrollTOTP: async () => {
        return ctlEnrollTOTP(configOf(get()));
      },

      // Confirm enroll: activate 2FA with the secret from enroll + a current code. On success
      // totpEnabled=true. On failure (e.g. wrong code) the error is thrown to the caller and
      // displayed in place by the component.
      confirmTOTP: async (secret, code) => {
        await ctlConfirmTOTP(configOf(get()), secret, code);
        set({ totpEnabled: true });
      },

      // Disable 2FA: requires the current code (to stop a hijacked session from removing the
      // second factor outright). On success totpEnabled=false.
      disableTOTP: async (code) => {
        await ctlDisableTOTP(configOf(get()), code);
        set({ totpEnabled: false });
      },

      // Passwordless passkey login: begin gets the challenge → assertLogin pops the authenticator
      // → finish exchanges it for a session. On failure (no passkey / assertion failed /
      // cancelled) it is displayed in place. On success, refresh the view + fetch the
      // account-security status.
      loginWithPasskey: async (username) => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());
          const ch = await passkeyLoginBegin(cfg, username);
          if (!ch.credentialId || !ch.alg) {
            // Empty allow_credentials = this username has no registered passkey (the backend
            // returns a decoy).
            set({ error: tLocal('controllerStore.noPasskeyRegistered'), loading: false });
            return;
          }
          set({ loginCeremony: true });
          let assertion;
          try {
            assertion = await assertLogin(
              ch.challenge,
              ch.credentialId,
              ch.alg,
              ch.rpid || window.location.hostname,
            );
          } finally {
            set({ loginCeremony: false });
          }
          const result = await passkeyLoginFinish(cfg, username, assertion);
          set({
            sessionToken: result.sessionToken,
            csrfToken: result.csrfToken,
            loggedIn: true,
            operatorName: result.operator,
            sessionExpiresAt: result.expiresAt,
            loading: false,
          });
          await get().hydrateFromServer();
          await get().refresh();
          await get().loadTOTPStatus();
          await get().loadPasskeyStatus();
        } catch (err) {
          set({
            error: localizeError(err, 'error.generic'),
            loading: false,
            loginCeremony: false,
          });
        }
      },

      // Fetch this account's login-passkey status. On a 403 (a break-glass token has no account)
      // or any error, keep it null.
      loadPasskeyStatus: async () => {
        try {
          set({ passkeyRegistered: await getPasskeyStatus(configOf(get())) });
        } catch {
          set({ passkeyRegistered: null });
        }
      },

      // Register a login passkey: reuse the keystone's create() ceremony
      // (enrollOperatorCredential gets the SPKI + alg), then POST /passkey/register to store the
      // public key. Only the public key leaves the authenticator. loginCeremony drives the "touch
      // your security key" prompt (does not trigger DeployBar's deploy banner). Errors are thrown
      // to the caller, displayed in place by PasskeySettings (consistent with TwoFactorSettings's
      // local errors).
      registerPasskey: async () => {
        const rpId = window.location.hostname;
        const origin = window.location.origin;
        set({ loginCeremony: true });
        try {
          const cred = await enrollOperatorCredential(rpId, origin);
          await ctlRegisterPasskey(configOf(get()), {
            alg: cred.alg,
            credentialId: cred.credentialId,
            publicKeyPEM: cred.publicKeyPEM,
            rpId,
            origin,
          });
          set({ passkeyRegistered: true, loginCeremony: false });
        } catch (err) {
          set({ loginCeremony: false });
          throw err;
        }
      },

      // Disable the login passkey (two-stage): begin gets a re-authentication challenge →
      // assertLogin → finish deletes the credential. A fresh assertion is required to stop a
      // hijacked session from removing the factor outright. begin returning done means there was
      // no passkey to begin with (idempotent). Errors are thrown to the caller, displayed in place
      // by PasskeySettings.
      disablePasskey: async () => {
        set({ loginCeremony: true });
        try {
          const cfg = configOf(get());
          const begin = await disablePasskeyBegin(cfg);
          if (begin.kind === 'done') {
            set({ passkeyRegistered: false, loginCeremony: false });
            return;
          }
          const ch = begin.challenge;
          if (!ch.credentialId || !ch.alg) {
            set({ loginCeremony: false });
            throw new Error(tLocal('controllerStore.cannotDisableNoCredential'));
          }
          const assertion = await assertLogin(
            ch.challenge,
            ch.credentialId,
            ch.alg,
            ch.rpid || window.location.hostname,
          );
          await disablePasskeyFinish(cfg, assertion);
          set({ passkeyRegistered: false, loginCeremony: false });
        } catch (err) {
          set({ loginCeremony: false });
          throw err;
        }
      },

      // Mint a one-time enrollment token for a node, returning the plaintext token (visible this
      // once only).
      mintToken: async (nodeId, ttl) => {
        return mintEnrollmentToken(configOf(get()), nodeId, ttl);
      },

      // KEYSTONE enroll (plan-5.1d): pin the off-host operator signing credential (passkey /
      // YubiKey), turning the keystone on. Flow: navigator.credentials.create()
      // (getPublicKey/getPublicKeyAlgorithm get the SPKI + COSE alg, avoiding CBOR) → POST
      // /operator-credential to pin the PKIX PEM + credential_id + rpid(=location.hostname) +
      // origin to the controller. rpid must equal create()'s rp.id — nodes verify
      // SHA256(rpid)==the assertion's rpIdHash. On success only the non-secret
      // credential_id/alg/rpId is left in localStorage, to set allowCredentials for later
      // signatures.
      enrollOperator: async (opts) => {
        const rotate = opts?.rotate === true;
        // Guard the fleet-stranding re-pin: when a credential is ALREADY pinned on the server and
        // the operator has not explicitly confirmed a rotation, do NOT start the WebAuthn ceremony
        // — arm pendingKeystoneRotate so the UI demands confirmation first (a rotation strands every
        // node until each is re-provisioned out of band AND a fresh deploy is signed).
        if (get().serverOperatorPinned === true && !rotate) {
          set({ pendingKeystoneRotate: true, error: null });
          return;
        }
        // rp.id must be the registrable domain (location.hostname); WebAuthn is unavailable in a
        // non-secure context.
        const rpId = window.location.hostname;
        const origin = window.location.origin;
        set({ enrolling: true, error: null });
        try {
          const cred = await enrollOperatorCredential(rpId, origin);
          await postOperatorCredential(configOf(get()), {
            alg: cred.alg,
            credentialId: cred.credentialId,
            publicKeyPEM: cred.publicKeyPEM,
            rpId,
            origin,
            rotate,
          });
          set({
            operatorCredentialId: cred.credentialId,
            operatorCredentialAlg: cred.alg,
            operatorRpId: rpId,
            operatorPublicKeyPEM: cred.publicKeyPEM,
            enrolling: false,
            pendingKeystoneRotate: false,
          });
          // Re-probe server truth (pinned + fingerprint + redeploy-required) and refresh the fleet.
          await get().hydrateKeystoneStatus();
          await get().refresh();
        } catch (err) {
          // A race (the server gained a credential between our status probe and this POST) surfaces
          // as the rotation-ack refusal — arm the confirmation instead of a raw error.
          if (controllerErrorCode(err) === 'keystone_rotation_requires_ack') {
            set({
              enrolling: false,
              pendingKeystoneRotate: true,
              error: null,
            });
            return;
          }
          set({
            error: localizeError(err, 'error.generic'),
            enrolling: false,
          });
        }
      },

      // hydrateKeystoneStatus probes GET /operator-credential and sets the SERVER-authoritative
      // serverOperator* fields. Best-effort: a transport/auth failure leaves the fields as-is (so a
      // transient blip never flips a known status to a false "Not enrolled"). See interface note.
      hydrateKeystoneStatus: async () => {
        try {
          const st = await getOperatorCredentialStatus(configOf(get()));
          set({
            serverOperatorPinned: st.pinned,
            serverOperatorAlg: st.pinned ? st.alg : null,
            serverOperatorFingerprint: st.pinned ? st.fingerprint : null,
            serverRedeployRequired: st.pinned && st.redeployRequired,
          });
        } catch {
          // Leave the prior status untouched — a probe failure is not evidence of "not enrolled".
        }
      },

      cancelKeystoneRotate: () => set({ pendingKeystoneRotate: false }),

      // compilePreview (PR6): a server-authoritative read-only compile. See the interface comment.
      // previewing is its dedicated flag (not the global loading).
      compilePreview: async () => {
        set({ previewing: true, error: null });
        try {
          const cfg = configOf(get());
          // POST the current canvas (zero-knowledge fail-safe: strip private keys; the controller
          // canvas has none anyway).
          const current = useTopologyStore.getState().getTopology();
          const { topo: clean } = stripPrivateKeys(current);
          const resp = await ctlCompilePreview(cfg, JSON.stringify(clean));
          // No enrolled nodes → no rendered configs: clear the preview and give an actionable
          // hint, leaving the canvas alone.
          if (!resp.topology || !resp.wireguard_configs || Object.keys(resp.wireguard_configs).length === 0) {
            useTopologyStore.getState().setCompileResult(null);
            set({
              previewing: false,
              error: tLocal('controllerStore.noEnrolledNodes'),
            });
            return;
          }
          // Display the server-authoritative compile result (CompilePreview / EdgeEditor's
          // "actual post-compile values").
          useTopologyStore.getState().setCompileResult(resp);
          // Merge the server-computed allocations (compiled_port + all pinned_*) back onto the
          // canvas unconditionally (server-authoritative), so the operator immediately sees the
          // internal listen port/IP and configures NAT off it; then Save persists and Deploy
          // reuses them stickily.
          if (resp.topology.edges) {
            useTopologyStore.getState().mergeServerAllocations(resp.topology.edges);
          }
          set({ previewing: false });
        } catch (err) {
          set({ error: localizeError(err, 'error.generic'), previewing: false });
        }
      },

      // Deploy the current topology to the fleet: reuse the same model.Topology JSON shape that
      // topologyStore.compile() sends to /api/compile (getTopology() →
      // {project,domains,nodes,edges,...}), through update-topology → stage → (KEYSTONE signing) →
      // promote → refresh.
      //
      // KEYSTONE branch (plan-5.1d, replacing the old requireUserKey() seam): after stage, GET
      // /trustlist.
      //   - Returns a manifest (keystone ON): base64-decode the standard-base64 trustlist_json to
      //     recover the canonical bytes → signManifest() (challenge = SHA256(those bytes); rpid
      //     binding = nodes verify SHA256(rpid)==rpIdHash) → POST /trustlist-signature → promote.
      //     promote's keystone gate requires a valid off-host signature, else 422 — so it must be
      //     sign-then-promote.
      //   - Returns null (keystone OFF / 404): promote directly (today's behavior, no signature
      //     needed).
      // If keystone is ON but no operator credential is enrolled locally yet, give an actionable
      // error (enroll the signing key first).
      deploy: async (opts) => {
        set({ loading: true, error: null });
        try {
          const cfg = configOf(get());

          // Resolve the design to upload + its stripped-key count. On a confirmed
          // shrink we deploy the SNAPSHOT the warning was computed from (binds the
          // confirmation to what the operator actually saw, not a since-changed
          // canvas); otherwise we strip the live canvas now.
          let cleanTopo: Topology;
          let stripped: number;
          const confirming = opts?.confirmedShrink ? get().pendingShrink : null;
          if (confirming) {
            cleanTopo = confirming.snapshot;
            stripped = confirming.stripped;
          } else {
            // Custody strip (plan-5, D4): never send a private key to the server (the
            // client mirror of the server's update-topology 400). In controller mode
            // the hydrated design is already key-free, but a locally-compiled/imported
            // design could carry keys — this is the fail-safe.
            const local = useTopologyStore.getState().getTopology();
            const r = stripPrivateKeys(local);
            cleanTopo = r.topo;
            stripped = r.stripped;

            // Shrink/empty guard (plan-5): before mutating the server design, compare
            // the canvas against the server copy. Emptying it, or dropping ≥50% of the
            // server's node-IDs, requires a typed confirmation (the audit's
            // one-click-destruction scenario). Version history (plan-2) is the recovery
            // backstop; this is the prevention layer.
            //
            // The server read is best-effort: a 404 means no server topology yet (no
            // shrink possible), and ANY other read failure (5xx/timeout/CSRF) must NOT
            // abort an otherwise-valid deploy — we proceed and rely on version history,
            // rather than blocking a legitimate upload on a transient guard-read error.
            let server: Topology | null = null;
            try {
              server = (await ctlGetTopology(cfg)) as Topology | null;
            } catch {
              server = null; // guard read failed → skip the guard (history is the backstop)
            }
            if (server && Array.isArray(server.nodes) && server.nodes.length > 0) {
              const canvasIds = new Set(cleanTopo.nodes.map((n) => n.id));
              const dropped = server.nodes.filter((n) => !canvasIds.has(n.id)).length;
              const emptied = cleanTopo.nodes.length === 0;
              const majorityDropped = dropped / server.nodes.length >= 0.5;
              if (emptied || majorityDropped) {
                set({
                  pendingShrink: {
                    serverNodeCount: server.nodes.length,
                    canvasNodeCount: cleanTopo.nodes.length,
                    // Never empty: an empty phrase would let an empty input match and
                    // one-click past the gate. Fall back to a typed sentinel.
                    confirmPhrase: cleanTopo.project.name || cleanTopo.project.id || 'DELETE',
                    snapshot: cleanTopo,
                    stripped,
                  },
                  loading: false,
                });
                return; // wait for the typed confirmation, then deploy({confirmedShrink:true})
              }
            }
          }

          const topoJSON = JSON.stringify(cleanTopo);
          await updateTopology(cfg, topoJSON);
          // The design is now the server's authoritative copy. Mark the canvas server-held
          // (even if stage/promote later fails — it IS on the server now) so it stops
          // persisting at rest and is flushed on logout/gate. Without this, a design BUILT
          // locally then deployed (first-deploy: server was empty, so hydrate never set the
          // flag) would leave the live fleet's public IPs + SSH targets readable in
          // localStorage while logged out (review: first-deploy leak).
          //
          // plan-10 / T7: on a CONFIRMED-shrink deploy the UPLOADED design is `confirming
          // .snapshot` (what the warning was computed from), which may differ from the
          // since-edited live canvas. Flipping the flag on the live canvas would mislabel a
          // divergent canvas as server-held, so partialize/gate would later flush those
          // post-warning edits with no backup. Load the snapshot so the canvas equals what
          // was actually uploaded; otherwise just flip the flag (live canvas IS what we sent).
          // (loadTopology also resets compile history + selection — intentional here, the canvas
          // is being replaced by the uploaded snapshot; compile history is empty in controller
          // mode anyway since local compile is refused.)
          if (confirming) {
            useTopologyStore.getState().loadTopology(confirming.snapshot, true);
          } else {
            useTopologyStore.getState().setCanvasFromServer(true);
          }
          // Keep the sync baseline (dirty/conflict — plan-10 / T2) in step with what we just
          // persisted, so a freshly-deployed canvas reads as clean (not dirty). This is the
          // UNPINNED upload; the post-deploy reconciliation below re-bases it to the pinned
          // design once stage's allocation has been read back (and is the fallback baseline
          // if that read fails).
          set({ lastSyncedSnapshot: canonicalDesign(cleanTopo), lastSyncedTopology: cleanTopo });
          const result = await stage(cfg);
          // When there are no enrolled nodes, stage produces no bundle (staged is empty), and
          // promote would then return 409 ErrNoStagedBundle — that is not an error but "no node
          // has joined the network yet". Just show skippedUnenrolled and skip promote (and
          // signing too), so a normal situation is not rendered as an error.
          if (result.staged.length > 0) {
            // KEYSTONE: retrieve the manifest to sign. null = keystone OFF, promote directly.
            const toSign = await getTrustlist(cfg);
            if (toSign !== null) {
              const credentialId = get().operatorCredentialId;
              const alg = get().operatorCredentialAlg;
              const pem = get().operatorPublicKeyPEM;
              if (credentialId === null || alg === null || !pem) {
                // keystone is on (nodes require a signature) but the complete pinned signing
                // credential (credential_id + alg + PEM) is not held locally: this is an
                // actionable precondition failure, not an internal error.
                throw new Error(
                  'This deploy requires an off-host signature, but no operator signing key is enrolled — enroll your signing key first.',
                );
              }
              // rpId must equal the value pinned at enroll time (nodes verify SHA256(rpid)==the
              // assertion's rpIdHash); an old record may lack rpId, so fall back to the current
              // hostname (which is what enroll used).
              const rpId = get().operatorRpId ?? window.location.hostname;
              // Decode the standard-base64 canonical bytes back to raw bytes: their SHA-256 is the
              // WebAuthn challenge (nodes compare base64url(SHA256(Canonical(manifest)))).
              const manifestBytes = base64StdToBytes(toSign.trustlistJson);
              set({ signing: true });
              let signed;
              try {
                signed = await signManifest(manifestBytes, credentialId, alg, rpId, pem);
              } finally {
                set({ signing: false });
              }
              // Before submitting the signature, re-check trustlist_json with the server's
              // substitution guard (echo back the exact standard-base64 bytes we just signed).
              await postTrustlistSignature(cfg, {
                trustlistJson: toSign.trustlistJson,
                signed,
              });
            }
            await promote(cfg);
          }
          // Post-deploy reconciliation (PR1): stage() ran CompileAndStage → persistAllocations,
          // which merged the freshly-allocated compiled_port + pinned_* (ports, transit IPs,
          // link-locals) BY EDGE ID into the STORED topology. Re-GET it and overlay those onto
          // the canvas so the operator immediately SEES the allocated internal port/IP — the
          // value a NAT port-forward must target — WITHOUT a full hydrate that would drop the
          // current selection / open EdgeEditor. Full hydrate only if the node/edge SET diverged
          // (a concurrent edit), where a field overlay would be wrong. Re-base the sync baseline
          // from the reconciled canvas so the freshly-pinned design reads clean (not dirty, and
          // no phantom save-conflict on the next edit). best-effort: a failed re-GET leaves the
          // canvas as the uploaded (unpinned) design — the pins are on the server and a later
          // Save adopts them (non-clobber) or a re-login hydrates them.
          try {
            const persisted = (await ctlGetTopology(cfg)) as Topology | null;
            if (persisted && Array.isArray(persisted.nodes) && Array.isArray(persisted.edges)) {
              const ts = useTopologyStore.getState();
              const canvas = ts.getTopology();
              if (sameIdSet(canvas.nodes, persisted.nodes) && sameIdSet(canvas.edges, persisted.edges)) {
                ts.mergeServerAllocations(persisted.edges);
              } else {
                ts.loadTopology(persisted, true);
              }
              const reconciled = useTopologyStore.getState().getTopology();
              set({ lastSyncedSnapshot: canonicalDesign(reconciled), lastSyncedTopology: reconciled });
            }
          } catch {
            // best-effort (see comment above) — never fail an otherwise-successful deploy on it.
          }
          // Clear any pending shrink-confirm (a confirmed deploy consumes it) and
          // surface how many private keys were stripped before upload (0 = no notice).
          set({ lastDeploy: result, loading: false, pendingShrink: null, lastStrippedKeys: stripped });
          await get().refresh();
        } catch (err) {
          // Clear pendingShrink on failure too: a CONFIRMED-shrink deploy
          // (deploy({confirmedShrink:true})) that throws during update/stage/
          // promote/signature still has pendingShrink set (it is consumed only on
          // the SUCCESS path at the end of try). Leaving it set keeps the
          // full-screen shrink-confirm modal (DeployBar renders solely on
          // pendingShrink) stuck open over the error. Clearing it surfaces the
          // error in the deploy bar and lets the operator retry Deploy, which
          // re-evaluates the shrink guard against the current server state.
          set({
            error: localizeError(err, 'error.generic'),
            loading: false,
            signing: false,
            pendingShrink: null,
          });
        }
      },

      cancelShrinkConfirm: () => set({ pendingShrink: null }),
      dismissStripNotice: () => set({ lastStrippedKeys: 0 }),
      clearModeNotices: () => set({ hydrationNotice: false, lastStrippedKeys: 0, pendingShrink: null }),

      // The single controller→local switch path (plan-10 / T1). The security fork is identical to
      // the login gate LoginPage's:
      //   - canvas is a server secret mirror (canvasFromServer) → flushWorkspace wipes it whole
      //     (never let the fleet's public IPs / SSH targets linger in local / localStorage across
      //     the switch);
      //   - canvas is local original work → purgeModeBoundaryState keeps the design, drops
      //     keys/allocations/compile history (the D6 lossy switch).
      // SettingsPage previously called purgeModeBoundaryState only (no serverHeld fork), which
      // downgraded a secret mirror to persistable local data → fleet secrets in localStorage
      // (audit T1). Converged here, the two can no longer diverge. Also: restore the local
      // translucency preference (A3, the server-pushed value must not carry into local mode) +
      // clear the controller banners.
      switchToLocal: () => {
        const topo = useTopologyStore.getState();
        // A server mirror goes through clearServerCanvasAtGate (mode is still controller at this
        // moment): it exports a backup before flushing when the mirror is dirty, sharing the same
        // "back up unsaved changes before flushing" logic as the logout/session-loss paths
        // (plan-10 / T2). Local original work goes through the D6 lossy purge (keep the design,
        // drop keys/allocations/history).
        if (topo.canvasFromServer) clearServerCanvasAtGate(get().mode, get().lastSyncedSnapshot);
        else topo.purgeModeBoundaryState();
        get().clearModeNotices();
        useUiStore.getState().restoreLocalTranslucency();
        // Switching back to local: clear the controller-mode sync snapshot and conflict flag
        // (server-authoritative concepts).
        // About the fleet-view cache (nodes/audit/lastDeploy/lastSyncedAt): deliberately NOT
        // cleared here. It is a non-secret advisory cache (only nodeId/status/codename/timestamps,
        // no keys, no design-level public IP/SSH), and in local mode the fleet/overview routes are
        // already redirected by the RequireControllerMode guard and will not display it (plan-11 /
        // T5's render-gate is the real fix). Clearing it would instead break the partialize-design
        // "instant coloring on re-entering controller": a session-preserved
        // controller→local→controller round-trip does not trigger refresh (checkSession only
        // hydrates, does not fetch the fleet), so an empty fleet would show until a manual refresh
        // (plan-11 review #2/#3). So the cache is preserved.
        set({ mode: 'local', lastSyncedSnapshot: null, lastSyncedTopology: null, saveConflict: false });
      },

      // local→controller switch. Deliberately NOT a blanket key purge: the asymmetry vs switchToLocal
      // is intentional (an accidental controller-click must not wipe a valid local design's private
      // keys — that data-loss footgun is why this path historically did nothing and let the login gate
      // + server hydration take over). Instead, clear ONLY the stranded pubkey-only nodes (public key
      // present, no private key) — useless in either mode and the source of the per-node un-pin chore
      // when a pubkey-only file was imported in local mode. Valid keypairs are untouched; once logged
      // in, server hydration replaces the canvas anyway. Notices are cleared for a clean controller entry.
      switchToController: () => {
        const ts = useTopologyStore.getState();
        ts.clearStrandedKeys();
        // Drop any LOCAL air-gap compile result on the way into controller mode (review should-fix #1).
        // A local compile carries reconstructed REAL private keys in compileResult, and the Deploy
        // page renders CompilePreview whenever compileResult is present — so a stale local result would
        // surface real private keys inside controller mode, the very boundary controller mode exists to
        // protect. compileResult is never persisted, so clearing it here closes the only in-memory path
        // (local compile → toggle to controller); a controller Compile re-populates it after entry with
        // placeholder-key configs.
        ts.setCompileResult(null);
        get().clearModeNotices();
        set({ mode: 'controller' });
      },

      // Controller-mode "save" (plan-10 / T2): persist the current canvas as the
      // server-authoritative copy (+ version history), but never stage/promote — the live fleet is
      // untouched. This is the lightweight persistence primitive missing beyond deploy(), so
      // undeployed in-progress work no longer lives only in a discardable mirror (lost on refresh
      // / logout).
      saveDesign: async (opts) => {
        // loading = the global busy flag (consistent with other actions); saving = specific to
        // this save, driving the Save button / conflict dialog, so the global loading set by an
        // unrelated op does not light it up (plan-11 review #1). Both must be cleared at every exit.
        set({ loading: true, saving: true, error: null });
        try {
          const cfg = configOf(get());
          // Zero-knowledge fail-safe: as in deploy(), strip private keys before upload (the
          // controller canvas has none anyway; this is the backstop).
          const current = useTopologyStore.getState().getTopology();
          let { topo: clean, stripped } = stripPrivateKeys(current);
          // no-op guard: if the design equals the last sync baseline, return immediately — neither
          // sending a network request nor needlessly adding a same-content version history entry on
          // the server (the backend does no content dedup, and the extra version would also push
          // out a genuinely old one). force skips it. Use isDesignDirty: a null baseline with an
          // empty canvas (first time, nothing to save) is also correctly judged "not dirty".
          if (!opts?.force && !isDesignDirty(current, get().lastSyncedSnapshot)) {
            set({ loading: false, saving: false });
            return;
          }
          // Re-GET the server design — for BOTH the non-clobber pin merge and the conflict check.
          // best-effort: a read failure skips both guards and writes `clean` (update-topology has
          // no optimistic-concurrency token; version history — plan-2 — is the backstop). The read
          // runs on force too, so even a force-Save adopts server pins before it overwrites.
          let readOk = true;
          let serverNow: Topology | null = null;
          try {
            serverNow = (await ctlGetTopology(cfg)) as Topology | null;
          } catch {
            readOk = false;
          }
          // Client-side conflict detection (D13): unless force, ignore pin fields when comparing
          // "current server state vs. the last sync baseline". This way "a deploy elsewhere only
          // added pins" (pin-only difference, adopted by the non-clobber merge below) does not
          // falsely report a conflict, while a genuine concurrent change (nodes/edges/non-pin
          // fields) still triggers "re-sync / overwrite / cancel". It must be decided before the
          // merge below: a conflict returns early and does not touch the canvas — otherwise a
          // cancelled Save would still silently overlay the server pins onto the canvas, leaving a
          // third state the user did not ask for. best-effort: a failed guard read skips conflict
          // detection and saves as usual (consistent with the deploy shrink guard), avoiding false
          // reports on a transient network error; this save then blind-writes the overwrite
          // (update-topology has no backend optimistic concurrency, see D13).
          if (!opts?.force && readOk) {
            const baseTopo = get().lastSyncedTopology;
            const baseNoPins = baseTopo ? canonicalDesignIgnoringPins(baseTopo) : null;
            const serverNoPins = serverNow ? canonicalDesignIgnoringPins(serverNow) : null;
            if (serverNoPins !== baseNoPins) {
              set({ loading: false, saving: false, saveConflict: true });
              return;
            }
          }
          // Non-clobber pin adoption (PR1): if the server carries freshly-allocated NAT ports/IPs
          // (compiled_port + pinned_*) that NEITHER the canvas NOR the last-synced base had — a
          // deploy on another tab, or one whose post-promote reconcile failed — adopt them onto the
          // canvas so this Save does not drop them and break the configured NAT forward. Operator-set
          // / operator-unpinned values win (lastSyncedTopology is the 3-way base). Runs only PAST the
          // conflict gate above (and on force, which skips that gate), so a cancelled/conflicted Save
          // never mutates the canvas — this is also what makes even a force-Save non-clobbering.
          // Recompute `clean` from the merged canvas before writing.
          if (readOk && serverNow && Array.isArray(serverNow.edges)) {
            const base = get().lastSyncedTopology;
            const ts = useTopologyStore.getState();
            ts.mergeServerAllocations(serverNow.edges, base ? base.edges : []);
            const reread = stripPrivateKeys(ts.getTopology());
            clean = reread.topo;
            stripped = reread.stripped;
          }
          await updateTopology(cfg, JSON.stringify(clean));
          // The canvas is now the server-authoritative copy: mark it server-held (stop hitting
          // disk, wipe on logout/gate), refresh the sync snapshot (reset dirty) + the timestamp,
          // and record the stripped private-key count (0=no notice).
          useTopologyStore.getState().setCanvasFromServer(true);
          set({
            loading: false,
            saving: false,
            saveConflict: false,
            lastStrippedKeys: stripped,
            lastSyncedSnapshot: canonicalDesign(clean),
            lastSyncedTopology: clean,
            lastSyncedAt: Date.now(),
          });
        } catch (err) {
          set({ error: localizeError(err, 'error.generic'), loading: false, saving: false });
        }
      },

      dismissSaveConflict: () => set({ saveConflict: false }),

      importDesignToServer: async (file) => {
        set({ loading: true, error: null });
        try {
          const text = await file.text();
          let topo: Topology;
          try {
            topo = JSON.parse(text) as Topology;
          } catch {
            set({ error: t(useTopologyStore.getState().language, 'topbar.importParseError'), loading: false });
            return;
          }
          // Shape validation: must be a complete design (same criteria as importProject).
          // domains/nodes/edges must be arrays — use Array.isArray rather than a truthiness check,
          // or a {} or a number would slip past validation and throw a generic error at
          // dropAllKeys's .map rather than giving the precise "not a valid design" hint.
          if (
            !topo ||
            typeof topo !== 'object' ||
            !topo.project ||
            !Array.isArray(topo.domains) ||
            !Array.isArray(topo.nodes) ||
            !Array.isArray(topo.edges)
          ) {
            set({ error: t(useTopologyStore.getState().language, 'topbar.importShapeError'), loading: false });
            return;
          }
          // Reserved feature route_policies: strip it if non-empty (consistent with
          // importProject; semantic validation rejects a non-empty array).
          if (Array.isArray(topo.route_policies) && topo.route_policies.length > 0) {
            delete topo.route_policies;
          }
          // The controller is the key authority: drop all key material from the file (private keys
          // are never uploaded; public keys come from each agent enrollment), clearing it before
          // writing to the server so a private key never enters the request body even for an
          // instant.
          const { topo: cleaned } = dropAllKeys(topo);
          // Write to the server: update-topology lands a version history entry and heals colliding
          // pins + normalizes at the write boundary. Never stage/promote — an import only updates
          // the authoritative design; reaching the fleet still needs a separate Deploy.
          await updateTopology(configOf(get()), JSON.stringify(cleaned));
          // Optimistic load (the already-healed imported design, fromServer=true so it does not
          // hit disk): even if the subsequent hydrateFromServer fails on a transient network
          // error, the canvas already reflects this import rather than staying on the pre-import
          // stale design (the POST already succeeded).
          useTopologyStore.getState().loadTopology(cleaned, true);
          // Then align the canvas and the sync baseline with the server-authoritative copy
          // (already healed + normalized); on success this overwrites the optimistic value above.
          await get().hydrateFromServer();
          set({ loading: false });
        } catch (err) {
          set({ error: localizeError(err, 'error.generic'), loading: false });
        }
      },

      // Refresh the view after evicting a node.
      revoke: async (nodeId) => {
        set({ loading: true, error: null });
        try {
          await revoke(configOf(get()), nodeId);
          set({ loading: false });
          await get().refresh();
        } catch (err) {
          set({
            error: localizeError(err, 'error.generic'),
            loading: false,
          });
        }
      },

      // Request a WG key rotation for the whole fleet (plan-4.6 ROUTINE tier): mark every approved
      // node rekey_requested, then refresh the view (the registry shows a rekeying badge). This is
      // only the first step of the zero-knowledge rotation flow — each agent regenerates its own
      // key and registers the new public key via /rekey; once the nodes have re-registered, the
      // operator must Deploy again, and the new generation of configs carrying everyone's new
      // public keys converges the fleet.
      rollKeys: async () => {
        set({ loading: true, error: null });
        try {
          await rekeyAll(configOf(get()));
          set({ loading: false });
          await get().refresh();
        } catch (err) {
          set({
            error: localizeError(err, 'error.generic'),
            loading: false,
          });
        }
      },

      // Clear a single node's pending-rotation flag without evicting it (unlike revoke: it
      // preserves the approval status and bearer token), then refresh the view so the rekeying
      // badge/count converges. Used to release a stuck "Roll keys" straggler (a dead/offline node,
      // or a mis-clicked full rotation) that would otherwise keep pinning the panel's Deploy gate.
      clearRekey: async (nodeId) => {
        set({ loading: true, error: null });
        try {
          await clearRekey(configOf(get()), nodeId);
          set({ loading: false });
          await get().refresh();
        } catch (err) {
          set({
            error: localizeError(err, 'error.generic'),
            loading: false,
          });
        }
      },
    }),
    {
      name: 'controller-storage',
      // Persist only the connection endpoints + the non-secret identifiers of the pinned operator
      // signing credential (credential_id/alg/rpId/public-key PEM are none of them key material —
      // the private key never leaves the authenticator). Never persist operatorToken /
      // sessionToken / CSRF (no secrets in localStorage), nor loading / error / signing.
      //
      // The non-secret cache P4 added (mode / nodes / settings / lastSyncedAt) is only for
      // "instant coloring" after a refresh. nodes carries only non-secret fields like
      // nodeId/status/codename/timestamps, no key material. The cache is advisory: the one place
      // nodes participates in gating (selectRekeyingCount → DeployBar disables Deploy while nodes
      // are rotating) is fail-closed — after a reload a stale cache at most "disables" Deploy,
      // never "lets through" a deploy that live state should have blocked; refresh() converges once
      // it has the live state. The controller backend is still the final authority at
      // stage/promote.
      partialize: (state) => ({
        baseURL: state.baseURL,
        pathPrefix: state.pathPrefix,
        agentBaseURL: state.agentBaseURL,
        operatorCredentialId: state.operatorCredentialId,
        operatorCredentialAlg: state.operatorCredentialAlg,
        operatorRpId: state.operatorRpId,
        operatorPublicKeyPEM: state.operatorPublicKeyPEM,
        mode: state.mode,
        nodes: state.nodes,
        settings: state.settings,
        lastSyncedAt: state.lastSyncedAt,
      }),
    }
  )
);
