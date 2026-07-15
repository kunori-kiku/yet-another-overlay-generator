// Pure command builder for the manual AgentHeld workflow. Trust input is the controller store's
// already-hydrated, server-authoritative operator PUBLIC descriptor — never a file from the
// candidate bundle. Keeping construction pure makes every security-sensitive flag table-testable.

export const MANUAL_KIT_CREDENTIAL_FILENAME = 'yaog-operator-credential.pem';

export interface ManualKitTrustState {
  pinned: boolean | null;
  alg: string | null;
  rpId: string | null;
  origin: string | null;
  publicKeyPEM: string | null;
}

export type ManualKitMode = 'checking' | 'verified' | 'legacy' | 'incomplete';

export interface ManualKitGuide {
  mode: ManualKitMode;
  alg: string | null;
  rpId: string | null;
  origin: string | null;
  publicKeyPEM: string | null;
  operatorFlags: string | null;
}

export function isManualKitWebAuthnAlg(alg: string): boolean {
  return alg === 'webauthn-es256' || alg === 'webauthn-eddsa';
}

// shellQuote emits one POSIX-shell argument. Node IDs are schema-constrained, but quote anyway so
// the UI never teaches an unsafe copy/paste pattern if the naming contract evolves.
export function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'"'"'`)}'`;
}

export function buildManualKitGuide(state: ManualKitTrustState): ManualKitGuide {
  if (state.pinned === null) {
    return { mode: 'checking', alg: null, rpId: null, origin: null, publicKeyPEM: null, operatorFlags: null };
  }
  if (state.pinned === false) {
    return { mode: 'legacy', alg: null, rpId: null, origin: null, publicKeyPEM: null, operatorFlags: null };
  }

  const alg = state.alg?.trim() || null;
  const publicKeyPEM = state.publicKeyPEM?.trim() ? state.publicKeyPEM : null;
  const rpId = state.rpId?.trim() || null;
  const origin = state.origin?.trim() || null;
  if (!alg || !publicKeyPEM || (alg !== 'ed25519' && !isManualKitWebAuthnAlg(alg))) {
    return { mode: 'incomplete', alg, rpId, origin, publicKeyPEM, operatorFlags: null };
  }

  const flags = [
    '--operator-cred', shellQuote(MANUAL_KIT_CREDENTIAL_FILENAME),
    '--operator-cred-alg', shellQuote(alg),
  ];
  if (isManualKitWebAuthnAlg(alg)) {
    // A WebAuthn assertion is cryptographically bound to SHA-256(RP ID), so no command is safe or
    // usable without the exact enrolled value. Origin is advisory in the node verifier; include it
    // whenever status recorded it, and make its omission visible in the UI.
    if (!rpId) {
      return { mode: 'incomplete', alg, rpId, origin, publicKeyPEM, operatorFlags: null };
    }
    flags.push('--operator-rpid', shellQuote(rpId));
    if (origin) flags.push('--operator-origin', shellQuote(origin));
  }

  return {
    mode: 'verified',
    alg,
    rpId,
    origin,
    publicKeyPEM,
    operatorFlags: flags.join(' '),
  };
}

export function buildManualKitApplyCommand(nodeId: string, guide: ManualKitGuide): string | null {
  const base = [
    'sudo yaog-agent kit apply',
    '--bundle', shellQuote(`${nodeId}-bundle.zip`),
    '--node-id', shellQuote(nodeId),
  ].join(' ');

  if (guide.mode === 'legacy') {
    return `${base} --dangerously-allow-no-keystone`;
  }
  if (guide.mode !== 'verified' || !guide.operatorFlags) return null;
  return `${base} ${guide.operatorFlags}`;
}
