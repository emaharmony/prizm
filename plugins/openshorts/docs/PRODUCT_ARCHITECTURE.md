# OpenShorts VOD Clipper — Product Boundary and Plugin Roadmap

Status: Architecture direction agreed 2026-11-24. This document is a plan, not a claim that the capabilities below are implemented.

## Decision

Build the VOD clipper as a **standalone application** with an optional **Prizm plugin**. Do not embed a video-processing pipeline into Prizm core.

The clipper must eventually run without Prizm and expose an independent API/CLI. The Prizm plugin is a thin distribution/control adapter that registers MCP tools, guides setup, maps job events, and enforces Prizm-side permissions. The same clipping engine must be usable by a non-Prizm customer.

Continue prototype development on `codex/openshorts-plugin` and retain PR #161 as the integration review. Do not extract a new product repository until actual VOD processing and quality-verified export work. This is a staging strategy, **not** a reason to introduce permanent Prizm internal dependencies into the engine.

## Current verified branch baseline

The current `plugins/openshorts` branch package contains:
- Import/re-import registration for an external MCP server in `prizm.yaml`, and plugin-owned state initialization.
- MCP tools `guide`, `run_demo`, and `status`.
- A synthetic creator-VOD owner and export sink with durable job records, receipts, deduplication/recovery rules, and tests.
- V1 contract files and a Prizm workflow-adapter skill.

**Not yet proven by this branch:** intake of real files, an OpenShorts transcription call, real FFmpeg renders, inspection of actual output MP4s, verified delivered links, or standalone execution of the clipping engine without Prizm internals. The demo is mock-only and must be labeled as such.

## Product architecture

### Standalone engine owns
1. Intake: stable/complete file detection, source fingerprint, authorized local path handling, deduplication and non-destructive source retention.
2. Analysis: OpenShorts transcription and optional evidence adapters for audio, visual gameplay, webcam observations, and chat when available.
3. Editorial intelligence: Highlight Investigator and Narrative Director, with source-grounded timestamps and candidate rationale.
4. Media execution: existing approved Lumi/OpenClaw Playbook and FFmpeg compositor, or portable equivalents behind interfaces; avoid replacing proven rendering.
5. Quality: technical checks, actual rendered-frame/audio review, bounded targeted repairs and rejection of unresolved defects.
6. Durable jobs: manifests, artifact inventory, idempotent export, crash recovery and a standalone API/CLI.

### Prizm plugin owns
1. Installation/configuration and MCP tool discovery/health.
2. Mapping Prizm requests to versioned standalone engine operations.
3. Translating job lifecycle to Prizm events and reporting results.
4. Prizm-specific policy, approval, agent guidance and optional creator-memory integration.

### Dependency rule

Engine -> no Prizm imports, configuration, database schema, message bus, or mandatory runtime.

Plugin -> may depend on Prizm MCP/protocol integration, but accesses the engine only via published versioned interfaces.

The existing `creatorvod` package currently imports Prizm internal workflow/persistence modules. Before claiming standalone independence, isolate or replace those imports in the engine layer without weakening recovery semantics.

## Five selected skills

- `highlight-investigator`: evidence-backed candidate selection, including low-speech gameplay; returns candidate manifest entries, never renders.
- `narrative-director`: hook/setup/payoff and valid source boundaries; outputs a constrained, executable edit plan.
- `playbook-constrained-renderer`: validates the active policy and per-VOD geometry before invoking rendering; outputs a render receipt.
- `visual-quality-inspector`: checks *actual final MP4s*; outputs per-gate verdicts, evidence frames, and optional targeted repair requests.
- `prizm-workflow-adapter`: maps plugin actions to the independent engine and reports durable status; does not own the media pipeline.

Common interface: versioned Clip Decision Manifest, immutable absolute source timestamps with explicit offset mapping, source fingerprint, policy/recipe versions, artifact hashes, QA evidence, and recovery identifiers. Keep candidate rank, editorial decisions, and final QA scores separate.

## Preserve existing Pentakewl policy

- Preserve v8 gameplay-first composition (approximately 64% game / 36% webcam) and the actual approved canvas/geometry.
- Recalibrate webcam placement per VOD; do not assume fixed coordinates or silently change source layout.
- No burned captions or title/hook overlays for Pentakewl unless separately approved.
- Target **up to 10 strong clips**, no filler; each delivered clip QA >= 8.0, batch average >= 8.5, with all applicable hard gates passed.
- Inspect final first/middle/last frames, transitions, source-relevant defect timestamps, audio and output availability; retain screenshots/links in the task log.
- Default to at most two targeted repairs per clip, with a fresh inspection after every rerender.
- Export is **not** publication. No automatic social posting or production-style changes without explicit authorization.

## Next milestone: one real VOD, one verified clip

1. Audit actual plugin branch, local Playbook authority and currently deployed OpenShorts/Lumi endpoints; distinguish source behavior from mock behavior.
2. Use an authorized **copy of the smallest accessible real VOD**; preserve the source and avoid the normal cleanup/retention path.
3. Feed it through actual OpenShorts transcription and the existing approved compositor, via the proposed engine interface.
4. Render one actual clip, run `ffprobe` and independent visual/audio QA, store screenshots/manifest, and verify its accessible export path/link.
5. Confirm idempotent resubmission and restart/recovery for this same real job, with no duplicate clip or destructive file changes.
6. Report the exact input path, output link, command/test results, deficiencies and evidence. Do not claim success from a mock receipt.

An early pilot may invoke an existing Lumi worker as a compatibility backend, but that is a temporary integration seam, not a permanent requirement for the product.

## Rollout gates

- Gate A: prove real copied-VOD intake and transcription without altering production services.
- Gate B: prove approved render and independent final-media QA for one clip.
- Gate C: prove verified delivery, crash recovery and non-duplication.
- Gate D: exercise the same engine through a standalone CLI/API **without Prizm running**.
- Gate E: expose that operation through the Prizm plugin and then scale to a batch.

Use real test media rather than adding more synthetic demo scenarios except where an isolated test is necessary to prove failures/retries. Preserve production baselines and require approval for changes to live deployment, locked Playbook styling, publishing settings or destructive storage.

## Commercialization checklist (later, not a blocker for the prototype)

- Audit licenses and distribution restrictions for every reused OpenShorts component, bundled assets, models, codecs and upload/publishing services.
- Separate creator profiles and tenant-specific job/artifact storage; define secrets isolation and retention/deletion controls.
- Document OS-dependent ffmpeg/model requirements and a supported installation story.
- Keep publishing opt-in and user-authorized; implement platform compliance and service limits before any hosted release.
- Benchmark runtime cost, model usage and clip QA on real VODs before pricing or performance claims.

## Branch discipline

Keep development on `codex/openshorts-plugin`. Do not merge to `staging` or export this as an official public plugin until real media and standalone boundaries are verified. Document each test with evidence and linked playable outputs.
