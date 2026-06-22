import { useEffect, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import {
  useControllerStore,
  selectHasAuth,
} from '../../stores/controllerStore';
import { t, type UILanguage } from '../../i18n';
import { localizeError } from '../../lib/localizeError';
import { compareSemver } from '../../lib/updateStatus';
import {
  AGENT_VERSION_RE,
  isUsableControllerVersion,
  planUpdateAllToControllerVersion,
} from '../../lib/agentRollout';
import { type AgentPin, type ControllerSettings } from '../../api/controllerClient';

// AgentUpdateSettings (controller-panel-rollout-ui plan-3): the operator card that configures the
// signed agent self-update rollout — target/min version, per-arch binary pins (with an
// "Assist from GitHub release" pre-fill), the canary node set, and promote-fleet-wide (behind a
// confirm). It PERSISTS settings only; a Compile → Stage → Promote via the deploy flow still ships
// them to nodes (the copy says so).
//
// CUSTODY (PRINCIPLES.md): the assist is a CONVENIENCE — it fetches the .sha256 sidecars over the
// GitHub proxy for the operator to REVIEW, never a trust anchor. The agent verifies the downloaded
// binary against the SHA-256 in the controller-signed artifacts.json before exec. This card never
// auto-saves a fetched pin; the operator reviews then Saves explicitly.

// CERTIFIED_ARCHES are the linux-<arch> keys self-update is certified for (agent/selfupdate.go).
// 386/armv7 are bootstrap-install-only, so the self-update form offers only these two.
const CERTIFIED_ARCHES = ['linux-amd64', 'linux-arm64'] as const;

// Client-side mirrors of validateAgentRollout (handler_bootstrap.go). The server is authoritative;
// these only give inline hints before save. SEMVER_RE is the shared version grammar from
// lib/agentRollout (AGENT_VERSION_RE) so the field validation + the controller-version usability
// check can never disagree on what a valid version is.
const SEMVER_RE = AGENT_VERSION_RE;
const ASSET_RE = /^[A-Za-z0-9._-]+$/;
const SHA256_RE = /^[0-9a-fA-F]{64}$/;

// blankBins is the empty per-arch pin map (both certified arches unset).
function blankBins(): Record<string, AgentPin> {
  const out: Record<string, AgentPin> = {};
  for (const arch of CERTIFIED_ARCHES) out[arch] = { asset: '', sha256: '' };
  return out;
}

export function AgentUpdateSettings() {
  const language = useTopologyStore((s) => s.language);
  const settings = useControllerStore((s) => s.settings);
  const loadSettings = useControllerStore((s) => s.loadSettings);
  const hasAuth = useControllerStore(selectHasAuth);

  // Guarded one-shot load (mirrors BootstrapSettings / TwoFactorSettings): store action, not a
  // setState, so it does not loop. Idempotent if a sibling card already triggered it.
  useEffect(() => {
    if (hasAuth && settings === null) void loadSettings();
  }, [hasAuth, settings, loadSettings]);

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-emerald-400">{t(language, 'agentUpdate.heading')}</h3>
      <p className="text-sm text-gray-400">{t(language, 'agentUpdate.description')}</p>
      {!hasAuth ? (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {t(language, 'agentUpdate.signInToConfigure')}
        </p>
      ) : settings === null ? (
        <p className="text-xs text-gray-500">{t(language, 'agentUpdate.loading')}</p>
      ) : (
        // Render only once settings have loaded, with NO settings-keyed remount: the form seeds its
        // local state from the loaded values on mount and then OWNS the operator's edits — a save
        // (or an unrelated settings change, e.g. the appearance toggle) must not remount it and
        // discard in-progress edits or the just-saved success notice. handleSave round-trips the
        // FRESH store settings (not this snapshot) so the full-replace contract still holds.
        <AgentUpdateForm initial={settings} language={language} />
      )}
    </section>
  );
}

function AgentUpdateForm({ initial, language }: { initial: ControllerSettings; language: UILanguage }) {
  const nodes = useControllerStore((s) => s.nodes);
  const loading = useControllerStore((s) => s.loading);
  const saveSettings = useControllerStore((s) => s.saveSettings);
  const fetchReleasePins = useControllerStore((s) => s.fetchReleasePins);
  // controllerVersion is server truth (plan-8): a real semver on a stamped release build, the literal
  // "dev" on an unstamped build, or "" only when talking to an older controller that predates the
  // field. The one-click "update all" affordance + the refuse-newer hint gate on isUsableControllerVersion
  // (real semver), so a "dev"/non-semver controller degrades to the "no version to match" note rather
  // than offering a doomed rollout — mirroring the backend guard's own semverPattern gate.
  const controllerVersion = useControllerStore((s) => s.controllerVersion);
  const controllerVersionUsable = isUsableControllerVersion(controllerVersion);

  const [targetVersion, setTargetVersion] = useState(initial.targetAgentVersion);
  const [minVersion, setMinVersion] = useState(initial.minAgentVersion);
  const [showAdvanced, setShowAdvanced] = useState(initial.minAgentVersion !== '');
  // Per-arch bin rows for the two certified arches (others not offered for self-update).
  const [bins, setBins] = useState<Record<string, AgentPin>>(() => {
    const seed = blankBins();
    for (const arch of CERTIFIED_ARCHES) {
      if (initial.agentBins[arch]) seed[arch] = initial.agentBins[arch];
    }
    return seed;
  });
  const [canary, setCanary] = useState<string[]>(initial.agentCanaryNodeIds);
  const [fleetWide, setFleetWide] = useState(initial.agentRolloutFleetWide);
  const [showFleetConfirm, setShowFleetConfirm] = useState(false);

  // Assist state. pinnedBase + pinnedForVersion capture the version_applied contract: when the
  // assist rewrites the "latest" base to a tagged URL, that tagged base must be saved as the agent
  // release base (else the agent — which fetches the verbatim saved base with no latest->tag
  // rewrite — downloads a different binary than the one pinned: a fail-closed hash mismatch).
  const [busy, setBusy] = useState(false);
  const [assistNote, setAssistNote] = useState<string | null>(null);
  const [pinnedBase, setPinnedBase] = useState<string | null>(null);
  const [pinnedForVersion, setPinnedForVersion] = useState('');
  const [localError, setLocalError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const onTargetChange = (v: string) => {
    setTargetVersion(v);
    // A versionApplied assist keys BOTH the tagged base AND the tagged pins (asset names are
    // version-agnostic; only the SHA-256 differs per tag) to the version it was fetched at. A
    // target change invalidates that whole assisted set: clear the pinned base AND the
    // assist-derived pins so neither a stale base nor wrong-hash pins can be saved against a
    // different version — the operator must re-assist (or enter pins manually) for the new target.
    // pinnedForVersion === '' means the current pins are manual or from a no-version (latest)
    // assist, which stay valid across a target edit, so they are left untouched.
    if (v.trim() !== pinnedForVersion) {
      if (pinnedForVersion !== '') setBins(blankBins());
      setPinnedBase(null);
      setPinnedForVersion('');
      setAssistNote(null);
    }
    setSaved(false);
  };

  const setBin = (arch: string, patch: Partial<AgentPin>) => {
    setBins((b) => ({ ...b, [arch]: { ...b[arch], ...patch } }));
    setSaved(false);
  };

  const toggleCanary = (nodeId: string) => {
    setCanary((c) => (c.includes(nodeId) ? c.filter((x) => x !== nodeId) : [...c, nodeId]));
    setSaved(false);
  };

  // filledBins keeps only the rows the operator actually populated (an empty row is not a pin).
  const filledBins = (): Record<string, AgentPin> => {
    const out: Record<string, AgentPin> = {};
    for (const arch of CERTIFIED_ARCHES) {
      const p = bins[arch];
      if (p.asset.trim() && p.sha256.trim()) out[arch] = { asset: p.asset.trim(), sha256: p.sha256.trim() };
    }
    return out;
  };

  // validate mirrors validateAgentRollout for inline hints (server authoritative on save).
  const validate = (): string | null => {
    const target = targetVersion.trim();
    if (target && !SEMVER_RE.test(target)) return t(language, 'agentUpdate.invalidTargetSemver');
    if (minVersion.trim() && !SEMVER_RE.test(minVersion.trim())) return t(language, 'agentUpdate.invalidMinSemver');
    for (const arch of CERTIFIED_ARCHES) {
      const p = bins[arch];
      if (p.asset.trim() && !ASSET_RE.test(p.asset.trim())) return t(language, 'agentUpdate.invalidAsset');
      if (p.sha256.trim() && !SHA256_RE.test(p.sha256.trim())) return t(language, 'agentUpdate.invalidSha');
      // A half-filled row (one of asset/sha256) is a configuration mistake.
      if (Boolean(p.asset.trim()) !== Boolean(p.sha256.trim())) return t(language, 'agentUpdate.incompletePin');
    }
    if (target && Object.keys(filledBins()).length === 0) return t(language, 'agentUpdate.targetNeedsBin');
    // Advisory mirror of the backend refuse-newer floor (validateAgentRollout): a target strictly
    // newer than the controller's own version is rejected at save, so warn before the round-trip.
    // compareSemver is the panel's existing SemVer-ish comparator (updateStatus.ts) — reused, not a
    // second implementation. Only meaningful when the controller reported a USABLE (real-semver)
    // version — a "dev"/non-semver controller version disables the floor (same as the backend).
    if (target && controllerVersionUsable && compareSemver(target, controllerVersion) > 0) {
      return t(language, 'agentUpdate.targetNewerThanController', { target, controller: controllerVersion });
    }
    return null;
  };
  const validationHint = validate();

  // handleAssist fetches the release pins for a target. targetOverride lets the one-click
  // "update all to the controller version" pass the new target in the SAME tick (React state set
  // by handleUpdateAllToControllerVersion is not yet visible here), so the assist + the eventual
  // save agree on the version. Returns true on success so the caller can chain (e.g. arm fleet-wide).
  const handleAssist = async (targetOverride?: string): Promise<boolean> => {
    setBusy(true);
    setLocalError(null);
    setSaved(false);
    try {
      // No assets → the server derives the certified arches' canonical asset names and fetches
      // their sidecars. version optional (pins "latest" to a tag when the base is the latest alias).
      const target = (targetOverride ?? targetVersion).trim();
      const res = await fetchReleasePins({ kind: 'agent', version: target || undefined, assets: [] });
      setBins((prev) => {
        const next = { ...prev };
        for (const arch of CERTIFIED_ARCHES) {
          const pin = res.pins[arch];
          if (pin) {
            // Strip any path qualification defensively (the asset pattern forbids '/').
            const asset = pin.asset.split('/').pop() ?? pin.asset;
            next[arch] = { asset, sha256: pin.sha256 };
          }
        }
        return next;
      });
      if (res.versionApplied) {
        // The assist pinned a tag; remember the tagged base to persist on save (and warn the
        // operator we will repoint the release base) so the saved pins match what the agent fetches.
        setPinnedBase(res.base);
        setPinnedForVersion(target);
        setAssistNote(t(language, 'agentUpdate.assistPinnedNote', { version: target, base: res.base }));
      } else if (target) {
        // version requested but a custom/mirror base ignored it — the pins are for whatever that
        // base serves, not the requested tag.
        setPinnedBase(null);
        setAssistNote(t(language, 'agentUpdate.assistCustomBaseWarn'));
      } else {
        setPinnedBase(null);
        setAssistNote(null);
      }
      return true;
    } catch (err) {
      setLocalError(localizeError(err, language));
      return false;
    } finally {
      setBusy(false);
    }
  };

  // handleUpdateAllToControllerVersion is the one-click "roll the whole fleet to the version this
  // panel ships" (plan-8 / requirement 3). The orchestration (gate on a usable version → set target
  // → assist in the SAME tick via the override → arm the fleet-wide confirm ONLY on success, never
  // auto-save) lives in lib/agentRollout.planUpdateAllToControllerVersion so it is unit-testable
  // without a render; here we just bind it to this card's React state + store effects.
  const handleUpdateAllToControllerVersion = () =>
    planUpdateAllToControllerVersion(controllerVersion, {
      setTarget: onTargetChange, // set target + invalidate any stale assist pins (shared path)
      assist: handleAssist, // passes the version as targetOverride, avoiding stale React state
      armFleetConfirm: () => setShowFleetConfirm(true),
    });

  const handleFleetToggle = (on: boolean) => {
    // Arming fleet-wide is the one fleet-affecting action; gate ON behind a confirm. OFF is
    // reversible and needs none (the empty-target safety contract + Principle 2).
    if (on) {
      setShowFleetConfirm(true);
    } else {
      setFleetWide(false);
      setSaved(false);
    }
  };

  const handleSave = async () => {
    if (validate()) return;
    setLocalError(null);
    // Round-trip the FRESH store settings (not the mount-time snapshot), so an unrelated field
    // changed since mount (mimic catalog, translucency) is not wiped by this full-replace save.
    const current = useControllerStore.getState().settings ?? initial;
    // saveSettings returns the localized error on failure, null on success — derive the outcome
    // from that, NOT from the call resolving (the store action never throws).
    const err = await saveSettings({
      ...current,
      targetAgentVersion: targetVersion.trim(),
      minAgentVersion: minVersion.trim(),
      agentBins: filledBins(),
      agentCanaryNodeIds: canary,
      agentRolloutFleetWide: fleetWide,
      // Persist the tagged base when an assist pinned a version (see pinnedBase above), so the
      // agent fetches the binary the pins were computed against. Otherwise leave it untouched.
      agentReleaseBaseURL:
        pinnedBase && targetVersion.trim() === pinnedForVersion ? pinnedBase : current.agentReleaseBaseURL,
    });
    if (err) {
      setLocalError(err);
      setSaved(false);
    } else {
      setSaved(true);
    }
  };

  const proxyText = initial.githubProxy.trim() || t(language, 'agentUpdate.proxyNone');

  const field = (label: string, value: string, set: (v: string) => void, placeholder: string) => (
    <div>
      <label className="text-xs text-gray-400">{label}</label>
      <input
        type="text"
        value={value}
        onChange={(e) => set(e.target.value)}
        placeholder={placeholder}
        className="w-full px-2 py-1 bg-gray-600 rounded text-sm font-mono border border-gray-500 focus:border-blue-400 outline-none"
      />
    </div>
  );

  return (
    <div className="space-y-4">
      {/* Target + min version */}
      <div className="space-y-2">
        {field(
          t(language, 'agentUpdate.targetVersionLabel'),
          targetVersion,
          onTargetChange,
          'v2.0.0-beta.3',
        )}
        <p className="text-[10px] text-gray-500">{t(language, 'agentUpdate.targetVersionHint')}</p>
        {/* One-click "match the controller" (plan-8): present only when the controller reported a
            USABLE (real-semver) version; a "dev"/non-semver/absent version shows a quiet note so the
            absence is explained, not a button that would arm a doomed rollout. */}
        {controllerVersionUsable ? (
          <div className="space-y-1">
            <button
              type="button"
              onClick={() => void handleUpdateAllToControllerVersion()}
              disabled={busy || loading}
              className="w-full rounded bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400"
            >
              {t(language, 'agentUpdate.updateAllToControllerVersion', { version: controllerVersion })}
            </button>
            <p className="text-[10px] text-gray-500">{t(language, 'agentUpdate.updateAllHint')}</p>
          </div>
        ) : (
          <p className="text-[10px] text-gray-600">{t(language, 'agentUpdate.noControllerVersion')}</p>
        )}
        <button
          type="button"
          onClick={() => setShowAdvanced((s) => !s)}
          className="text-xs text-blue-400 hover:text-blue-300"
        >
          {showAdvanced ? '▾ ' : '▸ '}
          {t(language, 'agentUpdate.advanced')}
        </button>
        {showAdvanced && (
          <div>
            {field(
              t(language, 'agentUpdate.minVersionLabel'),
              minVersion,
              (v) => {
                setMinVersion(v);
                setSaved(false);
              },
              'v2.0.0-beta.1',
            )}
            <p className="text-[10px] text-gray-500 mt-0.5">{t(language, 'agentUpdate.minVersionHint')}</p>
          </div>
        )}
      </div>

      {/* Per-arch binary pins + assist */}
      <div className="space-y-2 p-3 bg-gray-900 border border-gray-700 rounded">
        <div className="flex items-center justify-between">
          <h4 className="text-sm font-semibold text-gray-200">{t(language, 'agentUpdate.binsHeading')}</h4>
          <button
            type="button"
            onClick={() => void handleAssist()}
            disabled={busy || loading}
            className="px-3 py-1 text-xs bg-sky-600 hover:bg-sky-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
          >
            {busy ? t(language, 'agentUpdate.assisting') : t(language, 'agentUpdate.assistButton')}
          </button>
        </div>
        <p className="text-[10px] text-gray-500">{t(language, 'agentUpdate.binsHint')}</p>
        {CERTIFIED_ARCHES.map((arch) => (
          <div key={arch} className="space-y-1 border-t border-gray-800 pt-2">
            <p className="text-xs font-mono text-emerald-300">{arch}</p>
            <div className="grid grid-cols-1 gap-1">
              {field(
                t(language, 'agentUpdate.assetLabel'),
                bins[arch].asset,
                (v) => setBin(arch, { asset: v }),
                `yaog-agent-${arch}`,
              )}
              {field(
                t(language, 'agentUpdate.sha256Label'),
                bins[arch].sha256,
                (v) => setBin(arch, { sha256: v }),
                t(language, 'agentUpdate.sha256Placeholder'),
              )}
            </div>
          </div>
        ))}
        <p className="text-[10px] text-amber-300/80">{t(language, 'agentUpdate.assistCustody')}</p>
        {assistNote && <p className="text-[10px] text-sky-300 bg-sky-900/20 px-2 py-1 rounded">{assistNote}</p>}
      </div>

      {/* Canary node set */}
      <div className="space-y-1">
        <label className="text-xs text-gray-400">{t(language, 'agentUpdate.canaryHeading')}</label>
        <p className="text-[10px] text-gray-500">{t(language, 'agentUpdate.canaryHint')}</p>
        {nodes.length === 0 ? (
          <p className="text-xs text-gray-500">{t(language, 'agentUpdate.canaryNoNodes')}</p>
        ) : (
          <div className={`space-y-1 ${fleetWide ? 'opacity-50' : ''}`}>
            {nodes.map((n) => (
              <label key={n.nodeId} className="flex items-center gap-2 text-sm text-gray-200">
                <input
                  type="checkbox"
                  checked={canary.includes(n.nodeId)}
                  onChange={() => toggleCanary(n.nodeId)}
                  disabled={fleetWide}
                />
                <span className="font-mono">{n.nodeId}</span>
              </label>
            ))}
          </div>
        )}
        {fleetWide && <p className="text-[10px] text-amber-300/80">{t(language, 'agentUpdate.canaryFleetWideActive')}</p>}
      </div>

      {/* Promote fleet-wide */}
      <div className="space-y-1">
        <label className="flex items-center gap-2 text-sm text-gray-200">
          <input type="checkbox" checked={fleetWide} onChange={(e) => handleFleetToggle(e.target.checked)} />
          {t(language, 'agentUpdate.fleetWideLabel')}
        </label>
        <p className="text-[10px] text-gray-500">{t(language, 'agentUpdate.fleetWideHint')}</p>
      </div>

      {/* GitHub proxy echo (read-only; edited in Bootstrap settings) */}
      <div className="space-y-0.5">
        <label className="text-xs text-gray-400">{t(language, 'agentUpdate.proxyLabel')}</label>
        <p className="text-sm font-mono text-gray-300 break-all">{proxyText}</p>
        <p className="text-[10px] text-gray-500">{t(language, 'agentUpdate.proxyHint')}</p>
      </div>

      {/* Custody reminder */}
      <p className="text-[10px] text-gray-500 border-t border-gray-700 pt-2">{t(language, 'agentUpdate.custodyNote')}</p>

      {validationHint && <p className="text-xs text-amber-400 bg-amber-900/20 px-2 py-1 rounded">{validationHint}</p>}
      {localError && <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">⚠️ {localError}</p>}
      {saved && <p className="text-xs text-green-300 bg-green-900/20 px-2 py-1 rounded">{t(language, 'agentUpdate.savedNotice')}</p>}

      <button
        onClick={() => void handleSave()}
        disabled={loading || busy || validationHint !== null}
        className="px-4 py-1.5 text-sm bg-emerald-600 hover:bg-emerald-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
      >
        {loading ? t(language, 'agentUpdate.saving') : t(language, 'agentUpdate.saveButton')}
      </button>

      {/* Promote-fleet-wide confirm (amber modal, modeled on SettingsPage's lossy-switch dialog). */}
      {showFleetConfirm && (
        <div className="fixed inset-0 z-50 grid place-items-center bg-black/50 p-4" role="dialog" aria-modal="true">
          <div className="w-full max-w-md space-y-4 rounded-lg border border-gray-700 bg-gray-800 p-5">
            <h4 className="text-base font-semibold text-amber-400">{t(language, 'agentUpdate.fleetConfirmTitle')}</h4>
            <p className="text-sm text-gray-300">{t(language, 'agentUpdate.fleetConfirmBody')}</p>
            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setShowFleetConfirm(false)}
                className="rounded border border-gray-600 px-3 py-1.5 text-sm text-gray-300 hover:bg-gray-700"
              >
                {t(language, 'agentUpdate.cancel')}
              </button>
              <button
                type="button"
                onClick={() => {
                  setFleetWide(true);
                  setShowFleetConfirm(false);
                  setSaved(false);
                }}
                className="rounded bg-amber-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-amber-500"
              >
                {t(language, 'agentUpdate.fleetConfirmAction')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
