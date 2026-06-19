import type { Page, CDPSession } from '@playwright/test'

// virtualAuthenticator.ts wraps the Chrome DevTools Protocol WebAuthn domain so the passkey +
// keystone-signing specs can drive navigator.credentials.create()/get() with no real hardware.
// CTAP2 + internal transport + resident-key + user-verification, with isUserVerified forced true
// so a `userVerification:'required'` ceremony (webauthn.ts) auto-completes without UI.
//
// Navigation caveat (observed): a credential created via create() is reliably usable by a later
// get() only WITHOUT a full-page navigation (page.goto / reload) in between — so the keystone
// signing flow does its design import (which navigates) BEFORE enrolling+signing on one /deploy
// page. LOGIN passkeys are resident/discoverable and survive a same-document logout (a React
// re-render, not a navigation), so the passwordless/2FA legs work across logout.
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
