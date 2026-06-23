import { useEffect, useState } from 'react';
import type { WireGuardPeer } from '../../types/controller';
import { t, type UILanguage } from '../../i18n';
import { handshakeAge, peerStatusCounts, type HandshakeAge } from '../../lib/wgPeers';

// WireGuardPeersPanel (beta.12): a collapsible per-peer link panel — the detail behind the aggregate
// `wireguard` condition. It renders the last handshake (relative) for each peer the node connects to,
// so a single down link is visible WHICH link, not just a whole-node "LinkDown". Data is the agent's
// /telemetry metric (node.wireguardPeers); empty ⇒ nothing rendered (legacy/beta.11 agent, client
// node, or no peers). Observability only — no key material is shown.

const STATUS_DOT: Record<WireGuardPeer['status'], string> = {
  up: 'bg-green-400',
  stale: 'bg-yellow-400',
  never: 'bg-red-400',
};

// fmtAge localizes the pure HandshakeAge bucket. now is held in state (not read impurely during
// render) and re-ticks on an interval so the relative time stays roughly current; the lib stays pure.
function fmtAge(language: UILanguage, age: HandshakeAge): string {
  switch (age.kind) {
    case 'never':
      return t(language, 'wgPeers.never');
    case 'seconds':
      return t(language, 'wgPeers.secondsAgo', { n: String(age.value) });
    case 'minutes':
      return t(language, 'wgPeers.minutesAgo', { n: String(age.value) });
    case 'hours':
      return t(language, 'wgPeers.hoursAgo', { n: String(age.value) });
    case 'days':
      return t(language, 'wgPeers.daysAgo', { n: String(age.value) });
  }
}

export function WireGuardPeersPanel({
  peers,
  language,
}: {
  peers: WireGuardPeer[];
  language: UILanguage;
}) {
  // A live "now" for the relative handshake ages: seeded once (lazy init — not an impure render-time
  // read) and re-ticked every 30s so the panel ages in place without a fleet refetch.
  const [nowSec, setNowSec] = useState(() => Math.floor(Date.now() / 1000));
  useEffect(() => {
    const id = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 30_000);
    return () => clearInterval(id);
  }, []);
  if (peers.length === 0) return null;
  const counts = peerStatusCounts(peers);
  // Default-open only when something is wrong (a down/stale link), so the operator sees the detail
  // without a click exactly when it matters; an all-up node stays collapsed.
  const allUp = counts.never === 0 && counts.stale === 0;

  return (
    <details open={!allUp} className="rounded border border-[var(--hairline)] bg-[var(--surface)]">
      <summary className="cursor-pointer px-3 py-2 text-sm text-[var(--content)]">
        {t(language, 'wgPeers.heading')}{' '}
        <span className="text-xs text-[var(--content-muted)]">
          {t(language, 'wgPeers.upOfTotal', { up: String(counts.up), total: String(peers.length) })}
        </span>
      </summary>
      <ul className="divide-y divide-[var(--hairline)] px-3 pb-2">
        {peers.map((p) => (
          <li
            key={p.interface || p.peer}
            className="flex items-center gap-2 py-1.5 text-xs"
            title={p.endpoint || p.interface}
          >
            <span className={`h-2 w-2 shrink-0 rounded-full ${STATUS_DOT[p.status]}`} aria-hidden />
            <span className="flex-1 break-all font-mono text-[var(--content)]">{p.peer || p.interface}</span>
            <span className="shrink-0 text-[var(--content-muted)]">{fmtAge(language, handshakeAge(nowSec, p.lastHandshake, p.status))}</span>
          </li>
        ))}
      </ul>
    </details>
  );
}
