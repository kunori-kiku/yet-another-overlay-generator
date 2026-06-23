import { useEffect, useRef, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectHasAuth } from '../../stores/controllerStore';
import { t, type UILanguage } from '../../i18n';
import { type ControllerSettings } from '../../api/controllerClient';
import { deriveKey, collidingKeys } from '../../lib/mimicDiscover';

// MimicCatalogSettings (controller-panel-rollout-ui plan-4): the operator card for the mimic
// GitHub-.deb catalog — a version tag, the release base URL, and per-"<codename>-<arch>" .deb pins
// for distros that do not package mimic (Debian 12 / Ubuntu 24.04). Symmetric to
// AgentUpdateSettings, but the codename-arch combos are operator-defined, so the pins are a DYNAMIC
// add/remove list rather than the agent's fixed two arches.
//
// CUSTODY (PRINCIPLES.md): the "Assist from release" fetch is convenience only — the mimic release
// base is external and frequently publishes no .sha256 sidecars, so assist is BEST-EFFORT (per-row)
// and manual entry is the guaranteed path. Trust comes from the controller-signed artifacts.json +
// the install-time `sha256sum -c`, never this fetch; the card never auto-saves a fetched hash.

// A deb row: a stable id (React key across add/remove), the operator-entered fields, and a
// transient per-row note (e.g. an assist miss) that is not persisted.
interface DebRow {
  id: number;
  key: string;
  asset: string;
  sha256: string;
  note?: string;
}

// A discovered .deb asset awaiting the operator's pick + label. asset is the immutable release
// filename (also the stable React key — unique within a release); key is the operator-editable
// "<codename>-<arch>" label (prefilled via deriveKey); checked drives inclusion in "Add selected".
interface DiscoveredRow {
  asset: string;
  key: string;
  checked: boolean;
}

// Client-side mirrors of validateMimicCatalog (handler_bootstrap.go); the server is authoritative.
const SEMVER_RE = /^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)*$/;
const DEB_KEY_RE = /^[a-z0-9]+-[a-z0-9]+$/;
const DEB_ASSET_RE = /^[A-Za-z0-9._+-]+\.deb$/;
const SHA256_RE = /^[0-9a-fA-F]{64}$/;
const SHELL_BYTES_RE = /[$`;|&<>(){}[\]'"\\*?\s]/;

export function MimicCatalogSettings() {
  const language = useTopologyStore((s) => s.language);
  const settings = useControllerStore((s) => s.settings);
  const loadSettings = useControllerStore((s) => s.loadSettings);
  const hasAuth = useControllerStore(selectHasAuth);

  useEffect(() => {
    if (hasAuth && settings === null) void loadSettings();
  }, [hasAuth, settings, loadSettings]);

  return (
    <section className="bg-gray-800 border border-gray-700 p-4 rounded-lg space-y-3 max-w-2xl">
      <h3 className="text-lg font-semibold text-emerald-400">{t(language, 'mimicCatalog.heading')}</h3>
      <p className="text-sm text-gray-400">{t(language, 'mimicCatalog.description')}</p>
      {!hasAuth ? (
        <p className="text-xs text-yellow-400 bg-yellow-900/20 px-2 py-1 rounded">
          {t(language, 'mimicCatalog.signInToConfigure')}
        </p>
      ) : settings === null ? (
        <p className="text-xs text-gray-500">{t(language, 'mimicCatalog.loading')}</p>
      ) : (
        // Render only once settings load and do NOT remount on settings change (same rationale as
        // AgentUpdateSettings): the form owns the operator's edits and the success notice; handleSave
        // round-trips the FRESH store settings so the full-replace contract still holds.
        <MimicCatalogForm initial={settings} language={language} />
      )}
    </section>
  );
}

function MimicCatalogForm({ initial, language }: { initial: ControllerSettings; language: UILanguage }) {
  const loading = useControllerStore((s) => s.loading);
  const saveSettings = useControllerStore((s) => s.saveSettings);
  const fetchReleasePins = useControllerStore((s) => s.fetchReleasePins);
  const fetchReleaseAssets = useControllerStore((s) => s.fetchReleaseAssets);

  const [version, setVersion] = useState(initial.mimicVersion);
  const [releaseBase, setReleaseBase] = useState(initial.mimicReleaseBase);
  // Fleet-wide mimic UDP-fallback default a link inherits (plan-6): '' inherit/unset (fail closed) /
  // 'udp' / 'none'. A string tri-state mirroring the Go ControllerSettings.MimicFallbackDefault.
  const [fallbackDefault, setFallbackDefault] = useState(initial.mimicFallbackDefault);
  // Seed the id counter to the initial row count (no ref write during render); addRow advances it
  // in its event handler. Initial rows get ids 0..n-1 by index.
  const nextId = useRef(Object.keys(initial.mimicDebs).length);
  const [debs, setDebs] = useState<DebRow[]>(() =>
    Object.entries(initial.mimicDebs).map(([key, pin], i) => ({
      id: i,
      key,
      asset: pin.asset,
      sha256: pin.sha256,
    })),
  );
  const [busy, setBusy] = useState(false);
  const [localError, setLocalError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  // The release-asset discovery checklist: null = hidden (not yet discovered / dismissed);
  // a list = the operator is picking which discovered .deb assets to add as rows.
  const [discovered, setDiscovered] = useState<DiscoveredRow[] | null>(null);
  const [discovering, setDiscovering] = useState(false);

  const dirty = () => setSaved(false);

  const setRow = (id: number, patch: Partial<DebRow>) => {
    setDebs((rows) => rows.map((r) => (r.id === id ? { ...r, ...patch, note: undefined } : r)));
    dirty();
  };
  const addRow = () => {
    setDebs((rows) => [...rows, { id: nextId.current++, key: '', asset: '', sha256: '' }]);
    dirty();
  };
  const removeRow = (id: number) => {
    setDebs((rows) => rows.filter((r) => r.id !== id));
    dirty();
  };

  // filledDebs is the Record the wire carries — only rows with all three fields populated (an
  // empty/partial row is not a pin). A duplicate key keeps the LAST occurrence (matches a JS map).
  const filledDebs = (): Record<string, { asset: string; sha256: string }> => {
    const out: Record<string, { asset: string; sha256: string }> = {};
    for (const r of debs) {
      const key = r.key.trim();
      if (key && r.asset.trim() && r.sha256.trim()) out[key] = { asset: r.asset.trim(), sha256: r.sha256.trim() };
    }
    return out;
  };

  // validate mirrors validateMimicCatalog for inline hints (server authoritative on save).
  const validate = (): string | null => {
    if (version.trim() && !SEMVER_RE.test(version.trim())) return t(language, 'mimicCatalog.invalidVersion');
    if (releaseBase.trim() && SHELL_BYTES_RE.test(releaseBase.trim())) return t(language, 'mimicCatalog.invalidReleaseBase');
    // Case-insensitive scheme to match the server (Go's url.Parse lowercases the scheme, so it
    // accepts HTTP://… ); a case-sensitive test would wrongly block a server-valid base client-side.
    if (releaseBase.trim() && !/^https?:\/\/.+/i.test(releaseBase.trim())) return t(language, 'mimicCatalog.invalidReleaseBase');
    let anyFilled = false;
    for (const r of debs) {
      const key = r.key.trim();
      const filledCount = [key, r.asset.trim(), r.sha256.trim()].filter(Boolean).length;
      if (filledCount === 0) continue; // a fully-empty row is ignored, not an error
      anyFilled = true;
      if (filledCount < 3) return t(language, 'mimicCatalog.incompletePin');
      if (!DEB_KEY_RE.test(key)) return t(language, 'mimicCatalog.invalidKey');
      if (!DEB_ASSET_RE.test(r.asset.trim())) return t(language, 'mimicCatalog.invalidAsset');
      if (!SHA256_RE.test(r.sha256.trim())) return t(language, 'mimicCatalog.invalidSha');
    }
    // debs-require-release-base (validateMimicCatalog): a deb with nowhere to fetch from is rejected.
    if (anyFilled && !releaseBase.trim()) return t(language, 'mimicCatalog.debsNeedReleaseBase');
    return null;
  };
  const validationHint = validate();

  // handleAssist fetches each fillable row's sidecar INDEPENDENTLY (best-effort): the mimic release
  // base is external and frequently lacks sidecars, so a per-row miss leaves that row for manual
  // entry with an informational note rather than failing the whole batch.
  const handleAssist = async () => {
    setLocalError(null);
    dirty();
    const base = releaseBase.trim();
    if (!base) {
      setLocalError(t(language, 'mimicCatalog.assistNeedsBase'));
      return;
    }
    setBusy(true);
    const results = await Promise.all(
      debs.map(async (row) => {
        const key = row.key.trim();
        const asset = row.asset.trim();
        if (!key || !asset) return { id: row.id, sha256: null as string | null, failed: false };
        try {
          const res = await fetchReleasePins({ kind: 'mimic', base, assets: [{ key, asset }] });
          const pin = res.pins[key];
          return { id: row.id, sha256: pin ? pin.sha256 : null, failed: !pin };
        } catch {
          return { id: row.id, sha256: null, failed: true };
        }
      }),
    );
    const byId = new Map(results.map((r) => [r.id, r]));
    setDebs((rows) =>
      rows.map((row) => {
        const r = byId.get(row.id);
        if (!r) return row;
        if (r.sha256) return { ...row, sha256: r.sha256, note: undefined };
        if (r.failed) return { ...row, note: t(language, 'mimicCatalog.assistRowFailed', { asset: row.asset.trim() || row.key.trim() }) };
        return row;
      }),
    );
    setBusy(false);
  };

  // handleDiscover lists the release's .deb assets (POST release-assets) and opens the pick-from
  // checklist. Best-effort convenience: a failure leaves the checklist hidden and the operator adds
  // rows by hand. deriveKey prefills the "<codename>-<arch>" label; a package we cannot label
  // (dkms / unmatched) starts unchecked so the operator must label it before adding.
  const handleDiscover = async () => {
    setLocalError(null);
    const base = releaseBase.trim();
    if (!base) {
      setLocalError(t(language, 'mimicCatalog.assistNeedsBase'));
      return;
    }
    setDiscovering(true);
    try {
      const res = await fetchReleaseAssets({ base, version: version.trim() || undefined });
      if (res.assets.length === 0) {
        setDiscovered(null);
        setLocalError(t(language, 'mimicCatalog.discoverEmpty'));
        return;
      }
      setDiscovered(
        res.assets.map((asset) => {
          const key = deriveKey(asset);
          return { asset, key, checked: key !== '' };
        }),
      );
    } catch {
      setDiscovered(null);
      setLocalError(t(language, 'mimicCatalog.discoverFailed'));
    } finally {
      setDiscovering(false);
    }
  };

  const setDiscoveredRow = (asset: string, patch: Partial<DiscoveredRow>) => {
    setDiscovered((rows) => (rows ? rows.map((r) => (r.asset === asset ? { ...r, ...patch } : r)) : rows));
  };

  // A CHECKED discovered key collides when it duplicates another checked row OR an existing deb row;
  // "Add selected" is blocked until every checked key is unique (a duplicate would silently drop a
  // pin on save — see collidingKeys). An empty checked key is reported separately (needs a label).
  const checkedRows = (discovered ?? []).filter((r) => r.checked);
  const dupKeys = collidingKeys(
    checkedRows.map((r) => r.key),
    debs.map((r) => r.key),
  );
  const hasBlankCheckedKey = checkedRows.some((r) => !r.key.trim());
  const canAddSelected = checkedRows.length > 0 && !hasBlankCheckedKey && dupKeys.size === 0;

  // handleAddSelected appends the checked discovered assets as new deb rows with an EMPTY sha256:
  // custody keeps discovery (the name) separate from the pin (the hash), so the operator then runs
  // the existing per-row Assist (or pastes the hash) and Saves. Clears the checklist when done.
  const handleAddSelected = () => {
    if (!canAddSelected) return;
    const toAdd: DebRow[] = checkedRows.map((r) => ({
      id: nextId.current++,
      key: r.key.trim(),
      asset: r.asset,
      sha256: '',
    }));
    setDebs((rows) => [...rows, ...toAdd]);
    setDiscovered(null);
    dirty();
  };

  const handleSave = async () => {
    if (validate()) return;
    setLocalError(null);
    const current = useControllerStore.getState().settings ?? initial;
    const err = await saveSettings({
      ...current,
      mimicVersion: version.trim(),
      mimicReleaseBase: releaseBase.trim(),
      mimicDebs: filledDebs(),
      mimicFallbackDefault: fallbackDefault,
    });
    if (err) {
      setLocalError(err);
      setSaved(false);
    } else {
      setSaved(true);
    }
  };

  const proxyText = initial.githubProxy.trim() || t(language, 'mimicCatalog.proxyNone');

  return (
    <div className="space-y-4">
      {/* Version + release base */}
      <div className="space-y-2">
        <div>
          <label className="text-xs text-gray-400">{t(language, 'mimicCatalog.versionLabel')}</label>
          <input
            type="text"
            value={version}
            onChange={(e) => {
              setVersion(e.target.value);
              dirty();
            }}
            placeholder="v1.4.0"
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm font-mono border border-gray-500 focus:border-blue-400 outline-none"
          />
          <p className="text-[10px] text-gray-500 mt-0.5">{t(language, 'mimicCatalog.versionHint')}</p>
        </div>
        <div>
          <label className="text-xs text-gray-400">{t(language, 'mimicCatalog.releaseBaseLabel')}</label>
          <input
            type="text"
            value={releaseBase}
            onChange={(e) => {
              setReleaseBase(e.target.value);
              dirty();
            }}
            placeholder="https://github.com/hack3ric/mimic/releases/latest/download"
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm font-mono border border-gray-500 focus:border-blue-400 outline-none"
          />
          <p className="text-[10px] text-gray-500 mt-0.5">{t(language, 'mimicCatalog.releaseBaseHint')}</p>
        </div>
        <div>
          <label className="text-xs text-gray-400">{t(language, 'mimicCatalog.fallbackDefaultLabel')}</label>
          <select
            value={fallbackDefault}
            onChange={(e) => {
              setFallbackDefault(e.target.value);
              dirty();
            }}
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm border border-gray-500 focus:border-blue-400 outline-none"
          >
            <option value="">{t(language, 'mimicCatalog.fallbackDefaultUnset')}</option>
            <option value="udp">{t(language, 'mimicCatalog.fallbackDefaultUdp')}</option>
            <option value="none">{t(language, 'mimicCatalog.fallbackDefaultNone')}</option>
          </select>
          <p className="text-[10px] text-gray-500 mt-0.5">{t(language, 'mimicCatalog.fallbackDefaultHint')}</p>
        </div>
      </div>

      {/* Per-distro .deb rows */}
      <div className="space-y-2 p-3 bg-gray-900 border border-gray-700 rounded">
        <div className="flex items-center justify-between gap-2">
          <h4 className="text-sm font-semibold text-gray-200">{t(language, 'mimicCatalog.debsHeading')}</h4>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => void handleDiscover()}
              disabled={discovering || busy || loading}
              className="px-3 py-1 text-xs bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
            >
              {discovering ? t(language, 'mimicCatalog.discovering') : t(language, 'mimicCatalog.discoverButton')}
            </button>
            <button
              type="button"
              onClick={() => void handleAssist()}
              disabled={busy || loading}
              className="px-3 py-1 text-xs bg-sky-600 hover:bg-sky-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
            >
              {busy ? t(language, 'mimicCatalog.assisting') : t(language, 'mimicCatalog.assistButton')}
            </button>
          </div>
        </div>
        <p className="text-[10px] text-gray-500">{t(language, 'mimicCatalog.debsHint')}</p>

        {/* Discover checklist: pick + label the release's .deb assets, then add them as empty-SHA rows. */}
        {discovered !== null && (
          <div className="space-y-2 p-2 bg-gray-800 border border-indigo-800 rounded">
            <div className="flex items-center justify-between gap-2">
              <h5 className="text-xs font-semibold text-indigo-300">{t(language, 'mimicCatalog.discoverHeading')}</h5>
              <button
                type="button"
                onClick={() => setDiscovered(null)}
                className="px-2 py-0.5 text-[10px] bg-gray-700 hover:bg-gray-600 rounded text-gray-200"
              >
                {t(language, 'mimicCatalog.discoverDismiss')}
              </button>
            </div>
            <p className="text-[10px] text-gray-500">{t(language, 'mimicCatalog.discoverHint')}</p>
            {discovered.map((r) => {
              const isDup = r.checked && r.key.trim() !== '' && dupKeys.has(r.key.trim());
              const isBlank = r.checked && r.key.trim() === '';
              return (
                <div key={r.asset} className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={r.checked}
                    onChange={(e) => setDiscoveredRow(r.asset, { checked: e.target.checked })}
                    aria-label={r.asset}
                    className="accent-indigo-500"
                  />
                  <span className="font-mono text-[11px] text-gray-300 flex-1 break-all">{r.asset}</span>
                  <input
                    type="text"
                    value={r.key}
                    onChange={(e) => setDiscoveredRow(r.asset, { key: e.target.value })}
                    placeholder={t(language, 'mimicCatalog.keyLabel')}
                    aria-label={t(language, 'mimicCatalog.keyLabel')}
                    className={`w-32 px-2 py-0.5 bg-gray-600 rounded text-[11px] font-mono border outline-none ${
                      isDup || isBlank ? 'border-amber-500' : 'border-gray-500 focus:border-blue-400'
                    }`}
                  />
                </div>
              );
            })}
            {hasBlankCheckedKey && <p className="text-[10px] text-amber-400">{t(language, 'mimicCatalog.discoverNeedKey')}</p>}
            {dupKeys.size > 0 && <p className="text-[10px] text-amber-400">{t(language, 'mimicCatalog.discoverDupKey')}</p>}
            <button
              type="button"
              onClick={handleAddSelected}
              disabled={!canAddSelected}
              className="px-3 py-1 text-xs bg-indigo-600 hover:bg-indigo-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
            >
              {t(language, 'mimicCatalog.discoverAddSelected')}
            </button>
          </div>
        )}
        {debs.length === 0 ? (
          <p className="text-xs text-gray-500">{t(language, 'mimicCatalog.noDebs')}</p>
        ) : (
          debs.map((row) => (
            <div key={row.id} className="space-y-1 border-t border-gray-800 pt-2">
              <div className="grid grid-cols-1 gap-1">
                <div className="flex gap-2">
                  <input
                    type="text"
                    value={row.key}
                    onChange={(e) => setRow(row.id, { key: e.target.value })}
                    placeholder="bookworm-amd64"
                    aria-label={t(language, 'mimicCatalog.keyLabel')}
                    className="flex-1 px-2 py-1 bg-gray-600 rounded text-sm font-mono border border-gray-500 focus:border-blue-400 outline-none"
                  />
                  <button
                    type="button"
                    onClick={() => removeRow(row.id)}
                    disabled={busy || loading}
                    className="px-2 py-1 text-xs bg-gray-700 hover:bg-gray-600 disabled:bg-gray-700 rounded text-gray-200"
                  >
                    {t(language, 'mimicCatalog.removeRow')}
                  </button>
                </div>
                <input
                  type="text"
                  value={row.asset}
                  onChange={(e) => setRow(row.id, { asset: e.target.value })}
                  placeholder="mimic_1.4.0_amd64.deb"
                  aria-label={t(language, 'mimicCatalog.assetLabel')}
                  className="w-full px-2 py-1 bg-gray-600 rounded text-sm font-mono border border-gray-500 focus:border-blue-400 outline-none"
                />
                <input
                  type="text"
                  value={row.sha256}
                  onChange={(e) => setRow(row.id, { sha256: e.target.value })}
                  placeholder={t(language, 'mimicCatalog.sha256Placeholder')}
                  aria-label={t(language, 'mimicCatalog.sha256Label')}
                  className="w-full px-2 py-1 bg-gray-600 rounded text-sm font-mono border border-gray-500 focus:border-blue-400 outline-none"
                />
              </div>
              {row.note && <p className="text-[10px] text-amber-300/80">{row.note}</p>}
            </div>
          ))
        )}
        <button
          type="button"
          onClick={addRow}
          disabled={busy || loading}
          className="px-3 py-1 text-xs bg-gray-700 hover:bg-gray-600 disabled:bg-gray-700 rounded text-gray-200"
        >
          + {t(language, 'mimicCatalog.addRow')}
        </button>
        <p className="text-[10px] text-amber-300/80">{t(language, 'mimicCatalog.assistCustody')}</p>
      </div>

      {/* GitHub proxy echo (read-only) */}
      <div className="space-y-0.5">
        <label className="text-xs text-gray-400">{t(language, 'mimicCatalog.proxyLabel')}</label>
        <p className="text-sm font-mono text-gray-300 break-all">{proxyText}</p>
        <p className="text-[10px] text-gray-500">{t(language, 'mimicCatalog.proxyHint')}</p>
      </div>

      <p className="text-[10px] text-gray-500 border-t border-gray-700 pt-2">{t(language, 'mimicCatalog.custodyNote')}</p>

      {validationHint && <p className="text-xs text-amber-400 bg-amber-900/20 px-2 py-1 rounded">{validationHint}</p>}
      {localError && <p className="text-xs text-red-400 bg-red-900/20 px-2 py-1 rounded break-all">⚠️ {localError}</p>}
      {saved && <p className="text-xs text-green-300 bg-green-900/20 px-2 py-1 rounded">{t(language, 'mimicCatalog.savedNotice')}</p>}

      <button
        onClick={() => void handleSave()}
        disabled={loading || busy || validationHint !== null}
        className="px-4 py-1.5 text-sm bg-emerald-600 hover:bg-emerald-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
      >
        {loading ? t(language, 'mimicCatalog.saving') : t(language, 'mimicCatalog.saveButton')}
      </button>
    </div>
  );
}
