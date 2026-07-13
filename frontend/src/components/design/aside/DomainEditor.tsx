import { useTopologyStore } from '../../../stores/topologyStore';
import { t } from '../../../i18n';
import { Field, FIELD_SELECT_CLASS } from '../../../ui/Field';

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
        <Field
          label={t(language, 'domainEditor.name')}
          type="text"
          value={selectedDomain.name}
          onChange={(e) => updateDomain(selectedDomain.id, { name: e.target.value })}
        />
        <Field
          label="CIDR"
          type="text"
          value={selectedDomain.cidr}
          onChange={(e) => updateDomain(selectedDomain.id, { cidr: e.target.value })}
        />
        <Field
          label={t(language, 'domainEditor.transitCIDROptional')}
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
        />
        <Field label={t(language, 'domainEditor.routingMode')}>
          <select
            value={selectedDomain.routing_mode}
            onChange={(e) =>
              updateDomain(selectedDomain.id, {
                routing_mode: e.target.value as 'babel' | 'static' | 'none',
              })
            }
            className={FIELD_SELECT_CLASS}
          >
            <option value="babel">Babel</option>
            <option value="static">Static</option>
            <option value="none">None</option>
          </select>
        </Field>
        <Field label={t(language, 'domainEditor.allocationMode')}>
          <select
            value={selectedDomain.allocation_mode}
            onChange={(e) =>
              updateDomain(selectedDomain.id, {
                allocation_mode: e.target.value as 'auto' | 'manual',
              })
            }
            className={FIELD_SELECT_CLASS}
          >
            <option value="auto">Auto</option>
            <option value="manual">Manual</option>
          </select>
        </Field>
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
