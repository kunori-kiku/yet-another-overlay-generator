import type { ControllerNode } from '../types/controller';
import type { ControllerSettings } from '../api/controllerClient';

// updateStatus.ts derives a node's agent-self-update status for the Fleet chip
// (controller-panel-rollout-ui plan-5). deriveUpdateState is a PURE function (now is injected so it
// is deterministic in a test) and exported so a future FE test runner can cover it cheaply.

export type UpdateState = 'off' | 'not-targeted' | 'pending' | 'applying' | 'applied' | 'failed' | 'stale';

// STALE_MS: a node that is below target and has not checked in for this long is shown 'stale'
// rather than 'pending' (we can't trust it is still progressing). Generous vs the agent poll
// cadence so a healthy node is never falsely flagged. A node mid-update goes 'applying', not stale.
const STALE_MS = 3 * 60 * 1000;

// deriveUpdateState maps a node + the configured rollout to one chip state. The lastHealth markers
// are quoted VERBATIM from internal/agent/selfupdate.go and matched as SUBSTRINGS (only 'applied'
// is a true prefix), ordered failed -> applying -> applied; version compare is the fallback ONLY
// when no marker is present (an older version-aware agent reports a version but no self-update
// health line). See PRINCIPLES.md / the outline Principle 7: 'failed' is best-effort (the
// 'abandoned:' line is transient, overwritten by the next routine apply report).
export function deriveUpdateState(
  node: ControllerNode,
  settings: ControllerSettings | null,
  now: number = Date.now(),
): UpdateState {
  const target = settings?.targetAgentVersion.trim() ?? '';
  if (target === '') return 'off'; // empty target ⇒ no self-update (the safety contract) ⇒ no chip
  if (!node.inRollout) return 'not-targeted';

  const health = node.lastHealth || '';
  if (health.includes(' abandoned:')) return 'failed'; // "self-update to <v> abandoned: <reason>"
  if (health.includes('health-confirmed (probationary)')) return 'applying';
  if (health.startsWith('self-updated to ')) return 'applied';

  // No self-update health marker: the reported running version vs the target is authoritative.
  if (node.agentVersion.trim() && compareSemver(node.agentVersion, target) >= 0) return 'applied';

  // Below target (or an unknown reported version): pending — unless the node has gone quiet, in
  // which case we cannot trust it is progressing (a mid-update node returned 'applying' above, so
  // it is never mislabeled stale here).
  if (isStale(node.lastSeen, now)) return 'stale';
  return 'pending';
}

// isStale reports whether lastSeen (RFC3339) is older than STALE_MS. A zero/never value
// ("0001-01-01...") and an empty string count as stale (the node has never reported).
function isStale(lastSeen: string, now: number): boolean {
  if (!lastSeen || lastSeen.startsWith('0001-01-01')) return true;
  const t = Date.parse(lastSeen);
  if (Number.isNaN(t)) return false; // an unparseable timestamp is not treated as stale
  return now - t > STALE_MS;
}

interface ParsedSemver {
  core: [number, number, number];
  pre: string[];
}

// parseSemver parses "[v]MAJOR.MINOR.PATCH[-prerelease][+build]" into a comparable form, or null if
// it is not a 3-part numeric core. Build metadata is ignored (it does not affect precedence).
function parseSemver(v: string): ParsedSemver | null {
  let s = v.trim();
  if (s.startsWith('v') || s.startsWith('V')) s = s.slice(1);
  const plus = s.indexOf('+');
  if (plus >= 0) s = s.slice(0, plus);
  const dash = s.indexOf('-');
  const coreStr = dash >= 0 ? s.slice(0, dash) : s;
  const preStr = dash >= 0 ? s.slice(dash + 1) : '';
  const parts = coreStr.split('.');
  if (parts.length !== 3) return null;
  const core: number[] = [];
  for (const p of parts) {
    if (!/^[0-9]+$/.test(p)) return null;
    core.push(parseInt(p, 10));
  }
  return { core: core as [number, number, number], pre: preStr === '' ? [] : preStr.split('.') };
}

// compareSemver returns <0 / 0 / >0 for a<b / a==b / a>b per SemVer 2.0.0 precedence: numeric core,
// then a release (no prerelease) outranks a prerelease, then dot-separated prerelease identifiers
// compared numerically when both are numeric (so -beta.2 < -beta.10, which a naive string compare
// gets wrong) else lexically, with fewer identifiers ranking lower when all else is equal. An
// unparseable/empty version sorts BELOW any valid one, so an unknown reported version is never
// judged "applied".
export function compareSemver(a: string, b: string): number {
  const pa = parseSemver(a);
  const pb = parseSemver(b);
  if (!pa && !pb) return 0;
  if (!pa) return -1;
  if (!pb) return 1;
  for (let i = 0; i < 3; i++) {
    if (pa.core[i] !== pb.core[i]) return pa.core[i] < pb.core[i] ? -1 : 1;
  }
  if (pa.pre.length === 0 && pb.pre.length === 0) return 0;
  if (pa.pre.length === 0) return 1; // 1.0.0 > 1.0.0-beta
  if (pb.pre.length === 0) return -1;
  const n = Math.min(pa.pre.length, pb.pre.length);
  for (let i = 0; i < n; i++) {
    const ai = pa.pre[i];
    const bi = pb.pre[i];
    const aNum = /^[0-9]+$/.test(ai);
    const bNum = /^[0-9]+$/.test(bi);
    if (aNum && bNum) {
      const av = parseInt(ai, 10);
      const bv = parseInt(bi, 10);
      if (av !== bv) return av < bv ? -1 : 1;
    } else if (aNum !== bNum) {
      return aNum ? -1 : 1; // a numeric identifier ranks below an alphanumeric one
    } else if (ai !== bi) {
      return ai < bi ? -1 : 1;
    }
  }
  if (pa.pre.length !== pb.pre.length) return pa.pre.length < pb.pre.length ? -1 : 1;
  return 0;
}
