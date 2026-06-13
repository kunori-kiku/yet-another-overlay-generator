# plan-1.5 — Panel bootstrap-URL composition: server-authoritative agent prefix
<!-- drafted: 2026-06-13 (insertion point under M1; trigger pre-declared in outline) -->
Outline: [`outline.md`](outline.md) · Prerequisites: plan-1 substeps 1–2 committed

## Trigger

The plan-1 sweep found `EnrollmentFlow.tsx` composing the bootstrap one-liner from the panel's
`pathPrefix` mirror — which post-split mirrors the OPERATOR prefix — and the manual enroll
command from the bare agent base URL with no prefix at all. With distinct prefixes, both
displayed commands point fleet operators at paths that 404 on the agent port.

## Goal

The panel never guesses the agent prefix: the server reports it read-only in `GET /settings`
(`agent_path_prefix`, normalized "" or "/<seg>"), and EnrollmentFlow composes both the
bootstrap one-liner and the manual enroll command from it. No second user-typed mirror field
(server-authoritative — the subject's own principle, D1 applied to config).

Reads from specs: controller-agent-api (bootstrap composition), panel-deploy-fleet (enrollment UI)

## Read first

1. `internal/api/handler_bootstrap.go:26-66` (settingsJSON + GET branch).
2. `frontend/src/api/controllerClient.ts:568-615` (ControllerSettings/SettingsJSON/mapSettings).
3. `frontend/src/components/deploy/EnrollmentFlow.tsx:13-40` (both command compositions).

## Implementation steps

1. **Server**: `settingsJSON` gains `AgentPathPrefix string `json:"agent_path_prefix"``,
   populated from `h.agentPrefix` in BOTH the GET and POST responses (read-only: the POST
   decode ignores any submitted value — it is env-derived, not a stored setting).
2. **Client**: `ControllerSettings.agentPathPrefix` + mapping; `postSettings` does not send it.
3. **EnrollmentFlow**: both commands append `settings?.agentPathPrefix ?? ''` to their base;
   the operator-prefix mirror (`pathPrefix`) is no longer consulted here.
4. Tests: extend `controller_prefix_test.go` — GET /settings reports the agent prefix;
   subject-scoped.

## Verification

`go test ./internal/api/` + `cd frontend && npm run lint && npm run build`. Manual check rides
in plan-7's closure smoke (enroll a node via the displayed one-liner on a prefixed deploy).

## Tests produced by this plan

- Assertion added to `internal/api/controller_prefix_test.go` — subject-scoped (same retirement).

## Definition of done

- [ ] GET /settings carries `agent_path_prefix`; POST ignores it.
- [ ] EnrollmentFlow commands carry the server-reported agent prefix; operator mirror unused there.
- [ ] Go + frontend gates green; committed on the plan-1 branch.

## Out of scope

Any other consumer of the operator-prefix mirror (controllerClient request composition is
CORRECT — it talks to the operator API); settings storage schema changes.
