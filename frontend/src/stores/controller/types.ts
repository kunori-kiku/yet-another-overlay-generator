// The ControllerState shape (the single ControllerState interface, moved verbatim) plus the set/get
// aliases the slice creators are typed against. Keeping ONE ControllerState interface means each
// slice's get() sees the whole state (cross-slice action calls resolve), and the composed
// create() literal is the single checkpoint that the union of the slices equals ControllerState.

import type { StoreApi } from 'zustand';
import type {
  ControllerNode,
  ControllerAuditEntry,
  StageResult,
} from '../../types/controller';
import type {
  ControllerConfig,
  WebAuthnAlg,
  ControllerSettings,
  TOTPEnrollment,
  MintTokenResult,
  AgentPinFetchRequest,
  AgentPinFetchResult,
  ReleaseAssetsRequest,
  ReleaseAssetsResult,
} from '../../api/controllerClient';
import type { NodeHistory, NodeHistoryRequestOptions } from '../../lib/telemetryHistory';
import type {
  DeployPreview,
  DeployForceArg,
  TelemetryPolicyDeployMode,
  TelemetryPolicyUpgradeOffer,
} from '../../lib/deployPreview';
import type { Topology } from '../../types/topology';
import type { WebAuthnCredentialCandidate } from '../../lib/webauthn';

// Controller panel (Mode B) state. It is the single source of truth for the controller
// connection + fleet view, independent of topologyStore (which remains the sole source of
// truth for topology data). On deploy() it reads the current topology from topologyStore and
// reuses the same model.Topology JSON shape that compile() sends.
export interface ControllerState {
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
  // controllerVersion is the controller's own build version, surfaced on /session and login
  // (plan-7/8): a real semver on a stamped release, the literal "dev" on an unstamped build, or ""
  // only when an older controller predates the field. The user menu displays it verbatim; the
  // agent-update panel uses a real-semver value as the one-click "update all agents" target + the
  // refuse-newer advisory (treating "dev"/non-semver as "no version to match"). Memory only, never
  // persisted (server truth).
  controllerVersion: string;
  // Authenticated additive topology-schema handshake. Empty for older controllers; memory-only.
  controllerCapabilities: string[];

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
  // login() pops the authenticator in place and resubmits (loginCeremony drives the UI).
  passkeyRegistered: boolean | null;

  // fleet view
  nodes: ControllerNode[];
  audit: ControllerAuditEntry[];
  auditVerified: boolean;
  lastDeploy: StageResult | null;

  // Pre-deploy preview (plan-6): the read-only dry-run fetched when the operator initiates a Deploy
  // (POST /deploy-preview with the current canvas). Non-null ⇒ the confirmation dialog is open showing
  // "N update / M unchanged" + the per-node Force surface. TRANSIENT + never persisted (not in the
  // partialize allowlist) — a preview is a one-shot operator action, and it carries live digest
  // verdicts that are stale on reload (the same custody rule as the live telemetry). deployPreviewing
  // is the fetch-in-flight flag (distinct from the global loading, so an unrelated op does not flip it).
  deployPreview: DeployPreview | null;
  deployPreviewing: boolean;
  // Mode bound to the currently open preview. This is transient operator intent, not server state;
  // it must survive preview confirmation and the shrink-confirm continuation without being persisted.
  deployPreviewMode: TelemetryPolicyDeployMode;
  // deployPreviewError is the narrow old-controller compatibility state: a newer panel POSTed the
  // preview route but received 404/405 because that route is absent. Non-null ⇒ DeployBar offers a
  // no-preview fallback. Validation/security/server/network failures use the global blocking error
  // instead and must never expose "Deploy anyway".
  // TRANSIENT + never persisted (same custody rule as deployPreview).
  deployPreviewError: string | null;
  // Set only for the exact structured telemetry_policy_upgrade_required precondition and bound to
  // the successor-policy fingerprint that produced it. Independent from unrelated global errors.
  telemetryPolicyUpgradeOffer: TelemetryPolicyUpgradeOffer | null;

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
    // Force argument (plan-6) captured with the held deploy, so the confirmed-shrink continuation
    // (deploy({confirmedShrink:true})) re-stages with the SAME force the operator chose in the
    // preview dialog rather than silently dropping it. undefined ⇒ a plain (unforced) deploy.
    force?: DeployForceArg;
    // The rollout projection reviewed before this shrink confirmation. A confirmed continuation must
    // stage with the same mode or phase one would silently revert to the blocked normal deployment.
    telemetryPolicyMode: TelemetryPolicyDeployMode;
  } | null;

  // KEYSTONE (plan-5.1d): the pinned operator-held signing credential (passkey / YubiKey).
  // Only its public descriptor is persisted — credential_id (base64url(rawId)), alg, rpId — so
  // the panel can drive later assertions across a refresh (allowCredentials) and echo "signing
  // key registered". The browser/controller never receive plaintext private-key material, but
  // YAOG requests no attestation and therefore does not claim the provider cannot export, back
  // up, or synchronize it. All fields are null when not enrolled.
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
  // Exact public descriptor returned by the authenticated server-authoritative status probe.
  // These fields are held in memory (not persisted) for the manual AgentHeld kit workflow: the
  // panel may copy/download this already-loaded PUBLIC credential, but must never infer trust from
  // a candidate node bundle. WebAuthn uses rpId/origin; raw ed25519 leaves both null.
  serverOperatorRpId: string | null;
  serverOperatorOrigin: string | null;
  serverOperatorPublicKeyPEM: string | null;
  serverOperatorFingerprint: string | null;
  serverRedeployRequired: boolean;
  // pendingKeystoneRotate gates the dangerous re-pin: when a credential is ALREADY pinned on the
  // server, enrollOperator() refuses to start the WebAuthn ceremony and instead sets this true so
  // the UI can demand an explicit rotate confirmation (the pinned key is already shown in the
  // enrolled chip); only enrollOperator({rotate:true}) proceeds. (Mirrors pendingShrink as a gate.)
  pendingKeystoneRotate: boolean;
  // Volatile public descriptors retained only after create() and before successful server
  // persistence. They let a cancelled/failed second phase retry the exact candidate rather than
  // silently creating duplicate passkeys. Never persisted; cleared on logout/session loss.
  pendingKeystoneEnrollment: WebAuthnCredentialCandidate | null;
  pendingLoginPasskeyEnrollment: WebAuthnCredentialCandidate | null;

  // Monotonic, transient generation of the active controller/authentication context. Every
  // controller-bound async action captures it (with its request config) and stops after an await
  // if logout, session loss, identity change, controller-target change, or workflow-mode change
  // increments it. This prevents an old response from repopulating state/canvas and prevents a
  // multi-step mutation from continuing under a later target/session/mode. Never persisted.
  authGeneration: number;

  // volatile UI state
  loading: boolean;
  error: string | null;
  lastSyncedAt: number | null;
  // Dedicated freshness clock for the Fleet node snapshot. Unlike lastSyncedAt, this is never
  // advanced by topology saves or other controller work, so Live telemetry cannot look fresh merely
  // because an unrelated mutation succeeded. Transient: rebuilt by the first Fleet observation.
  lastFleetSyncedAt: number | null;
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

  // fetchReleaseAssets runs the assisted release-asset DISCOVERY fetch (POST release-assets) over
  // the current controller config. It returns the .deb asset names a config CARD lets the operator
  // pick from — it neither persists nor auto-trusts anything (custody) and does NOT touch global
  // loading/error; the caller surfaces its own busy/localError. Throws ControllerError on a coded
  // failure.
  fetchReleaseAssets: (body: ReleaseAssetsRequest) => Promise<ReleaseAssetsResult>;
  // fetchNodeHistory runs the operator node-history read (GET node-history) over the current
  // controller config and RETURNS the parsed series for a component to render. LIVE-ONLY: it neither
  // sets store state nor persists (the fetched history must never enter localStorage — the
  // stripLiveTelemetry custody rule); it does NOT touch global loading/error (the caller owns its own
  // busy/empty/error state). Throws ControllerError on a coded failure.
  fetchNodeHistory: (
    nodeId: string,
    from: string,
    to: string,
    step?: string,
    options?: NodeHistoryRequestOptions,
  ) => Promise<NodeHistory>;
  // Background/manual Fleet observation. Unlike refresh(), it never touches the global mutation
  // loading/error gate. true means this request installed current server truth; false means it was
  // skipped or superseded by a newer/context-changing read. A current transport/API failure rejects.
  refreshFleetView: () => Promise<boolean>;
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
  hydrateFromServer: (opts?: { reportError?: boolean }) => Promise<boolean>;
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
  // (private keys stripped first) → the server compiles the deployment-ready subgraph (no stage, no
  // persist) → set the result as compileResult (for CompilePreview / EdgeEditor to display), and
  // merge the server-computed allocations (compiled_port + pinned_*) back onto the canvas so the
  // operator can see them and adjust the NAT ip:port accordingly, then Save persists and Deploy
  // reuses them stickily.
  compilePreview: () => Promise<void>;
  // openDeployPreview (plan-6): POST the CURRENT canvas (private keys stripped, exactly what deploy()
  // pushes) to /deploy-preview and set deployPreview so the DeployBar renders the confirmation dialog.
  // It is what the Deploy button now triggers (instead of deploying immediately) — the operator reviews
  // "N update / M unchanged" + picks any Force, then confirms. Only a 404/405 route-compatibility
  // response sets deployPreviewError and offers "Deploy anyway"; every other failure blocks deploy.
  openDeployPreview: (mode?: TelemetryPolicyDeployMode) => Promise<void>;
  // cancelDeployPreview clears the preview dialog / error banner (the operator dismissed it without
  // deploying).
  cancelDeployPreview: () => void;
  // deploy uploads the current canvas (private keys stripped first) → stage(force) → (keystone
  // signing) → promote. force (plan-6) re-stages nodes even when unchanged (forceAll / forceNodes),
  // chosen in the preview dialog. When a deploy would shrink the server design substantially, unless
  // confirmedShrink it sets pendingShrink (carrying the force) and holds the deploy (awaiting the
  // typed project-name confirmation).
  deploy: (opts?: {
    confirmedShrink?: boolean;
    force?: DeployForceArg;
    telemetryPolicyMode?: TelemetryPolicyDeployMode;
    // True only for the old-controller 404/405 no-preview fallback. The deploy action rechecks the
    // exact snapshot and refuses successor policy so a later canvas edit cannot lose unknown fields.
    legacyPreviewFallback?: boolean;
  }) => Promise<void>;
  // forceRedeployNode (plan-6) re-stages ONE node even if unchanged, then promotes — the node-detail
  // escape hatch (clear-rekey precedent). It reuses deploy() with force_nodes:[nodeId] so the
  // keystone sign step is not reimplemented; the current design is what gets deployed.
  forceRedeployNode: (nodeId: string) => Promise<void>;
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
  // downloadManualNodeBundle downloads a MANUAL node's promoted, signed install bundle as a ZIP and
  // triggers a browser download. Manual nodes have no agent, so the operator installs by hand.
  downloadManualNodeBundle: (nodeId: string) => Promise<void>;
}

// The set/get the slice creators receive. They are the store's canonical setState/getState, so
// each slice's set can write ANY ControllerState field (cross-slice sets are verbatim from the
// original single create() literal) and each slice's get() returns the whole state (cross-slice
// action calls resolve).
export type ControllerSet = StoreApi<ControllerState>['setState'];
export type ControllerGet = StoreApi<ControllerState>['getState'];
