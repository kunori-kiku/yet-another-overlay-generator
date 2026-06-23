import { useState } from 'react';
import { useTopologyStore } from '../../stores/topologyStore';
import { t } from '../../i18n';

export function DomainList() {
  const { domains, removeDomain, reorderDomains, selectDomain, selectedDomainId, language } = useTopologyStore();
  const [draggingId, setDraggingId] = useState<string | null>(null);

  if (domains.length === 0) {
    return (
      <p className="text-xs text-[var(--content-muted)] italic">{t(language, 'domainList.noDomainsCreatedYet')}</p>
    );
  }

  return (
    <div className="space-y-2">
      {domains.map((domain) => (
        <div
          key={domain.id}
          draggable
          onDragStart={() => setDraggingId(domain.id)}
          onDragOver={(e) => e.preventDefault()}
          onDrop={() => {
            if (draggingId) {
              reorderDomains(draggingId, domain.id);
            }
            setDraggingId(null);
          }}
          onDragEnd={() => setDraggingId(null)}
          onClick={() => selectDomain(domain.id)}
          className={`p-2 rounded text-sm space-y-1 cursor-pointer border ${
            selectedDomainId === domain.id
              ? 'bg-[var(--control-hover)] border-[var(--accent)]'
              : 'bg-[var(--control)] border-transparent hover:border-[var(--hairline)]'
          }`}
          title={t(language, 'domainList.clickToEditDrag')}
        >
          <div className="flex items-center justify-between">
            <span className="font-medium text-blue-400">☰ {domain.name}</span>
            <button
              onClick={(e) => {
                e.stopPropagation();
                removeDomain(domain.id);
              }}
              className="text-[var(--danger)] hover:text-[var(--danger)] text-xs"
              title={t(language, 'domainList.delete')}
            >
              ✕
            </button>
          </div>
          <div className="text-xs text-[var(--content-muted)]">
            CIDR: {domain.cidr} | {domain.routing_mode} | {domain.allocation_mode}
          </div>
        </div>
      ))}
    </div>
  );
}
