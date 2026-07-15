// Existing registered credentials need the enrollment/compatibility warning just as much as a
// user about to register: an older credential may be grandfathered and return only UP. Unknown
// status is the sole authenticated state where the notice waits, avoiding a misleading claim
// before the account-bound status request completes.
export function shouldShowWebAuthnEnrollmentNotice(
  loggedIn: boolean,
  passkeyRegistered: boolean | null,
): boolean {
  return loggedIn && passkeyRegistered !== null;
}
