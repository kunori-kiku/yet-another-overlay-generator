// Shared controller route for browser WebAuthn enrollment. Login-passkey and keystone
// registration both use the same authenticated, purpose-scoped one-use challenge.

import { postJSON, type ControllerConfig } from './transport';

export type WebAuthnEnrollmentPurpose = 'login' | 'keystone';

interface WebAuthnEnrollmentBeginResponseJSON {
  challenge: string;
}

export async function beginWebAuthnEnrollment(
  cfg: ControllerConfig,
  purpose: WebAuthnEnrollmentPurpose,
): Promise<string> {
  const res = await postJSON(
    cfg,
    'webauthn/enrollment/begin',
    JSON.stringify({ purpose }),
  );
  const body = (await res.json()) as WebAuthnEnrollmentBeginResponseJSON;
  if (typeof body.challenge !== 'string' || body.challenge.length === 0) {
    throw new Error('controller returned an empty WebAuthn enrollment challenge');
  }
  return body.challenge;
}
