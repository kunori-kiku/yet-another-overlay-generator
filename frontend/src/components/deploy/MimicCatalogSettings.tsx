import { useEffect, useRef, useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { useControllerStore, selectHasAuth } from '../../stores/controllerStore';
import { t, type UILanguage } from '../../i18n';
import { type ControllerSettings } from '../../api/controllerClient';

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

  const [version, setVersion] = useState(initial.mimicVersion);
  const [releaseBase, setReleaseBase] = useState(initial.mimicReleaseBase);
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

  const handleSave = async () => {
    if (validate()) return;
    setLocalError(null);
    const current = useControllerStore.getState().settings ?? initial;
    const err = await saveSettings({
      ...current,
      mimicVersion: version.trim(),
      mimicReleaseBase: releaseBase.trim(),
      mimicDebs: filledDebs(),
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
            placeholder="https://github.com/.../releases/download/v1.4.0"
            className="w-full px-2 py-1 bg-gray-600 rounded text-sm font-mono border border-gray-500 focus:border-blue-400 outline-none"
          />
          <p className="text-[10px] text-gray-500 mt-0.5">{t(language, 'mimicCatalog.releaseBaseHint')}</p>
        </div>
      </div>

      {/* Per-distro .deb rows */}
      <div className="space-y-2 p-3 bg-gray-900 border border-gray-700 rounded">
        <div className="flex items-center justify-between">
          <h4 className="text-sm font-semibold text-gray-200">{t(language, 'mimicCatalog.debsHeading')}</h4>
          <button
            type="button"
            onClick={() => void handleAssist()}
            disabled={busy || loading}
            className="px-3 py-1 text-xs bg-sky-600 hover:bg-sky-500 disabled:bg-gray-600 disabled:text-gray-400 rounded text-white font-medium"
          >
            {busy ? t(language, 'mimicCatalog.assisting') : t(language, 'mimicCatalog.assistButton')}
          </button>
        </div>
        <p className="text-[10px] text-gray-500">{t(language, 'mimicCatalog.debsHint')}</p>
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
