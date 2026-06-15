# Plan 4.3 — Phase 2c: controller compile/deploy + HTTP surface + mTLS + agent integration

Parent: [plan-4-2026_06_08.md](plan-4-2026_06_08.md) · Prereq: 4.1 (persistence) + 4.2 (enrollment)
merged. Third Phase 2 sub-plan — large, so split into three stacked PRs:

- **4.3a — compile/stage (this file, detailed).** Server-side render-ready compile producing signed
  per-node bundles into the Store. No HTTP. CI-testable.
- **4.3b — HTTP surface + TLS 1.3 + mTLS + auth middleware.** `/api/v1/controller/*` on an env-gated
  controller mode of `cmd/server`; tenant+node from the mTLS cert CN at one chokepoint. CI via httptest.
- **4.3c — agent mTLS integration + end-to-end test.** Agent `enroll` subcommand + mTLS pull/poll/
  report; in-process httptest+TLS+dev-CA+MemStore e2e (enroll→deploy→poll→config→verify→apply[mocked]→
  report). Real-host two-node smoke remains the manual gate.

## 4.3a goal

`internal/controller/compile.go`: turn the stored public-keys-only topology + the enrolled registry
into **signed per-node bundles staged at the next generation**, applying the user-chosen
**render-what's-ready** policy (2026-06-08): render only the **enrolled subgraph**; an edge whose far
end has not enrolled is omitted and activates on a later deploy.

## Design (adopted)

- **Reuse the tested export path, no refactor, no duplication.** Build the enrolled-subgraph
  `model.Topology`, then run the SAME pipeline the air-gap path uses —
  `render.GenerateKeys(topo, render.AgentHeld)` → `compiler.Compile` → `render.All` →
  `artifacts.Export(result, tempDir)` — into a temp dir, then read each node dir back into a
  `map[string][]byte` and `StageBundle` it. Signing is the Phase-0 `YAOG_BUNDLE_SIGNING_KEY` path
  inside `Export` (so staged bundles carry `bundle.sig`). The temp-dir round-trip is a deliberate,
  low-risk reuse of the fully-tested exporter for an infrequent operator action; an in-memory
  `Export` is a possible later optimization. (This keeps the compiler/renderer/exporter **frozen**.)
- **Enrolled subgraph filter** (the render-ready policy): include a node iff its registry record is
  `NodeApproved` with a non-empty `WGPublicKey`; set that node's `WireGuardPublicKey` from the
  registry; drop any edge with an unenrolled endpoint. Feed the filtered topology to the unchanged
  pipeline (which then renders only the present peers). Zero-knowledge holds: `GenerateKeys(AgentHeld)`
  emits the placeholder; the registry never held a private key.
- **node.Name vs node.ID:** export names dirs by `node.Name`; the Store + agent key by `node.ID`. The
  controller holds the topology, so it maps `node.Name → node.ID` when reading temp dirs back to
  `StageBundle` (the documented 4.2 wart; full unification is a later cleanup).
- **Generation:** `CompileAndStage` stages bundles; the operator `PromoteStaged` (existing Store
  method) flips them to current and bumps the generation. The bundle's generation is recorded in the
  `SignedBundle.Generation` (the next generation = `CurrentGeneration+1`), giving 4.3c a
  signed-content-bound anti-rollback signal (closing the Phase-1b unsigned-manifest gap once the agent
  keys anti-rollback on it).

## Implementation steps

1. `internal/controller/compile.go`:
   - `type StageResult { Staged []string; SkippedUnenrolled []string; Generation int64 }` (per-node
     readiness for the 4.4 UI).
   - `CompileAndStage(ctx, store Store, t TenantID, now time.Time) (StageResult, error)`: `GetTopology`
     → unmarshal `model.Topology` → build enrolled subgraph from `ListNodes` → if no enrolled nodes,
     return an empty StageResult (nothing to deploy, not an error) → run the pipeline into a temp dir
     (`os.MkdirTemp`, `defer RemoveAll`) → for each enrolled node read `<tmp>/<node.Name>/` into a
     file map → `StageBundle(SignedBundle{NodeID: node.ID, Generation: CurrentGeneration+1, Files})` →
     `AppendAudit("stage")`. Return the readiness lists.
   - Helper `enrolledSubgraph(topo, nodes) (filtered model.Topology, skipped []string)`.
2. Tests `internal/controller/compile_test.go`: build a topology + a partially-enrolled MemStore;
   assert (a) only enrolled nodes get staged bundles, unenrolled are in SkippedUnenrolled; (b) a staged
   bundle's wg confs contain the enrolled peers' public keys and the PRIVATEKEY_PLACEHOLDER (never a
   real private key — reuse the custody-guard idea); (c) edges to unenrolled peers are absent;
   (d) after a later enroll + re-compile, the previously-skipped node is staged and the now-mutual edge
   appears; (e) empty registry → empty StageResult, no error. (Signing off in CI → bundles still
   stage; a signed variant via t.Setenv covers bundle.sig presence.)
3. Spec `docs/spec/controller/deploy.md` (compile/stage/promote + the render-ready policy + the
   reuse-the-exporter decision + generation); README index. (HTTP routes documented in 4.3b.)

## Definition of done (4.3a)

- [ ] CI green; render-ready compile stages only enrolled nodes; re-compile fills in as the fleet
      enrolls; no staged bundle contains a parseable WG private key; compiler/renderer/exporter
      untouched; no new go.mod dep.

## Out of scope (4.3b / 4.3c / Plan 5)

HTTP endpoints, TLS/mTLS, the auth chokepoint (4.3b); agent mTLS + the e2e test (4.3c); multi-tenant
enforcement, KMS, OIDC, stage→promote step-up (Plan 5).
