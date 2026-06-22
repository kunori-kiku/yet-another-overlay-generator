import type { MessageKey } from '../i18n';

// mimicCond.ts holds the PURE logic for the rich `mimic` Node Condition chip (plan-6): the closed
// reason→label-key map and the status→Tailwind-class resolver. In lib/ (not the .tsx) so it is
// unit-testable in the node-env vitest without a React import — same pattern as nodeConditions.ts.

// REASON_LABEL maps a mimic condition reason (the CLOSED enum classifyMimic emits, plan-5 agent) to
// an i18n message key. The keys MUST equal the agent's reason codes verbatim. An unknown future
// reason falls through to a generic label at the call site (never blank) — forward-compatible.
export const REASON_LABEL: Record<string, MessageKey> = {
  Active: 'mimicCond.active',
  FellBackToUDP: 'mimicCond.fellBack',
  KernelTooOld: 'mimicCond.kernelTooOld',
  EbpfLoadFailed: 'mimicCond.ebpfFailed',
  InstallFailed: 'mimicCond.installFailed',
};

// mimicStatusClass maps a condition status (plan-1's closed ok/warn/error/unknown set) to a Tailwind
// badge class. A mimic fallback is a `warn` (it de-cloaks the link — read loud). Any unrecognized
// status lands in the neutral 'unknown' bucket so the chip never renders blank.
export function mimicStatusClass(status: string): string {
  switch (status) {
    case 'ok':
      return 'bg-green-900/40 text-green-300 border-green-700';
    case 'warn':
      return 'bg-amber-900/40 text-amber-300 border-amber-700';
    case 'error':
      return 'bg-red-900/40 text-red-300 border-red-700';
    default: // 'unknown' (model.ConditionStatusUnknown) + any future value
      return 'bg-gray-800 text-gray-400 border-gray-600';
  }
}
