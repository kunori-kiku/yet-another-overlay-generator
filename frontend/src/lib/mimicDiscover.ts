// mimicDiscover.ts — the pure logic behind the mimic-catalog "Discover from release" checklist
// (beta9-smoke-hardening plan-4). Kept out of the component so it is unit-testable in the node
// vitest env (the project has no jsdom/RTL). The component owns the React state + rendering; this
// module owns the two decisions that must be correct: how to prefill a label from an asset name,
// and which selected labels collide (and so must block "Add selected").

// deriveKey best-effort prefills a "<codename>-<arch>" catalog key from a discovered .deb asset
// name that follows the "<codename>_mimic_<...>_<arch>.deb" convention. It returns '' for a dkms
// package (arch-independent kernel-module source — the operator must label it) or any name that
// does not match, so the operator supplies the label explicitly rather than accepting a wrong
// guess. The derived key still passes through DEB_KEY_RE validation + the collision guard before use.
export function deriveKey(asset: string): string {
  if (asset.includes('dkms')) return '';
  const m = /^([a-z0-9]+)_mimic_.*_([a-z0-9]+)\.deb$/.exec(asset);
  return m ? `${m[1]}-${m[2]}` : '';
}

// collidingKeys returns the set of CHECKED labels that collide — with another checked label OR an
// already-present deb-row label. The mimic catalog keys its pins by "<codename>-<arch>" and the
// save path keeps the LAST occurrence of a duplicate (a mislabel — e.g. two arches both typed the
// same, or a -dkms row sharing a label — would silently drop a real pin), so "Add selected" must be
// blocked until every checked label is unique. Empty labels are NOT reported here (the caller flags
// "needs a label" separately); only non-empty collisions are returned.
export function collidingKeys(checkedKeys: string[], existingKeys: string[]): Set<string> {
  const dup = new Set<string>();
  const seen = new Set(existingKeys.map((k) => k.trim()).filter(Boolean));
  for (const raw of checkedKeys) {
    const k = raw.trim();
    if (!k) continue;
    if (seen.has(k)) dup.add(k);
    seen.add(k);
  }
  return dup;
}
