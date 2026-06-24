// Single source of truth for the per-role CATEGORICAL hue used across the canvas: the node card
// border + fill, the connection handles, and the MiniMap dot. Previously roleColors,
// roleHandleColorClass (CustomNode), and the MiniMap nodeColor switch each hardcoded their OWN
// role→color list, which is exactly the kind of triple that drifts. One map keeps them in lockstep.
//
// The hue is categorical identity (kept the same in light AND dark — it labels the role, it is not a
// theme surface), so these stay raw colors, not semantic tokens. The Tailwind class strings live here
// as literals so the content scanner still generates them. The `hex` is for the MiniMap, which takes a
// raw color callback (no Tailwind there).
export interface RoleHue {
  border: string; // Tailwind border class for the node card
  fill: string; // Tailwind low-alpha bg class for the node card
  handle: string; // Tailwind classes (important-prefixed) for the connection handle
  hex: string; // raw hex for the MiniMap nodeColor callback
}

export const ROLE_HUE: Record<string, RoleHue> = {
  peer: { border: 'border-green-500', fill: 'bg-green-500/20', handle: '!border-green-500 !bg-green-500', hex: '#22c55e' },
  router: { border: 'border-blue-500', fill: 'bg-blue-500/20', handle: '!border-blue-500 !bg-blue-500', hex: '#3b82f6' },
  relay: { border: 'border-yellow-500', fill: 'bg-yellow-500/20', handle: '!border-yellow-500 !bg-yellow-500', hex: '#eab308' },
  gateway: { border: 'border-purple-500', fill: 'bg-purple-500/20', handle: '!border-purple-500 !bg-purple-500', hex: '#a855f7' },
  client: { border: 'border-cyan-500', fill: 'bg-cyan-500/20', handle: '!border-cyan-500 !bg-cyan-500', hex: '#06b6d4' },
};

export const DEFAULT_ROLE = 'peer';

// roleHue resolves a role to its hue, falling back to the peer hue for any unknown role (so a new
// role never renders blank — same total-over-input contract the old `|| roleColors.peer` had).
export function roleHue(role: string): RoleHue {
  return ROLE_HUE[role] ?? ROLE_HUE[DEFAULT_ROLE];
}
