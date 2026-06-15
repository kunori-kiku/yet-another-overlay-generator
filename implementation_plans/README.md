# `implementation_plans/` — handoff convention

Durable multi-session work plans. Each *subject* gets one folder; each *session* executes one
plan file. A fresh session must be able to pick up any subject from its `outline.md` alone.

## Layout

```
implementation_plans/
  README.md                          <-- this file
  <subject>-<YYYY_MM_DD>/            <-- one folder per subject
    outline.md                       <-- durable spine: mission, principles, milestones, decisions
    plan-1-<YYYY_MM_DD>.md           <-- one session each
    plan-2-<YYYY_MM_DD>.md
    plan-2.5-<YYYY_MM_DD>.md         <-- insertion point, drafted only when blocked
    ...
  _completed/
    <archived-subjects>/
```

## Subject naming

Descriptive English phrases (`audit-remediation-and-allocation-stability`), never codes
(`p7_2c18`, `phase_b`). Someone scanning `_completed/` in a year must recognize the work.

## Plan numbering

Integers from 1, incrementing. Insertion-point plans use decimals (`plan-2.5-<date>.md`) and are
drafted ONLY by the executing session when blocked, AFTER updating the outline to wire the
insertion in. Plan dates are draft dates.

## Authorship contract

- **outline.md** owns: mission + success criteria, principles (invariants with risk class),
  current state of the world, must-read references, decisions log, all milestones with proposed
  solutions + hazards + verification gate + stop-loss, insertion-point markers, closure criteria,
  plan status table.
- **plan-N.md** owns: one session of concrete work — prerequisites, goal, `Reads from specs:`
  list, read-first file:line list, implementation steps with exact anchors/commands/commit
  templates, tests produced (with lifecycle classification), definition of done, out of scope.

## Off-track recovery

When execution hits a wall: STOP. Update the outline (decisions log + status table), draft
`plan-N.5` describing the unblock work, ask the user. Never improvise past a HIGH-risk principle.

## Closure ritual

Verify the outline's closure checklist, refresh `STATUS.md`, `git mv` the subject folder to
`_completed/`, commit + push.

## Why this exists

Multi-session work dies in handoff. The folder is the contract that keeps long-running subjects
shippable across sessions, agents, and context resets.

## Project-specific note

This repo has TWO spec trees. `Reads from specs:` lists name the **flat root `specs/<component>`** cache
(e.g. `controller-stage-promote`, `model-validation`, `panel-auth`) — that is what
`execute-implementation-plan` partial-loads as `<repo-root>/specs/<component>.md` (it STOPs on a missing
file; see `specs/README.md`). The deeper, nested prose lives at `docs/spec/<area>/<file>.md` (e.g.
`docs/spec/artifacts/mimic.md`, `docs/spec/compiler/validation.md`) and is referenced by explicit path in a
plan's `Read first` list when needed.
