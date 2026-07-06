import type { MessageKey } from '../i18n';

// mimicCond.ts holds the PURE label logic for the rich `mimic` Node Condition chip (plan-6): the
// closed reason→label-key map. In lib/ (not the .tsx) so it is unit-testable in the node-env vitest
// without a React import — same pattern as nodeConditions.ts. The chip's COLOR comes from
// nodeConditions.conditionVisual (the single status→class authority shared with the generic strip),
// so there is no separate mimic color map to drift.

// REASON_LABEL maps a mimic condition reason (the CLOSED enum classifyMimic emits, plan-5 agent) to
// an i18n message key. The keys MUST equal the agent's reason codes verbatim. The agent's `Unknown`
// reason and any future reason fall through to a generic label at the call site (never blank).
export const REASON_LABEL: Record<string, MessageKey> = {
  Active: 'mimicCond.active',
  FellBackToUDP: 'mimicCond.fellBack',
  KernelTooOld: 'mimicCond.kernelTooOld',
  EbpfLoadFailed: 'mimicCond.ebpfFailed',
  InstallFailed: 'mimicCond.installFailed',
  EgressUnresolved: 'mimicCond.egressUnresolved',
  NativeDowngradedSkb: 'mimicCond.nativeDowngraded',
  ModuleUnavailable: 'mimicCond.moduleUnavailable',
  Stopped: 'mimicCond.stopped',
};
