import { useTopologyStore } from '../../../stores/topologyStore';
import { t } from '../../../i18n';

// Domain property editor (extracted verbatim from RightPanel's selected-domain block; used by the selection-driven Design right-side aside).
export function DomainEditor() {
  const language = useTopologyStore((s) => s.language);
  const domains = useTopologyStore((s) => s.domains);
  const selectedDomainId = useTopologyStore((s) => s.selectedDomainId);
  const updateDomain = useTopologyStore((s) => s.updateDomain);
  const removeDomain = useTopologyStore((s) => s.removeDomain);

  const selectedDomain = domains.find((d) => d.id === selectedDomainId);
  if (!selectedDomain) return null;

  return (
    <section>
      <h2 className="text-sm font-semibold text-[var(--content-muted)] uppercase tracking-wider mb-2">
        {t(language, 'domainEditor.domainProperties')}
      </h2>
      <div className="space-y-2">
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'domainEditor.name')}</label>
          <input
            type="text"
            value={selectedDomain.name}
            onChange={(e) => updateDomain(selectedDomain.id, { name: e.target.value })}
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-[var(--content-muted)]">CIDR</label>
          <input
            type="text"
            value={selectedDomain.cidr}
            onChange={(e) => updateDomain(selectedDomain.id, { cidr: e.target.value })}
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'domainEditor.transitCIDROptional')}</label>
          <input
            type="text"
            value={selectedDomain.transit_cidr || ''}
            onChange={(e) =>
              updateDomain(selectedDomain.id, {
                transit_cidr: e.target.value.trim() || undefined,
              })
            }
            pattern="^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\/\d{1,2}$"
            title={t(language, 'domainEditor.ipv4CIDRFormatE')}
            placeholder="10.10.0.0/24"
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)] focus:border-[var(--accent)] outline-none"
          />
        </div>
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'domainEditor.routingMode')}</label>
          <select
            value={selectedDomain.routing_mode}
            onChange={(e) =>
              updateDomain(selectedDomain.id, {
                routing_mode: e.target.value as 'babel' | 'static' | 'none',
              })
            }
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
          >
            <option value="babel">Babel</option>
            <option value="static">Static</option>
            <option value="none">None</option>
          </select>
        </div>
        <div>
          <label className="text-xs text-[var(--content-muted)]">{t(language, 'domainEditor.allocationMode')}</label>
          <select
            value={selectedDomain.allocation_mode}
            onChange={(e) =>
              updateDomain(selectedDomain.id, {
                allocation_mode: e.target.value as 'auto' | 'manual',
              })
            }
            className="w-full px-2 py-1 bg-[var(--control)] rounded text-sm border border-[var(--hairline)]"
          >
            <option value="auto">Auto</option>
            <option value="manual">Manual</option>
          </select>
        </div>
        <button
          onClick={() => removeDomain(selectedDomain.id)}
          className="w-full py-1 bg-[var(--danger-solid)] hover:bg-[var(--danger-solid)] text-[var(--danger-solid-fg)] rounded text-sm"
        >
          {t(language, 'domainEditor.deleteDomain')}
        </button>
      </div>
    </section>
  );
}
