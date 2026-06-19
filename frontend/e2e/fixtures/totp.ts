import crypto from 'node:crypto'

// totpNow reproduces internal/controller/totp.go in-test so the login matrix can complete a
// password+TOTP leg without a real authenticator app. It MUST stay byte-faithful to the Go
// side: base32-decode the secret → HMAC-SHA1 (Go uses crypto/sha1, totp.go:21) over the
// big-endian 30s counter (totpPeriod=30) → RFC-4226 dynamic truncation → 6 digits
// (totpDigits=6). A self-test against a known RFC-6238 SHA1 vector (fixtures.spec.ts) guards
// the helper from silently drifting from the Go derivation.
//
// No clock mocking: callers pass real server time (Date.now()); the controller's
// totpSkewSteps=1 window (totp.go:36, the ±1 step loop at :89) absorbs test latency.

const TOTP_PERIOD_SECONDS = 30
const TOTP_DIGITS = 6

// decodeBase32 decodes an RFC-4648 base32 string (A-Z2-7, case-insensitive, optional '='
// padding) to bytes. Matches the encoding internal/controller/totp.go emits for the secret.
function decodeBase32(input: string): Buffer {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  const clean = input.toUpperCase().replace(/=+$/, '').replace(/\s+/g, '')
  let bits = 0
  let value = 0
  const out: number[] = []
  for (const ch of clean) {
    const idx = alphabet.indexOf(ch)
    if (idx === -1) throw new Error(`totpNow: invalid base32 character ${JSON.stringify(ch)}`)
    value = (value << 5) | idx
    bits += 5
    if (bits >= 8) {
      bits -= 8
      out.push((value >>> bits) & 0xff)
    }
  }
  return Buffer.from(out)
}

// totpNow returns the current 6-digit TOTP code for a base32 secret. atUnixSeconds defaults to
// real wall-clock time; pass an explicit value only for the RFC-6238 self-test vector.
export function totpNow(secretBase32: string, atUnixSeconds?: number): string {
  const nowSec = atUnixSeconds ?? Math.floor(Date.now() / 1000)
  const counter = Math.floor(nowSec / TOTP_PERIOD_SECONDS)

  // 8-byte big-endian counter.
  const counterBuf = Buffer.alloc(8)
  counterBuf.writeBigUInt64BE(BigInt(counter))

  const hmac = crypto.createHmac('sha1', decodeBase32(secretBase32)).update(counterBuf).digest()

  // RFC-4226 dynamic truncation: low nibble of the last byte selects a 4-byte window.
  const offset = hmac[hmac.length - 1] & 0x0f
  const binary =
    ((hmac[offset] & 0x7f) << 24) |
    ((hmac[offset + 1] & 0xff) << 16) |
    ((hmac[offset + 2] & 0xff) << 8) |
    (hmac[offset + 3] & 0xff)

  return (binary % 10 ** TOTP_DIGITS).toString().padStart(TOTP_DIGITS, '0')
}
