import { useState } from 'react';
import { t, type UILanguage } from '../../i18n';
import { triggerBrowserDownload } from '../../lib/download';
import {
  MANUAL_KIT_CREDENTIAL_FILENAME,
  buildManualKitApplyCommand,
  buildManualKitGuide,
  isManualKitWebAuthnAlg,
  type ManualKitTrustState,
} from './manualKitApply';

interface ManualKitNode {
  id: string;
  name?: string;
}

interface ManualKitApplyGuideProps {
  language: UILanguage;
  nodes: readonly ManualKitNode[];
  trust: ManualKitTrustState;
  fingerprint: string | null;
}

// ManualKitApplyGuide turns the already-hydrated operator PUBLIC descriptor into a complete,
// copyable command. It performs no network read and never inspects a candidate bundle for trust.
export function ManualKitApplyGuide({ language, nodes, trust, fingerprint }: ManualKitApplyGuideProps) {
  const guide = buildManualKitGuide(trust);
  const [copied, setCopied] = useState<string | null>(null);
  const [copyFailed, setCopyFailed] = useState(false);

  const copyText = async (key: string, text: string) => {
    setCopyFailed(false);
    try {
      if (!navigator.clipboard?.writeText) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(text);
      setCopied(key);
    } catch {
      setCopied(null);
      setCopyFailed(true);
    }
  };

  const copyLabel = (key: string, normalKey: 'nodeRegistry.manualKitCopyCredential' | 'nodeRegistry.manualKitCopyParameters' | 'nodeRegistry.manualKitCopyCommand') =>
    copied === key ? t(language, 'nodeRegistry.manualKitCopied') : t(language, normalKey);

  const commandList = (
    <ul className="space-y-2">
      {nodes.map((node) => {
        const command = buildManualKitApplyCommand(node.id, guide);
        if (!command) return null;
        const copyKey = `command:${node.id}`;
        return (
          <li key={node.id} className="rounded border border-[var(--hairline)] bg-[var(--surface)] p-2 space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <span className="text-xs font-medium text-[var(--content)]">{node.name || node.id}</span>
              <button
                type="button"
                onClick={() => void copyText(copyKey, command)}
                className="px-2 py-1 text-xs rounded bg-[var(--control)] hover:bg-[var(--control-hover)] text-[var(--content)]"
              >
                {copyLabel(copyKey, 'nodeRegistry.manualKitCopyCommand')}
              </button>
            </div>
            <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded bg-[var(--surface-sunken)] p-2 text-[11px] text-[var(--content)]"><code>{command}</code></pre>
          </li>
        );
      })}
    </ul>
  );

  return (
    <div data-testid="manual-kit-guide" className="space-y-2 rounded-lg border border-[var(--hairline)] bg-[var(--surface-sunken)] p-3">
      <h5 className="text-sm font-semibold text-[var(--content)]">
        {t(language, 'nodeRegistry.manualKitTitle')}
      </h5>

      {guide.mode === 'checking' && (
        <div className="rounded border border-[var(--hairline)] bg-[var(--control)] p-2 text-xs text-[var(--content-muted)]">
          <p className="font-medium">{t(language, 'nodeRegistry.manualKitChecking')}</p>
          <p>{t(language, 'nodeRegistry.manualKitCheckingHint')}</p>
        </div>
      )}

      {guide.mode === 'incomplete' && (
        <div className="rounded border border-[var(--warning-border)] bg-[var(--warning-bg)] p-2 text-xs text-[var(--warning)] space-y-1">
          <p className="font-medium">{t(language, 'nodeRegistry.manualKitIncomplete')}</p>
          <p>{t(language, 'nodeRegistry.manualKitIncompleteHint')}</p>
          {guide.alg && <p>{t(language, 'nodeRegistry.manualKitAlgorithm')}: <code>{guide.alg}</code></p>}
          {guide.rpId && <p>{t(language, 'nodeRegistry.manualKitRpId')}: <code>{guide.rpId}</code></p>}
          {guide.origin && <p>{t(language, 'nodeRegistry.manualKitOrigin')}: <code>{guide.origin}</code></p>}
        </div>
      )}

      {guide.mode === 'verified' && guide.publicKeyPEM && guide.alg && guide.operatorFlags && (
        <div className="space-y-2">
          <div className="rounded border border-[var(--success-border)] bg-[var(--success-bg)] p-2 text-xs space-y-2">
            <p className="font-medium text-[var(--success)]">
              {t(language, 'nodeRegistry.manualKitVerified')}
            </p>
            <p className="text-[var(--content-muted)]">
              {t(language, 'nodeRegistry.manualKitCredentialSourceHint')}
            </p>
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
              <dt className="text-[var(--content-muted)]">{t(language, 'nodeRegistry.manualKitAlgorithm')}</dt>
              <dd className="font-mono break-all text-[var(--content)]">{guide.alg}</dd>
              {isManualKitWebAuthnAlg(guide.alg) ? (
                <>
                  <dt className="text-[var(--content-muted)]">{t(language, 'nodeRegistry.manualKitRpId')}</dt>
                  <dd className="font-mono break-all text-[var(--content)]">{guide.rpId}</dd>
                  <dt className="text-[var(--content-muted)]">{t(language, 'nodeRegistry.manualKitOrigin')}</dt>
                  <dd className="font-mono break-all text-[var(--content)]">
                    {guide.origin || t(language, 'nodeRegistry.manualKitOriginNotRecorded')}
                  </dd>
                </>
              ) : (
                <>
                  <dt className="text-[var(--content-muted)]">{t(language, 'nodeRegistry.manualKitRpIdOrigin')}</dt>
                  <dd className="text-[var(--content-muted)]">{t(language, 'nodeRegistry.manualKitNotUsedForEd25519')}</dd>
                </>
              )}
              {fingerprint && (
                <>
                  <dt className="text-[var(--content-muted)]">{t(language, 'nodeRegistry.manualKitFingerprint')}</dt>
                  <dd className="font-mono break-all text-[var(--content)]">{fingerprint}</dd>
                </>
              )}
            </dl>
            <div className="flex flex-wrap gap-2">
              <button
                type="button"
                onClick={() => void copyText('credential', guide.publicKeyPEM!)}
                className="px-2 py-1 text-xs rounded bg-[var(--info-solid)] text-[var(--info-solid-fg)]"
              >
                {copyLabel('credential', 'nodeRegistry.manualKitCopyCredential')}
              </button>
              <button
                type="button"
                onClick={() => triggerBrowserDownload(new Blob([guide.publicKeyPEM!], { type: 'application/x-pem-file' }), MANUAL_KIT_CREDENTIAL_FILENAME)}
                className="px-2 py-1 text-xs rounded bg-[var(--info-solid)] text-[var(--info-solid-fg)]"
              >
                {t(language, 'nodeRegistry.manualKitDownloadCredential')}
              </button>
              <button
                type="button"
                onClick={() => void copyText('parameters', guide.operatorFlags!)}
                className="px-2 py-1 text-xs rounded bg-[var(--control)] hover:bg-[var(--control-hover)] text-[var(--content)]"
              >
                {copyLabel('parameters', 'nodeRegistry.manualKitCopyParameters')}
              </button>
            </div>
          </div>
          <p className="text-xs font-medium text-[var(--content)]">
            {t(language, 'nodeRegistry.manualKitCommands')}
          </p>
          {commandList}
        </div>
      )}

      {guide.mode === 'legacy' && (
        <div className="space-y-2 rounded border border-[var(--danger-border)] bg-[var(--danger-bg)] p-2">
          <p className="text-xs font-semibold text-[var(--danger)]">
            {t(language, 'nodeRegistry.manualKitLegacyTitle')}
          </p>
          <p className="text-xs text-[var(--danger)]">
            {t(language, 'nodeRegistry.manualKitLegacyWarning')}
          </p>
          {commandList}
        </div>
      )}

      {copyFailed && (
        <p className="text-xs text-[var(--danger)]">{t(language, 'nodeRegistry.manualKitCopyFailed')}</p>
      )}
    </div>
  );
}
