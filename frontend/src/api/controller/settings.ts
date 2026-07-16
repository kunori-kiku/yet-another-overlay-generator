// Bootstrap-settings client routes (plan-5.2, server-persisted): read + full-replace write of the
// operator-editable agent bootstrap config (public agent URL, GitHub proxy, agent release base, the
// signed self-update rollout, and the mimic .deb catalog). Also owns AgentPin / MimicDebPin (the
// pinned-artifact shapes; release.ts imports AgentPin for its fetch result).

import { request, postJSON, type ControllerConfig } from './transport';

// --- bootstrap settings (plan-5.2) ---

// AgentPin is one integrity-pinned release asset: the asset filename and the SHA-256 the agent
// verifies the downloaded bytes against before exec. Mirrors renderer.Artifact (Go) and the map
// values of agent_bins / mimic_debs on the wire.
export interface AgentPin {
  asset: string;
  sha256: string;
}

// MimicDebPin is one mimic-catalog row's pinned PAIR: the userspace `mimic` .deb (asset/sha256) and
// its companion `mimic-dkms` .deb (dkmsAsset/dkmsSha256, absent on a legacy mimic-only row). Mirrors
// model.MimicDebPin (Go) and the map values of mimic_debs on the wire. Distinct from AgentPin (which
// backs agent_bins) so the dkms companion never leaks into the agent-bins path.
export interface MimicDebPin {
  asset: string;
  sha256: string;
  dkmsAsset?: string;
  dkmsSha256?: string;
}

// MimicDebPinJSON is the wire form of a mimic_debs entry (snake_case dkms_* companion fields).
interface MimicDebPinJSON {
  asset: string;
  sha256: string;
  dkms_asset?: string;
  dkms_sha256?: string;
}

// ControllerSettings is the operator-editable, server-persisted bootstrap config: the public
// agent URL (where nodes curl the bootstrap / enroll), an optional GitHub proxy prefix (default
// off), the agent-binary release base URL, the signed agent self-update rollout, and the mimic
// GitHub-.deb catalog. POST /settings is FULL-REPLACE — see postSettings.
export interface ControllerSettings {
  publicAgentURL: string;
  githubProxy: string;
  agentReleaseBaseURL: string;
  // translucency is the panel appearance preference (P5), served server-side via
  // GET/POST /settings. It is NOT part of the agent bootstrap script.
  translucency: boolean;
  // agentPathPrefix is READ-ONLY, server-reported (YAOG_AGENT_PATH_PREFIX,
  // normalized '' or '/<seg>'): the prefix agent-facing URLs mount under. The panel
  // composes the bootstrap one-liner / enroll command from it — never from the
  // operator-prefix mirror, which belongs to the panel's own API base.
  agentPathPrefix: string;
  // Signed agent self-update rollout (controller-panel-rollout-ui). All NON-SECRET pins.
  // EMPTY targetAgentVersion ⇒ no self-update (the safety contract). agentBins maps
  // "linux-<arch>" to the pinned asset; canary/fleet-wide stage the canary-then-fleet rollout.
  targetAgentVersion: string;
  minAgentVersion: string;
  agentBins: Record<string, AgentPin>;
  agentCanaryNodeIds: string[];
  agentRolloutFleetWide: boolean;
  // Mimic GitHub-.deb catalog. mimicDebs maps "<codename>-<arch>" to the pinned .deb; empty
  // mimicReleaseBase ⇒ distro-only mimic (no GitHub fallback).
  mimicVersion: string;
  mimicReleaseBase: string;
  mimicDebs: Record<string, MimicDebPin>;
  // Fleet-wide mimic→UDP fallback policy a tcp link inherits ('' / 'udp' / 'none'). plan-4; UI in plan-6.
  mimicFallbackDefault: string;
  // Per-node telemetry-history record target (telemetry-history plan-2). null ⇒ server default
  // (DefaultTelemetryHistoryCap, 20160 ≈ 7 days at 30s beats); 0 ⇒ history disabled; N ⇒ target N.
  // The controller's independent 128 MiB per-node physical ceiling may retain fewer variable-width
  // records. A nullable number mirrors the Go *int (nil pointer omitted on the wire → default; an explicit 0
  // disables). Validated >= 0 and <= 1_000_000 client-side (server authoritative).
  telemetryHistoryCap: number | null;
}

// SettingsJSON mirrors settingsJSON in internal/api/handler_bootstrap.go. The rollout + mimic
// fields are omitempty on the wire (Go), hence optional here; mapSettings supplies safe defaults.
interface SettingsJSON {
  public_agent_url: string;
  github_proxy: string;
  agent_release_base_url: string;
  translucency: boolean;
  agent_path_prefix?: string;
  target_agent_version?: string;
  min_agent_version?: string;
  agent_bins?: Record<string, AgentPin>;
  agent_canary_node_ids?: string[];
  agent_rollout_fleet_wide?: boolean;
  mimic_version?: string;
  mimic_release_base?: string;
  mimic_debs?: Record<string, MimicDebPinJSON>;
  mimic_fallback_default?: string;
  // Go *int with omitempty: absent (nil) ⇒ default; an explicit number (incl. 0 = disabled) present.
  telemetry_history_cap?: number;
}

// mapMimicDebs / mimicDebsToJSON convert the two-package mimic catalog between the camelCase UI type
// and the snake_case wire (an empty dkms_* string on the wire becomes undefined, so a mimic-only row
// round-trips without a phantom companion). Kept beside the settings mappers.
function mapMimicDebs(w?: Record<string, MimicDebPinJSON>): Record<string, MimicDebPin> {
  const out: Record<string, MimicDebPin> = {};
  for (const [k, v] of Object.entries(w ?? {})) {
    out[k] = { asset: v.asset, sha256: v.sha256, dkmsAsset: v.dkms_asset || undefined, dkmsSha256: v.dkms_sha256 || undefined };
  }
  return out;
}
function mimicDebsToJSON(m: Record<string, MimicDebPin>): Record<string, MimicDebPinJSON> {
  const out: Record<string, MimicDebPinJSON> = {};
  for (const [k, v] of Object.entries(m)) {
    out[k] = {
      asset: v.asset,
      sha256: v.sha256,
      ...(v.dkmsAsset ? { dkms_asset: v.dkmsAsset } : {}),
      ...(v.dkmsSha256 ? { dkms_sha256: v.dkmsSha256 } : {}),
    };
  }
  return out;
}

export function mapSettings(d: SettingsJSON): ControllerSettings {
  return {
    publicAgentURL: d.public_agent_url,
    githubProxy: d.github_proxy,
    agentReleaseBaseURL: d.agent_release_base_url,
    translucency: d.translucency,
    agentPathPrefix: d.agent_path_prefix ?? '',
    targetAgentVersion: d.target_agent_version ?? '',
    minAgentVersion: d.min_agent_version ?? '',
    agentBins: d.agent_bins ?? {},
    agentCanaryNodeIds: d.agent_canary_node_ids ?? [],
    agentRolloutFleetWide: d.agent_rollout_fleet_wide ?? false,
    mimicVersion: d.mimic_version ?? '',
    mimicReleaseBase: d.mimic_release_base ?? '',
    mimicDebs: mapMimicDebs(d.mimic_debs),
    mimicFallbackDefault: d.mimic_fallback_default ?? '',
    // A number (incl. 0) is honored; an absent field (nil *int on the wire) maps to null ⇒ default.
    telemetryHistoryCap: typeof d.telemetry_history_cap === 'number' ? d.telemetry_history_cap : null,
  };
}

// emptyControllerSettings is the all-unset initial value for a controlled settings form before the
// server record loads: the rollout + mimic fields mirror mapSettings's omitempty defaults, while
// translucency intentionally seeds the server's default-on appearance (the real GET always carries a
// concrete translucency + a defaulted release base, so this is never produced from a live response).
// Shared so each settings form does not re-spell the full field set (and they stay in sync as fields grow).
export function emptyControllerSettings(): ControllerSettings {
  return {
    publicAgentURL: '',
    githubProxy: '',
    agentReleaseBaseURL: '',
    translucency: true,
    agentPathPrefix: '',
    targetAgentVersion: '',
    minAgentVersion: '',
    agentBins: {},
    agentCanaryNodeIds: [],
    agentRolloutFleetWide: false,
    mimicVersion: '',
    mimicReleaseBase: '',
    mimicDebs: {},
    mimicFallbackDefault: '',
    telemetryHistoryCap: null,
  };
}

// toSettingsJSON maps the FULL ControllerSettings to its wire form. Every persisted field is
// included because POST /settings is FULL-REPLACE: the server rebuilds ControllerSettings purely
// from the body (handler_bootstrap.go), so any omitted field is persisted as its zero value — an
// omit-list literal here would silently WIPE the rollout/mimic config on an unrelated edit. The
// read-only agent_path_prefix is deliberately NOT sent (server-derived; POST ignores it).
export function toSettingsJSON(s: ControllerSettings): SettingsJSON {
  return {
    public_agent_url: s.publicAgentURL,
    github_proxy: s.githubProxy,
    agent_release_base_url: s.agentReleaseBaseURL,
    translucency: s.translucency,
    target_agent_version: s.targetAgentVersion,
    min_agent_version: s.minAgentVersion,
    agent_bins: s.agentBins,
    agent_canary_node_ids: s.agentCanaryNodeIds,
    agent_rollout_fleet_wide: s.agentRolloutFleetWide,
    mimic_version: s.mimicVersion,
    mimic_release_base: s.mimicReleaseBase,
    mimic_debs: mimicDebsToJSON(s.mimicDebs),
    mimic_fallback_default: s.mimicFallbackDefault,
    // Omit the cap when null (server keeps its default via a nil *int); send the number otherwise
    // (0 rides through as an explicit "disabled"). The full-replace contract holds either way.
    ...(s.telemetryHistoryCap !== null ? { telemetry_history_cap: s.telemetryHistoryCap } : {}),
  };
}

// getSettings reads the current bootstrap settings (defaults applied server-side).
export async function getSettings(cfg: ControllerConfig): Promise<ControllerSettings> {
  const res = await request(cfg, 'settings', { method: 'GET' });
  return mapSettings((await res.json()) as SettingsJSON);
}

// postSettings saves the bootstrap settings and returns the stored values. It sends the FULL
// settings (toSettingsJSON) — POST is full-replace, so a caller editing one field must still
// round-trip every other field or it is wiped (see toSettingsJSON).
export async function postSettings(cfg: ControllerConfig, s: ControllerSettings): Promise<ControllerSettings> {
  const res = await postJSON(cfg, 'settings', JSON.stringify(toSettingsJSON(s)));
  return mapSettings((await res.json()) as SettingsJSON);
}
