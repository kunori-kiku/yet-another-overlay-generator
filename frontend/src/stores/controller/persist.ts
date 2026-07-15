// THE custody gate. This module holds the ONE auditable persistence allowlist (partialize) that
// decides what the controllerStore writes to localStorage — deliberately kept as a single
// definition and NOT fragmented across the slices, because it is the zero-knowledge custody
// boundary (invariant [6]): it must never leak fleet secrets / key material to disk. Any change to
// what persists is reviewed HERE, in one place.

import type { ControllerState } from './types';
import { stripLiveTelemetry } from '../../lib/custody';
import { localOnly } from '../../lib/localOnly';

// The localStorage key the controllerStore persists under.
export const PERSIST_NAME = 'controller-storage';

// partialize is the custody allowlist. Persist only the connection endpoints + the non-secret
// public descriptor of the pinned operator signing credential (credential_id/alg/rpId/public-key
// PEM). YAOG never receives plaintext private-key material, but requests no attestation and makes
// no claim that a provider cannot export/synchronize it. Never persist operatorToken / sessionToken
// / CSRF (no secrets in localStorage), nor loading / error / signing.
//
// The non-secret cache P4 added (mode / nodes / settings / lastSyncedAt) is only for
// "instant coloring" after a refresh. nodes carries only non-secret fields like
// nodeId/status/agentVersion/timestamps, no key material. The cache is advisory: rekey state
// drives a warning/confirmation rather than an authorization gate, and refresh() converges it
// to live server truth. The controller backend remains authoritative at stage/promote.
//
// Per-node LIVE telemetry (beta.12 wireguardPeers) is stripped via stripLiveTelemetry — it
// carries raw peer endpoints (fleet-confidential) and a frozen handshake age is stale on
// reload; the aggregate wireguard condition in `conditions` (curated, endpoint-free) stays
// for instant coloring, and the per-link detail is re-fetched live on refresh.
export function partialize(state: ControllerState) {
  return {
    baseURL: state.baseURL,
    pathPrefix: state.pathPrefix,
    agentBaseURL: state.agentBaseURL,
    operatorCredentialId: state.operatorCredentialId,
    operatorCredentialAlg: state.operatorCredentialAlg,
    operatorRpId: state.operatorRpId,
    operatorPublicKeyPEM: state.operatorPublicKeyPEM,
    mode: state.mode,
    nodes: state.nodes.map(stripLiveTelemetry),
    settings: state.settings,
    lastSyncedAt: state.lastSyncedAt,
  };
}

// merge replicates the persist middleware's default (shallow-merge persisted state over
// the freshly-built initial state), then COERCES mode to 'local' in the static-local-design
// build (VITE_LOCAL_ONLY). Without this, a localStorage 'controller' written by the
// all-in-one build and then loaded by the static site (same origin re-hosted, or a stale
// cache) would resurface controller mode the build is supposed to lock out. The default
// build keeps the persisted mode verbatim (the standard rehydration).
export function merge(persisted: unknown, current: ControllerState): ControllerState {
  const merged = { ...current, ...(persisted as Partial<ControllerState>) };
  if (localOnly()) merged.mode = 'local';
  return merged;
}
