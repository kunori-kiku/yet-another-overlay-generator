# Plan 1.5 — Insertion: pre-existing frontend lint failures block CI (D18 pulled forward)

Outline: [outline.md](outline.md) · Trigger: PR #3 (docs-only) CI run — `Go vet + test` PASSED on
untouched code, but `Frontend lint + build` FAILED with 3 pre-existing `react-hooks/refs` errors
in `frontend/src/components/canvas/TopologyCanvas.tsx`.

## Why this insertion exists

The outline pre-declared this exact failure mode ("plan-1.5 — ci.yml red on untouched main").
The lint errors are not noise: ESLint independently flagged **audit finding D18** (side-effecting
`useMemo` blocks calling `setNodes`/`setEdges` during render, plus `positionMap.current` ref
read/write during render — the cause of nodes jumping back to stale coordinates after unrelated
edits). D18 was scheduled for Plan 9; CI blocking every PR pulls it forward.

Per the program's no-workarounds principle, the fix is the REAL D18 fix — not eslint-disable
comments.

## The fix (already implemented)

`frontend/src/components/canvas/TopologyCanvas.tsx`:
1. `flowNodes` memo is now pure — computes default grid positions only; no ref access in render.
2. The two side-effecting `useMemo` sync blocks became `useEffect`s; `positionMap.current`
   seeding/merging happens inside the effect (legal ref access), preserving drag positions and
   fixing the snap-back bug.

## Landing path

Committed on `plan/audit-remediation-2026-06` (so PR #3 goes green), then merged up the stacked
branches (`fix/port-endpoint-ownership` → PR #4, `fix/compile-feedback-api-hardening` → Plan 3 PR).

## Definition of done

- [ ] PR #3 CI fully green (both jobs).
- [ ] PR #4 CI green after base merge-up.
- [ ] Plan 9's scope note updated: D18 done here, not in Plan 9.
