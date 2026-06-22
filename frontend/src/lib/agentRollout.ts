// agentRollout.ts holds the PURE agent-rollout logic the AgentUpdateSettings card needs, factored
// out of the .tsx so it is unit-testable in the node-env vitest without a React/jsdom render — the
// same pattern as nodeConditions.ts / mimicCond.ts. Two concerns live here:
//
//   1. AGENT_VERSION_RE — the single client-side mirror of the backend's semverPattern
//      (handler_bootstrap.go). It is the ONE version grammar the panel uses, shared by the
//      AgentUpdateSettings field validation AND the controller-version usability check, so the two
//      can never drift to different notions of "valid version".
//   2. The one-click "update all agents to the controller version" orchestration, expressed as a
//      pure async function over injected effects so its contract (gate → set target → assist →
//      arm fleet-confirm ONLY on success, and NEVER save) is verifiable without rendering.

// AGENT_VERSION_RE mirrors internal/api/handler_bootstrap.go semverPattern verbatim: an optional
// leading "v", a numeric major.minor.patch, and an optional dot/plus pre-release+build tail.
export const AGENT_VERSION_RE = /^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)*$/;

// isUsableControllerVersion decides whether the controller's reported version is something the panel
// can roll agents TO. It must gate on real-semver VALIDITY, not mere non-emptiness: an unstamped
// controller reports the literal "dev" (NewControllerHandler normalizes an empty BuildVersion to
// "dev"), which is non-empty but not a release the agent can fetch. This mirrors the backend
// refuse-newer guard, which likewise gates on semverPattern.MatchString(controllerVersion) — so a
// dev/non-semver controller version disables the one-click affordance instead of arming a doomed
// rollout to a tag that does not exist.
export function isUsableControllerVersion(controllerVersion: string): boolean {
  return controllerVersion !== '' && AGENT_VERSION_RE.test(controllerVersion);
}

// RolloutEffects are the side effects planUpdateAllToControllerVersion drives — injected so the
// orchestration is pure (the component supplies its React state setters + store actions).
export interface RolloutEffects {
  // setTarget seeds the rollout target with the controller version (so a later Save persists it).
  setTarget: (version: string) => void;
  // assist fetches the release pins for the given version; resolves true on success, false on
  // failure. It MUST receive the version explicitly (not read stale React state) so the assist and
  // the eventual save agree on the version.
  assist: (version: string) => Promise<boolean>;
  // armFleetConfirm opens the existing fleet-wide confirm modal (the operator still confirms + Saves).
  armFleetConfirm: () => void;
}

// planUpdateAllToControllerVersion is the one-click "roll the whole fleet to the version this panel
// ships" orchestration (plan-8): if the controller version is usable, set it as the target, assist
// its pins, and — ONLY if that succeeded — arm the fleet-wide confirm. It deliberately has NO save
// effect: custody requires the operator to review the fetched pins and Save explicitly. A
// non-usable (empty / "dev" / non-semver) controller version is a no-op.
export async function planUpdateAllToControllerVersion(
  controllerVersion: string,
  effects: RolloutEffects,
): Promise<void> {
  if (!isUsableControllerVersion(controllerVersion)) return;
  effects.setTarget(controllerVersion);
  const ok = await effects.assist(controllerVersion);
  if (ok) effects.armFleetConfirm();
}
