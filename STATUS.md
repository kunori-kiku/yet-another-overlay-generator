# STATUS
<!-- regenerated: 2026-07-17 -->
<!-- by: Codex — v2.0.0-rc.14 is GitHub Latest from annotated tag object
50be73e0fe4f4617dd3099dec5269c889353a803, targeting
c2f8963277d7db774896d79179d62c0f38ea6632. Release run 29586806508 succeeded and
published the exact 22 verified assets. GHCR 2.0.0-rc.14 and latest share parent
sha256:ff215250b4ed8a12a90e8d179fc326440cce7392fc322919258c242fd35dd9a4. -->

## Active work

- **✅ `v2.0.0-rc.14` — RELEASED / GITHUB LATEST (2026-07-17).** The Design canvas now zooms out
  to 0.1 percent so the supported 2,000-node topology can fit in one viewport. Independent,
  default-on controls hide link endpoint hosts/ports or node overlay IPs without editing topology
  data, changing deployment dirty state, or losing endpoint names and selection targets.
- **Source and CI evidence:** PR #312 merged the two independently re-reviewed fixes as
  `c2f8963277d7db774896d79179d62c0f38ea6632`. PR CI run `29586044454` and exact-main push run
  `29586405662` passed all seven required jobs. Local lint, production build, focused regressions,
  and the complete 430-test Vitest suite were green before merge.
- **Publication evidence:** annotated tag object `50be73e0fe4f4617dd3099dec5269c889353a803`
  peels to that exact source commit. Release run `29586806508` passed every tag-time gate, built and
  verified all seven target bundles, ran the native Windows checks, published the exact 22-asset
  set, and promoted the release as GitHub Latest. A fresh public download passed
  `verify-release-assets.sh` against all 22 files.
- **Container evidence:** GHCR `2.0.0-rc.14` and `latest` both resolve to
  `sha256:ff215250b4ed8a12a90e8d179fc326440cce7392fc322919258c242fd35dd9a4`. Independent local
  verification confirmed native `linux/amd64` child
  `sha256:61f9b8648fd6eeabff3486e365b90def67031408630750680c351b63c43eb648` and `linux/arm64`
  child `sha256:f7b3d6f6948a114e81bc0556f0b4d2e271130a1d689bd98257f105a01d26f9d5`, exact labels and
  entrypoint, ELF machine types, and both runtimes reporting `v2.0.0-rc.14`. Docker Hub was not
  configured and was correctly skipped.

## Open questions / blockers

- No rc.14 implementation or publication blocker remains.
- The address controls are deliberately browser-local presentation preferences and default to the
  pre-rc.14 detailed view. They do not require a controller or agent upgrade to preserve topology
  or deployment compatibility.

## Next actions

1. Operators can update to `v2.0.0-rc.14` / GHCR `2.0.0-rc.14` (or `latest`).
2. Record later work under the fresh `Unreleased` heading and use the next immutable version.
3. Treat feedback from unusually large real topologies as follow-up tuning rather than changing
   topology or deployment semantics.

## Recently closed subjects (last 3)

- `rc14-canvas-scalability-readability` (2026-07-17, Delivered): deeper fit/zoom plus independent
  display-only link-address and overlay-IP controls; published as rc.14.
- `rc13-agent-selfupdate-health-retry` (2026-07-17, Delivered): retained the candidate and rollback
  breadcrumb across transient health failures and published rc.13.
- `deployment-stability-and-charted-telemetry-2026_07_17` (2026-07-17, Delivered): rc.12 fixes,
  URL/device telemetry, chart/history framework, reviews, specs, and publication completed.
