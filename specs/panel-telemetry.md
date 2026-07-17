# Panel telemetry

<!-- last-verified: 2026-07-17 -->

## Responsibility

Own the Fleet node-detail editor and observation UI for signed active checks, automatic device
telemetry, live results, and exact-series history charts
(`frontend/src/components/pages/FleetNodeDetailPage.tsx:192-244`,
`frontend/src/components/pages/FleetNodeDetailPage.tsx:306-319`).

## Files

- `frontend/src/components/deploy/TelemetryProbeEditor.tsx:28-309` — edits bounded ICMP, TCP, and URL probe drafts.
- `frontend/src/components/deploy/TelemetryDevicePanel.tsx:67-250` — controls automatic device discovery and renders live inventory/readings.
- `frontend/src/components/deploy/TelemetryProbeResults.tsx:58-202` — joins configured and reported probes and renders latest outcomes.
- `frontend/src/components/deploy/NodeResourceHistory.tsx:149-422` — owns range, resolution, exact selector, retry, and chart state.
- `frontend/src/lib/telemetryHistory.ts:245-276` — serializes exact probe/device history selectors for the controller API.
- `frontend/src/components/charts/TimeSeriesChart.tsx:1-210` — renders the shared accessible time-series presentation.

## Inputs

`panel-deploy-fleet` supplies the selected topology node, controller-reported probe/device data,
agent capabilities, keystone state, and the whole-design Save action through the Fleet node page
(`frontend/src/components/pages/FleetNodeDetailPage.tsx:49-87`,
`frontend/src/components/pages/FleetNodeDetailPage.tsx:192-244`). `controller-telemetry` supplies live
node observations and `NodeHistory` responses through the controller store
(`frontend/src/components/deploy/NodeResourceHistory.tsx:149-194`).

## Outputs

Editors update the in-memory topology node and Save persists the whole design draft; activation stays
with the separate Deploy flow (`frontend/src/components/pages/FleetNodeDetailPage.tsx:192-229`). The
history view emits one exact probe selector and at most one exact device selector, then renders
resource, latency, availability, and catalog-defined device series
(`frontend/src/components/deploy/NodeResourceHistory.tsx:205-231`,
`frontend/src/components/deploy/NodeResourceHistory.tsx:484-508`,
`frontend/src/components/deploy/NodeResourceHistory.tsx:612-705`).

## Decision points (if any)

- Manual nodes cannot enable active telemetry; managed nodes see keystone/capability readiness before
  activation (`frontend/src/components/pages/FleetNodeDetailPage.tsx:77-87,159-178`,
  `frontend/src/components/deploy/TelemetryProbeEditor.tsx:36-86`,
  `frontend/src/components/deploy/TelemetryDevicePanel.tsx:84-153`).
- Probe type selects host/optional port or a distinct URL plus expected status contract
  (`frontend/src/components/deploy/TelemetryProbeEditor.tsx:147-246`).
- Current configured probe and detected-device choices determine the exact history request; absent
  choices explicitly suppress those families (`frontend/src/components/deploy/NodeResourceHistory.tsx:205-231`).

## Invariants

- Fetched history and live probe/device payloads stay outside browser persistence; the custody
  allowlist strips live telemetry and omits the Fleet freshness clock
  (`frontend/src/stores/controller/persist.ts:14-45`).
- URL actual status is latest-result context, while charts use measured latency and availability
  (`frontend/src/components/deploy/TelemetryProbeResults.tsx:126-185`,
  `frontend/src/components/deploy/NodeResourceHistory.tsx:490-508`).
- Missing history observations become explicit chart gaps while valid numeric zero remains data; the
  shared chart renderer does not connect across null points (`frontend/src/lib/telemetryHistory.ts:190-197`,
  `frontend/src/lib/telemetryHistory.ts:596-650`,
  `frontend/src/components/charts/TimeSeriesChart.tsx:203-216,252-265`).

## Gotchas (optional)

- Save may preserve an incomplete draft; Deploy readiness is the activation boundary
  (`frontend/src/components/pages/FleetNodeDetailPage.tsx:71-87`,
  `frontend/src/components/pages/FleetNodeDetailPage.tsx:210-229`).
- A failed history refresh retains the last successful chart and exposes Retry instead of clearing it
  (`frontend/src/components/deploy/NodeResourceHistory.tsx:171-194`,
  `frontend/src/components/deploy/NodeResourceHistory.tsx:324-394`).
- History families and device numeric definitions are exhaustive registries consumed by the renderer;
  adding a charted family or device value requires updating the shared registry rather than an ad-hoc
  panel field (`frontend/src/lib/telemetryHistory.ts:40-45,589-594`,
  `frontend/src/components/deploy/NodeResourceHistory.tsx:403-422,612-621`).
