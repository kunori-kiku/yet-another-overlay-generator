import type { Page, CDPSession } from '@playwright/test'

// virtualAuthenticator.ts wraps the Chrome DevTools Protocol WebAuthn domain so the passkey +
// keystone-signing specs can drive navigator.credentials.create()/get() with no real hardware.
// CTAP2 + internal transport + resident-key + user-verification, with isUserVerified forced true
// so a `userVerification:'required'` ceremony (webauthn.ts) auto-completes without UI. The
// authenticator + its resident credentials persist across page.reload() (they live in the
// browser, not page JS state), which the keystone F1-regression leg relies on.
//
// Chromium-only (the suite's single project). The keys it mints are real ES256 keys, so a
// credential created via create() produces get() assertions the Go verifier accepts against the
// public key the controller pinned — the keystone end-to-end leg needs no manual key handling.

export interface VirtualAuthenticator {
  client: CDPSession
  authenticatorId: string
}

export async function addVirtualAuthenticator(page: Page): Promise<VirtualAuthenticator> {
  const client = await page.context().newCDPSession(page)
  await client.send('WebAuthn.enable')
  const { authenticatorId } = await client.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  })
  return { client, authenticatorId }
}
