# Plan 4.6 — Key rotation: "Roll the keys" (manual + periodic)

Parent: [plan-4-2026_06_08.md](plan-4-2026_06_08.md). Owner request (2026-06-08): a panel button that
rotates **all node WireGuard private keys** fleet-wide, plus a **periodic** (scheduled) rotation option.
Queued AFTER the panel (PR-B) and intertwined with the Plan-5 signing keystone (key material is trust
material). Not started — design captured here.

## How it must work in the zero-knowledge model

The controller NEVER holds node private keys, so rotation is **agent-driven**:

1. Operator clicks **Roll keys** (or the schedule fires) → controller marks a fleet **rekey epoch**.
2. Each agent, on its next poll, sees the rekey signal, **generates a NEW WG keypair** locally
   (private stays on the node, 0600), and **registers the new public key** with the controller via an
   authenticated re-key call (per-node bearer token; mirrors enrollment but updates the pubkey on an
   already-approved node — NOT a new membership).
3. Once the controller has collected new pubkeys, it **recompiles** and bumps the generation so every
   node re-pulls configs carrying peers' NEW public keys.
4. Nodes apply at the new generation. **Expect brief per-link flap** during the rolling apply (a peer
   holding the old pubkey rejects the new one until it too applies) — acceptable for a deliberate roll;
   document it. (A zero-flap rotation would need dual-key WG interfaces — out of scope for v1.)

## Security tiering (ties to [[security-model-keystone]])

- **Rolling EXISTING members' keys = routine tier.** The membership SET is unchanged (same nodes), so
  this can be automated / step-up-signed, which is what makes a **periodic** schedule possible without a
  human hardware signature every cycle. The user signs a **rotation policy** once (interval); scheduled
  rolls execute under that pre-authorization.
- **Adding/removing a member = membership tier** (human hardware/Bitwarden signature) — unchanged; a
  roll never adds/removes members, only refreshes existing ones' keys.
- Open question to resolve at build time: whether even a routine roll should re-sign the trust-list with
  the off-host key (recommended once the keystone lands), since the pubkey set changes.

## Surfaces

- **Agent:** a `rekey` capability (generate new WG key → register new pubkey → let the normal pull
  re-apply). Reuses `EnsureKey` (force-regenerate variant) + the bearer-token client.
- **Controller:** a rekey-epoch flag per fleet + a `POST /nodes/{id}/pubkey` (or `/rekey`) authenticated
  re-key endpoint (per-node token; updates pubkey on an approved node, audited) + the recompile/bump.
- **Scheduler:** periodic rotation (interval in tenant config); the controller triggers epochs.
- **Panel:** a **Roll keys** button (sensitive-op step-up seam) + a periodic-rotation toggle/interval.

## Out of scope (v1 / until built)

Zero-flap dual-key rotation; per-node selective rotation UI (v1 = whole fleet); rotation of the operator
token / the off-host signing key (separate concern).
