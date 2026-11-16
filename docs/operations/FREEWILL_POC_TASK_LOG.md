# Freewill POC Task Log

This public-safe log records scoped work, observed results, limitations, and next actions. It contains no raw local E-series artifacts or private project history.

## PR3 — sanitized Freewill POC remainder

**Roadmap IDs:** FW-P2, FW-P3, FW-G2, and the 73 locked FW/TEL/DEV/REL/DIA/SEC/OPS/CORE requirements.

**Remote baseline:** PR1 merge SHA 441207fbde631b06ebd5b28dafb3b284fc7a6ab8; PR2 #146 merge SHA 694a633532a4b08e5b0eeaecf0d1a90da9475f90.

**Expected:** Carry the reviewed Freewill POC remainder onto the PR2 merge with production code, regression coverage, configuration/reference updates, UI integration, and sanitized design/inventory documentation. Keep authority in the existing workflow, approval, policy, event, API, and runtime boundaries.

**Observed:** The candidate contains the dependency-coupled 117-path remainder. The canonical inventory retains 73 unique requirement IDs, five CTX items, and nine phases. Preference evidence and challenge GET routes now require the configured service bearer when local principal auth is not configured. No raw evidence artifacts, machine-specific paths, private project source, or prior POC/E-series local branch/revision lineage were added in PR3.

**Verification:** the focused preference read-auth regression, staticcheck ./..., go build ./..., go vet ./..., and a captured full go test ./... -count=1 run after the review fix passed. Earlier full-suite attempts included one FAIL with truncated terminal output; later captured runs passed, so the earlier result was not reproduced. The added-line privacy scan found no local-path or credential-token patterns; sample project details are generic. git diff --check passed.

**Limitations:** Windows checks do not establish Linux OCI execution, provider-backed R1/G1 acceptance, served P2, exact-head CI, or production readiness.

**Next action:** Finish final diff/privacy review and independent review. Keep the candidate local until the open execution and CI gates are satisfied.

## 2026-11-15 — CTX-05 GLM schema and feedback PR4 review candidate

**Roadmap IDs:** FW-003; CTX-01–05. Prepared local branch `codex/freewill-ctx05-pr4-prep` from the PR3 candidate tree `79a0b4c78d90688a8bd82b3abb5a3f4020eebc2f`.

The four-file code slice adds native JSON schemas to the exact GLM Flash idea and choice requests, retains host binding for source/action/candidate IDs, and keeps non-GLM selection on its existing text protocol. GLM retry feedback now matches each emitted Flash parser/action/choice failure category; choice correction names the idea schema because recovery restarts at idea generation. No new package or authority path was introduced.

**Verification:** Focused multiagent GLM/parser/recovery tests passed. Synthetic CLI tests for exact host binding, fake served event admission, and safe failure diagnostics passed. `git diff --check` passed. No live GLM request was made; test fixtures and examples use synthetic project data.

**Limitations and next action:** Provider schema compatibility and CTX-05 semantic acceptance remain open. Request independent review and exact-head CI before publication; keep the candidate local and unpublished pending those checks.

## 2026-11-15 — CTX-05 Cloud schema transport and host-constraint correction

The explicit direct Ollama Cloud route now forwards the GLM Flash selector output schema; the local Ollama compatibility route continues to omit the schema for this model. The idea schema states the only valid candidate/sleep combinations and adds the parser's straightforward idea/rationale length bounds. Host parsing remains authoritative for byte limits, minimum rationale word count, unsafe content, provenance, and action binding. No new package or authority path was introduced.

**Verification:** Mocked transport asserts the exact synthetic schema in the direct Cloud wire payload and confirms the local route omits it. Focused multiagent schema/recovery tests passed. No live Cloud request was made because process credentials and explicit Cloud opt-ins were unavailable.

**Limitations and next action:** Exact `glm-5.3-flash:cloud` provider compatibility and CTX-05 semantic acceptance remain open. Continue with independent review and exact-head CI; do not publish pending those checks.

## 2026-11-15 — CTX-02 host-pinned source revision refresh E66

**Roadmap IDs:** FW-P1 / FW-003 and CTX-02. Branch `codex/freewill-ctx02-refresh-slice` is based on the corrected PR4 head `866de44673761ab0cd15bfd5f6169ac1726c525e` (PR #148, tree `7e71d8a820a6441092b2b26f39f4d6d7bccfb34d`).

Extended the existing E56 standalone serve test with two synthetic host-imported source revisions. Process A persists a pinned profile and README action; after it exits, the host fixture and configured commit pin advance to revision B; process B selects a host-bound action using both current source IDs. The test reads both runs back after process B exits and verifies A's manifest remains pinned, B's source digests and action basis references are current, the manifest does not contain the README body, only the expected pending-proposal event is present, approval-decision/dispatch/tool/action effects are absent, and the synthetic project workspace is unchanged by either serve process. The existing profile importer already binds repository URL, commit SHA, and profile digest behind authorized project-read scope, so this slice required tests only and did not add a fetch client or authority path. The test provider and repository fixtures are synthetic; no private project content or credentials are included.

**Verification:** `go test -count=1 ./cmd/prizm-cli -run '^TestE56StandaloneServeBindsSourceBackedActionAcrossRestart$'` passed. `go test -count=1 ./internal/tool -run '^(TestPrivateProjectProfileImportIsPinnedAndReadOnly|TestBuildFreewillProjectContextRefreshesSafeSourceRevision)$'` passed. `gofmt` and `git diff --check` passed. The fake provider phase detector was changed from prompt wording to stable `project_context`/`candidates` request fields after the first run exposed the prompt wording change.

**Limitations and next action:** E66 proves host-pinned profile refresh between local served processes, not a live GitHub Plugin fetch or semantic entailment. Any actual project refresh remains an owner-driven GitHub Plugin operation with a host-supplied exact revision; the Prizm runtime does not fetch GitHub. Keep this local candidate unpublished until PR4 is merged; then coordinate a retarget/restack onto `codex/freewill-integration` and require exact-head CI. Do not target staging or claim CTX-02 complete.

## 2026-11-15 — CTX-05 PR4 restacked onto PR3 integration merge

Restacked the independently reviewed PR4 change series onto exact integration merge `46f42596275091b2b734344afe6bc8c5102bf9cf` (tree `8b63bd28dc54358b30e016487af2fbdd168512fc`). `git range-diff` matched all six reviewed logical commits. PR4 remains a four-path code change plus public-safe handoff documentation; it does not add a package or widen authority.

**Verification:** `go test -count=1 ./internal/provider/ollama ./internal/workflow/multiagent` passed. Focused synthetic E57 host-binding, fake-serve admission, and safe-diagnostic tests passed. `git diff --check` and gofmt passed. No live Cloud call was made because the process-scoped key and explicit opt-in flags were absent.

**Limitations and next action:** Exact-model Cloud schema compatibility and CTX-05 semantic acceptance remain open. Publish the clean branch to `codex/freewill-integration`, require exact-head CI and review clearance, then merge into the Freewill integration branch only.

## 2026-11-15 — CTX-02 E66 restacked after PR4 merge

PR #148 merged into `codex/freewill-integration` at `a554f08bd297a1408e929819c84ffd1b64bd491f` (tree `7e71d8a820a6441092b2b26f39f4d6d7bccfb34d`); CI run #291 is green. Restacked E66 onto the exact merge commit. The change remains one existing CLI regression-test extension and append-only handoff evidence; no production runtime, package, provider, or authority changes.

**Verification:** On the restacked tree, `go test -count=1 ./cmd/prizm-cli -run '^TestE56StandaloneServeBindsSourceBackedActionAcrossRestart$'` passed; `go test -count=1 ./internal/tool -run '^(TestPrivateProjectProfileImportIsPinnedAndReadOnly|TestBuildFreewillProjectContextRefreshesSafeSourceRevision)$'` passed. `gofmt -d cmd/prizm-cli/freewill_source_action_standalone_serve_test.go` and `git diff --check` were clean.

**Limitations/next action:** The synthetic two-process test proves host-pinned revision A/B import, provenance and basis binding, persistence across restart, and zero project/approval/dispatch/tool/action effects aside from the expected pending proposal. It does not exercise live GitHub Plugin refresh or real-model semantic entailment. Await independent review before publication; then target only `codex/freewill-integration` and require exact-head CI. Do not stage or deploy.

## 2026-11-15 — CTX-02 reviewer assertion hardening

Strengthened E66's restart assertion to compare the entire revision A persisted source manifest, serialized before refresh and after revision B completes. Strengthened the B manifest content-exclusion check to reject each nonempty line from both synthetic README revisions in raw and JSON-encoded form. Updated the current plan handoff to reflect PR4's merge/green CI and the actual review/acceptance gates.

**Verification:** `go test -count=1 ./cmd/prizm-cli -run '^TestE56StandaloneServeBindsSourceBackedActionAcrossRestart$'` passed after the assertion changes. `gofmt` and `git diff --check` passed.

**Limitations/next action:** Test and documentation only; no production fix or authority expansion. Keep unpublished pending independent reviewer clearance. Live GitHub Plugin refresh and model semantic acceptance remain open.

## 2026-11-15 — FW-02 served event and scheduled-wake acceptance

**Roadmap IDs:** FW-P1 / FW-002 (FW-02). Branch `codex/fw02-served-event-timer` is based on integration commit `937a2f10898be4fec5deed23bb12d4f768520c82`.

Extended the existing NATS serve-composition test to enable both the event allowlist and scheduled wake source. An unlisted event and a subject/envelope type mismatch invoke no selector. One allowed event and one synthetic scheduled tick, delivered serially through the same started Freewill runtime, persist two distinct sleeping runs. Each has exactly one wake claim, selection-started event, and selection-committed event; both remain execution-disabled and have no approval, dispatch, tool, action, or apply event. Reopening the runtime/store and replaying the same event ID adds no run and makes no provider request. The test uses a local fake provider and an embedded NATS bus; no live provider or Cloud request was made.

**Verification:** `go test -count=1 ./cmd/prizm-cli -run '^TestFreewillEventWakeNATSServeAllowlistAndDedupeAfterRuntimeReopen$'` passed. `gofmt` and `git diff --check` passed.

**Limitations and next action:** The test injects a scheduled NATS tick rather than waiting for the cron scheduler, and closes/reopens the composed runtime in one test process rather than restarting the `prizm serve` OS process. A wake delivered while the actor admission slot is occupied is not buffered; this slice serializes timer/event wakes and adds no retry queue. Visual evidence is not applicable to this backend behavior. Keep this local and unpublished until the earlier GLM/CTX ordering is resolved and an independent review clears it; do not target staging.

## 2026-11-15 — CTX-05 host-owned GLM line mapper

**Roadmap IDs:** FW-003; CTX-01–05. Isolated branch `codex/freewill-ctx05-host-mapper` restacked on PR #150 merge `86701aca71a2f8cdebe6df71b942d5f5861a7379` (all required CI green).

The Flash idea stage now requests three bounded labeled-text candidate blocks without a native JSON schema. The host maps only recognized fields into typed candidates and creates candidate IDs. Score, nonempty factor allowlist, exact source IDs, uncertainty, ACTION_ID uniqueness and exact host binding, scope, budget, rationale, and `decision_only` remain strictly validated. Phase-two choice keeps its host-ID-bound schema and tools remain absent. The numeric score decoder remains exact and range-bounded.

**Verification:** Focused multiagent parser/selection/recovery/score tests, direct Cloud wire tests, and E57 offline binding/fake-serve/denied-egress/safe-diagnostic tests passed. No full-suite result is claimed.

**Provider evidence:** Controlled synthetic direct probes `SRCREF-L1/L2`, `FACT-L1/L2`, and `CHOICE-C1` succeeded at the field-shape stage; raw text was discarded. The first real served line-mapper harness attempt remained semantically unsuccessful: after two wakes/two recovery attempts it failed at `duplicate_project_action`, following `invalid_project_basis`; zero candidates/choice and zero effects, four-call maximum. The harness metadata for that first attempt did not include a full run ID. One prompt refinement limits ACTION_ID to one candidate; rerun the same bounded synthetic harness and retain the sanitized run ID. CTX-05 acceptance remains open pending host-bound basis/action and choice success plus independent review.

## 2026-11-15 — CTX-05 served line-contract retry evidence

`TestE57StandaloneLiveGLMSyntheticActionGrounding` on exact integration base `86701aca71a2f8cdebe6df71b942d5f5861a7379` produced run `freewill-db90a3d08f2f1fde7a0bd23e09a6c82d`. The test remained inside its 4-call upper bound and recorded zero tools/effects and no raw output. Two wakes/two recoveries ended with `duplicate_project_action`, then `invalid_scored_ideas_idea_shape`; no candidate or choice was produced. The test PASS is safety-only; CTX-05 semantic acceptance remains open. Further Cloud calls are paused pending independent review of host-side duplicate action handling.

## 2026-11-15 — CTX-05 independent action-binding review correction

The duplicate-action failure was caused by an unnecessary cross-candidate uniqueness check. Removed that restriction in selector and service normalization: distinct ideas may reference the same exact host action, while each binding still requires the exact issued ID and source. Tests retain rejection for unissued action IDs, source mismatch, duplicate ideas, and unsafe/oversized idea text. Cloud line protocol is scoped to `glm-5.3-flash:cloud`; local `glm-5.3-flash` keeps JSON schema and feedback behavior. A bounded served retry remains before semantic acceptance can be considered.

## 2026-11-15 — CTX-05 final bounded served retry

`TestE57StandaloneLiveGLMSyntheticActionGrounding` run `freewill-93b2b7ef05b77493d319a33d3a13f605` used the exact PR #150 integration base `86701aca71a2f8cdebe6df71b942d5f5861a7379` and the isolated host mapper. It stayed within 4 Cloud calls and recorded zero tools/effects and no raw output. Two wakes/two recoveries ended with `invalid_scored_ideas_rationale`, then `invalid_project_basis`; candidate count and choice were zero. The Go test PASS covers safety assertions only. Semantic acceptance remains open; pause further Cloud calls pending independent review of the remaining response-contract mismatch.

## 2026-11-15 — CTX-05 compact authorized Cloud projection

Restacked on PR #150 integration `86701aca71a2f8cdebe6df71b942d5f5861a7379`. Exact `glm-5.3-flash:cloud` now receives a bounded compact projection with only egress-authorized source IDs/content and supplied roadmap action ID/text/source ID; the full snapshot and provenance remain host-side. Profile and source metadata, including paths, revisions, digests and commit identifiers, are excluded. Existing selector Cloud-egress authorization is required. Local Flash JSON/schema behavior is retained. The prompt requests `IDEA`, `SCORE`, `FACTORS`, `BASIS_REFS`, `UNCERTAINTY`, and `RATIONALE`; `ACTION_ID` is optional only when host actions are supplied. Host parsing/binding remains fail-closed.

**Verification:** `go test ./internal/workflow/multiagent -count=1`, `go test ./internal/provider/ollama -count=1`, and selected offline E57 binding/fake-serve/egress/metadata tests passed. Projection privacy/size/egress tests and `BASIS_REFS: NONE` parsing passed. Independent privacy/authority review cleared the projection.

**Served evidence:** `TestE57StandaloneLiveGLMSyntheticActionGrounding`, run `freewill-64ee7cc2efde1caf46e10d6236ccd8c8`, completed two wakes and two recovery attempts within the four-call cap. Metadata outcomes were `invalid_project_basis` then `invalid_choice`; final diagnostics described a fenced response with seven choice-incompatible top-level keys. Candidate count and choice were zero, host action/basis/uncertainty bindings were false, effects were zero, and raw output was not recorded. The harness exited successfully on safety assertions only; semantic acceptance remains open. Visual review is not applicable.

**Next action:** Keep this work unpublished. Diagnose the remaining basis and choice contract failures through sanitized host metadata and independent review. Do not weaken exact basis/action checks or broaden Cloud egress; require a later bounded synthetic served run to produce a host-bound candidate and allowed choice before marking CTX-05 accepted.

## 2026-11-15 — CTX-05 Cloud choice mapper and served semantic pass

Added a Cloud-only compact phase-two choice request containing the objective and host-issued candidate IDs, validated ephemeral model IDEA labels, advisory scores, and factors. The line parser accepts exactly one `CHOICE_ID` plus one bounded safe `REASON`, or exact `NONE`; it rejects arbitrary JSON, fences, duplicate/conflicting IDs, unknown candidate IDs, unsafe reasons, and tool calls. The host retains the action-bound title/goal, scope, budget, candidate ID, and persisted reason. The local Flash JSON/schema choice path is unchanged. Candidate display labels are ephemeral and validated; they cannot rewrite the host-bound proposal.

**Served evidence:** `TestE57StandaloneLiveGLMSyntheticActionGrounding`, run `freewill-c7cf9b56e8c6c73893d138f4d38c8283`, completed 2 wakes/1 recovery within the four-call maximum. First attempt returned `invalid_project_basis`; the recovery succeeded with 3 candidates, an allowed choice, exact host action and basis binding, uncertainty and source manifest present, and status pending approval. Effects were zero; `raw_output_recorded=false`. The harness exited successfully. This is one bounded synthetic semantic pass, not general entailment proof. No private BassBook source or raw model output was sent or retained. Visual review is not applicable.

**Verification/next action:** Full `go test ./internal/workflow/multiagent -count=1` passed (76.6s); `go test ./internal/provider/ollama -count=1` and selected offline E57 binding/fake-serve/egress/metadata tests passed; focused choice parser/projection/host-binding tests passed; `gofmt` and `git diff --check` passed. Commit the local milestone and send the stable HEAD/diff/evidence for independent review. Keep unpublished pending review and exact-head CI; no staging or deployment.

## 2026-11-15 — FW-G2/E45 native OCI emergency-halt acceptance

**Roadmap IDs:** FW-P2 and FW-G2; R1/G1 remains open. Branch
`codex/freewill-g2-e45-halt` is based on Freewill integration merge
`445953cc7e07977654f4989829d9d52a535b77ed` (PR #152), which includes the
earlier PR #151 integration.

Ran the existing Linux-only
`TestFreewillProductionOperatorHaltDuringOCI` against Docker Desktop Linux
engine 29.9.2 and the already-cached immutable Debian image
`debian@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc`.
The synthetic served workflow grants only its test-created parent intention,
starts an OCI child, then issues a same-scope halt during an active child
command. The test confirms the container is reaped; parent and child remain
canceled; run, child, and selected-intention correlation IDs survive reopen;
no child patch approval/apply, validation, or approved artifact occurs; and
same-runtime recovery/unhalt and a fresh durable runtime plus approval service
reopened from SQLite do not replay the child or make additional provider calls.
The synthetic source repository remains unchanged.

**Verification:** Because the host is Windows and the test has a Linux build
tag, the package test binary was cross-compiled then run as a Linux process in
a disposable Debian runner with the existing Docker socket. `docker.io` and
`git` were installed only inside that throwaway runner; no image was pulled or
deleted. The strengthened test passed in 8.14s. Visual evidence is not
applicable.

**Limits and next action:** This closes only the E45 execution-evidence gap for
one synthetic in-flight halt. Broad FW-G2 isolation, identity, budget, network,
secret, and revocation gates remain open, as does full R1/G1 acceptance.
Request independent review of the local test/docs diff before a PR; no staging
merge or deployment.

## 2026-11-15 — CTX-05 Cloud choice egress boundary correction

Independent review identified that the compact Cloud choice context must not forward optional preference hypothesis text or source-pair references without a separate Cloud-egress authorization. Omitted those fields from the exact Cloud route; the existing local Flash JSON route retains the preference context. Added a regression proving Cloud payload exclusion and made `NONE` a reserved, case-insensitive no-op token that cannot be issued as a candidate ID. This does not change the served E57 path, which uses no preference influence. Targeted tests passed; rerun the full relevant suite before the final local commit/review.

**Verification:** Full `go test ./internal/workflow/multiagent -count=1` passed (81.1s); `go test ./internal/provider/ollama -count=1` and selected offline E57 tests passed; targeted Cloud omission/`NONE` tests passed; `gofmt` and `git diff --check` passed. The prior served run remains applicable because this correction removes optional data from Cloud choice context and reserves an impossible host candidate ID; it does not change the synthetic served request or line protocol.

## 2026-11-16 — CTX-05 PR #151 merge and exact-head CI

PR #151 merged into `codex/freewill-integration` at `1f31d058bccb5753c7e2558e0aaaab218ef46aa8`. Exact-head CI run #296 (`43798511932`) passed Linux build/tests/race, Windows, Python, OCI sandbox, and lint. The CI lint correction lowercased two score-validation error strings. The full suite also identified stale synthetic provider fixtures; they now assert the compact authorized source/action projection and use the Cloud choice line contract. Focused CLI regressions passed, including the standalone fake event test with three candidates, two local fake calls, sleeping decision-only state, and zero effects. The bounded live synthetic result remains `freewill-c7cf9b56e8c6c73893d138f4d38c8283`: three candidates and allowed choice, host action and basis binding, uncertainty/source manifest valid, pending approval, zero effects, and raw output not recorded.

**Open gates:** This is one bounded synthetic semantic pass. General semantic entailment, real-project grounding, and live GitHub refresh remain open. No staging or deployment.

## 2026-11-15 — FW-G2/E68 stale approval revocation before OCI launch

**Roadmap IDs:** FW-P2 and FW-G2; R1/G1 remains open. Branch
`codex/freewill-g2-revocation-before-oci` is based on integration merge
`61295cfb1f0505ba8c8e522fe101e2ade9a4e055` (PR #153, containing E45). Test-only
commit `e1dc64a456e5aeb703deeba16c8552721d7d7749` extends the existing Linux OCI
production-cycle test file; no production code or package boundary changed.

`TestFreewillProductionRevokedApprovalBeforeOCILaunch` creates a synthetic
pending intention approval, toggles halt and unhalt through the authenticated
operator-control API for the exact synthetic agent/project/workspace scope,
then submits the stale grant through the served approval API. The API returns
HTTP 409. The approval's captured generation remains the old value while the
durable control generation advances; a reopened read-only SQLite status reader
projects the approval as revoked/halted and execution-disabled. The durable
parent checkpoint and approval row remain approval-pending; this test does not
claim that a terminal parent state is persisted. No grant decision, child
dispatch, child approval/apply, validation, provider call, managed OCI container
change, source artifact, or source-repository mutation occurred.

**Verification:** `GOOS=linux GOARCH=amd64 go test -c -o
.tmp/freewill-b2-revocation.test ./cmd/prizm-cli` passed on Windows. The exact
test passed on Linux against Docker Engine 29.9.2 and cached image
`debian@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc`
using `-test.run=^TestFreewillProductionRevokedApprovalBeforeOCILaunch$ -test.count=1 -test.timeout=7m`
in a disposable Debian runner with the existing Docker socket. `docker.io` and
`git` were installed only in the runner's `--rm` container; no image was pulled
or deleted. `git diff --check` passed. Visual evidence is not applicable.

**Limits/next action:** E68 establishes one synthetic invalidated-grant denial
before child dispatch. It does not complete B2 or R1/G1, and it does not test
actual child OCI execution. Network/secret, identity, budget, isolation/path
escape, and other authorization gates remain open. Obtain independent review
of the local diff before opening a PR to `codex/freewill-integration`; no
staging merge or deployment.
