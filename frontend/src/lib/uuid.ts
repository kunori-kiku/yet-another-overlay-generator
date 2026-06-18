// crypto.randomUUID() is only exposed in a secure context (HTTPS or localhost) — when the
// panel is accessed via http://<LAN IP> it is undefined and throws
// "crypto.randomUUID is not a function". The underlying crypto.getRandomValues()
// has no such restriction, so in a non-secure context we hand-roll a UUIDv4 fallback (RFC 4122:
// set version=4 in the high 4 bits of byte 6, set variant=10 in the high 2 bits of byte 8).
// Always use this function wherever a client-side random ID is needed; never call crypto.randomUUID directly.
export function uuid(): string {
  if (typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
