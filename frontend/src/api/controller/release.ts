// Assisted release-pin + release-asset discovery client routes (plan-1). Convenience-only: the
// fetched sidecar pins/assets are for REVIEW — trust stays the signed artifacts.json the agent
// verifies against; nothing here is auto-trusted or persisted. AgentPin is imported from ./settings
// (the pinned-asset shape it shares with the saved catalog).

import { postJSON, type ControllerConfig } from './transport';
import type { AgentPin } from './settings';

// AgentPinFetchRequest is the body of the assisted release-pin fetch (POST release-pins, plan-1):
// kind selects the asset grammar + default base; version optionally pins a "latest" base to a tag;
// base optionally overrides the saved base; assets may be empty for kind='agent' (certified arches
// derived server-side).
export interface AgentPinFetchRequest {
  kind: 'agent' | 'mimic';
  version?: string;
  base?: string;
  assets: { key: string; asset: string }[];
}

// AgentPinFetchResult is the resolved pins + resolution metadata. versionApplied is REQUIRED by
// the rollout-UI contract: when true, base is the TAGGED url the pins were computed against and
// the UI must persist it as the agent release base — the agent fetches the verbatim saved base
// with no latest→tag rewrite, so a tagged pin + a moving "latest" base is a fail-closed hash
// mismatch (see the release_pins.go Base doc + the outline decisions log).
export interface AgentPinFetchResult {
  pins: Record<string, AgentPin>;
  base: string;
  version: string;
  versionApplied: boolean;
  proxyApplied: boolean;
  resolved: Record<string, string>;
}

// fetchPins calls the operator release-pins endpoint to pre-fill artifact pins for REVIEW. The
// fetched sidecar is convenience-only transport; trust stays the signed artifacts.json the agent
// verifies against. A coded error surfaces as ControllerError for tError to localize.
export async function fetchPins(cfg: ControllerConfig, body: AgentPinFetchRequest): Promise<AgentPinFetchResult> {
  const res = await postJSON(cfg, 'release-pins', JSON.stringify(body));
  const d = (await res.json()) as {
    pins?: Record<string, AgentPin>;
    base: string;
    version: string;
    version_applied: boolean;
    proxy_applied: boolean;
    resolved?: Record<string, string>;
  };
  return {
    pins: d.pins ?? {},
    base: d.base,
    version: d.version,
    versionApplied: d.version_applied,
    proxyApplied: d.proxy_applied,
    resolved: d.resolved ?? {},
  };
}

// ReleaseAssetsRequest is the body of the assisted release-asset DISCOVERY fetch (POST
// release-assets): base optionally overrides the saved mimic release base. There is NO version —
// which release is listed is determined entirely by the base (a ".../releases/latest/..." base
// lists latest; a ".../releases/download/<tag>" base lists that tag). The kind is implicitly mimic.
export interface ReleaseAssetsRequest {
  base?: string;
}

// ReleaseAssetsResult is the discovered .deb asset names + the CANONICAL download base. assets is the
// list of *.deb names the release publishes (debug sidecars excluded server-side); the operator picks
// from it. base is the server-normalized ".../releases/latest/download" | ".../releases/download/<tag>"
// form the install fetches from — the panel adopts it so a loosely-typed base becomes install-valid.
export interface ReleaseAssetsResult {
  assets: string[];
  base: string;
}

// fetchReleaseAssets calls the operator release-assets endpoint to LIST a GitHub release's .deb
// asset names (the controller hits the GitHub REST API directly — never the gh-proxy), so the mimic
// catalog can offer a pick-from checklist instead of hand-typed filenames. Discovery is a convenience
// only — the SHA-256 pin is still fetched (per-row Assist) and saved separately; nothing is trusted
// or persisted here. A coded error surfaces as ControllerError.
export async function fetchReleaseAssets(
  cfg: ControllerConfig,
  body: ReleaseAssetsRequest,
): Promise<ReleaseAssetsResult> {
  const res = await postJSON(cfg, 'release-assets', JSON.stringify(body));
  const d = (await res.json()) as {
    assets?: string[];
    base?: string;
  };
  return {
    assets: d.assets ?? [],
    base: d.base ?? '',
  };
}
