// Canonical English message catalog — the SINGLE source of valid message keys.
// `keyof typeof en` defines MessageKey (see ../index.ts); every other language is a
// Partial of these keys and falls back to English per-key, so a missing translation
// never renders blank.
//
// Naming convention:
//   - shared / cross-component strings: flat camelCase (migrated from the former
//     positional STRINGS table — e.g. navOverview, themeToggleLabel);
//   - component-local strings: a 'component.name' namespace (e.g. nodeEditor.title)
//     so parallel migration of different components cannot collide on a key.
// Parameterized messages use {param} placeholders, interpolated by t(): e.g.
//   'deployBar.rekeying': '{count} node(s) still rotating keys'  ->  t(lang, 'deployBar.rekeying', { count })
//
// Error-code messages (localizing backend {code} envelopes via tError) live under the
// 'error.' namespace; 'error.generic' is the last-resort fallback.
export const en = {
  publicAddressLabel: "Public address (how peers reach this server, IP:port)",
  publicAddressPlaceholder: "e.g. 203.0.113.10:51820",
  publicAddressHint: "Leave empty if this node is behind NAT (peers dial in to it).",
  autoLayoutLabel: "Auto layout",
  showInterfacesLabel: "Show interfaces",
  portPendingLabel: "auto",
  addBackupLink: "Add backup link",
  backupEndpointNudge: "Point the backup at a distinct endpoint for path diversity (it copied the primary’s address).",
  roleLabel: "Link role",
  rolePrimary: "Primary",
  roleBackup: "Backup",
  duplicateChip: "duplicate?",
  mimicHint: "Wraps this link as TCP via mimic for networks that throttle or block UDP. Both ends must be Linux (eBPF); MTU is auto-lowered. Not a censorship/DPI-circumvention feature.",
  xdpModeLabel: "mimic XDP mode",
  xdpModeHint: "Affects only transport=tcp (mimic) links. Some VPS NICs do not support native XDP, so skb is the default; choose native only if you know this NIC supports it.",
  brandName: "YAOG Console",
  primaryNavLabel: "Primary",
  skipToContent: "Skip to content",
  navOverview: "Overview",
  navDesign: "Design",
  navFleet: "Fleet",
  navDeploy: "Deploy",
  navSecurity: "Security",
  navSettings: "Settings",
  sidebarCollapse: "Collapse sidebar",
  sidebarExpand: "Expand sidebar",
  themeToggleLabel: "Toggle theme",
  themeSystem: "System",
  themeLight: "Light",
  themeDark: "Dark",
  userMenuLabel: "Account",
  overviewTopologyHeading: "Topology",
  overviewControllerHeading: "Controller Fleet",
  overviewDomains: "Domains",
  overviewNodes: "Nodes",
  overviewEdges: "Edges",
  overviewFleetNodes: "Registered nodes",
  overviewLastDeploy: "Last deploy",
  overviewLastSynced: "Last synced",
  overviewNotSynced: "Not synced yet — connect a controller in Settings.",
  settingsModeHeading: "Mode",
  settingsModeHint: "Choose the local/manual workflow or the controller fleet workflow.",
  modeLocal: "Local / Manual",
  modeController: "Controller",
  settingsAppearanceHeading: "Appearance",
  compileHistoryTitle: "Compile History",
  chClearHistory: "Clear History",
  chExposureAudit: "Global Exposure Audit",
  chExposedNodes: "Exposed Nodes (Public / Relays)",
  chNoExposedNodes: "No exposed nodes",
  chListensOnPort: "Listens on port",
  chInboundPaths: "Inbound allowed paths",
  chNetworkStats: "Network Statistics",
  chTotalNodes: "Total Nodes",
  chEncryptedEdges: "Encrypted Edges",
  chCurrentChecksum: "Current Checksum",
  chCompilationHistory: "Compilation History",
  chCurrentWorkingState: "Current Working State",
  chReadyToExport: "Ready to export",
  chSnapshot: "Snapshot",
  chNoHistory: "No history available. Compile the project first.",
  chConfigDiff: "Configuration Diff",
  chCurrentVsSnapshot: "(Current vs Snapshot)",
  chSelectFileToDiff: "Select a file to diff...",
  chNoWgConfigs: "no wg configs",
  chSelectFromDropdown: "Select a file from the dropdown to view differences.",
  chFileNotInCurrent: "File not found in current compilation.",
  chFileNotInBoth: "File does not exist in both states.",
  deployControllerHint: "Controller deploy: stage, sign, and promote in the deploy bar below.",
  toolbarLists: "Domains & Nodes",
  fleetBack: "← Back to fleet",
  fleetNodeDetailTitle: "Node detail",
  fleetNodeNotFound: "Node not found — connect and refresh the controller in Settings.",
  appearanceTheme: "Theme",
  appearanceTranslucency: "Translucency",
  appearanceTranslucencyHint: "Off uses solid surfaces (a plainer minimalism).",
  connectRefresh: "Connect / Refresh",

  // Backend error-code localization (see tError). The per-code keys ('error.<code>')
  // are added as backend codes land; 'error.generic' is the shape-agnostic fallback.
  'error.generic': 'Something went wrong. Please try again.',
  // Per-action fallbacks for readApiErrorMessage when the response body is NOT JSON
  // (proxy HTML 502/504, auth redirect, empty body) — keyed so they respect the UI
  // language instead of the former mixed zh/en literals.
  'error.validateFailed': 'Validation failed',
  'error.compileFailed': 'Compile failed',
  'error.exportFailed': 'Export failed',
  'error.deployScriptFailed': 'Failed to generate deploy script',
} as const;
