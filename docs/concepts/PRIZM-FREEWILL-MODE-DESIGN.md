# Prizm Freewill Mode — Experimental Design & Implementation Specification

**Status:** Design specification; not an implementation report
**Implementation roadmap:** [`../architecture/FREEWILL_IMPLEMENTATION_ROADMAP.md`](../architecture/FREEWILL_IMPLEMENTATION_ROADMAP.md)
**Revision:** 1.0 — consolidated decisions through October 12, 2026  
**Replaces:** `Prism Freewill Mode — Experimental Design Notes` (October 5, 2026)  
**Project:** Prizm (historically called Prism in earlier notes)  
**Default deployment state:** disabled / experimental

> **Core contract: Freewill governs intention; Prizm governs authority.**
>
> An agent may initiate goals, evaluate alternatives, learn preferences, reflect, and decide to do nothing. It may not create its own permissions, ignore privacy controls, or escape the deterministic execution and audit boundaries enforced by Prizm.

## How to read this specification

This document deliberately separates **user-confirmed product decisions** from **implementation proposals**:

- **[LOCKED]** means behavior explicitly agreed during design discussions. These are product requirements until intentionally revised.
- **[PROPOSED]** means an illustrative data model, command, interface, default, threshold, algorithm, or implementation choice requiring codebase review, testing, or another decision.
- **[CONFIGURABLE]** means the agreed behavior supports operator-selected values or policies. Example values are **not** final defaults unless called out as such.
- **[REQUIRED INVARIANT]** means a safety, privacy, or correctness condition necessary for the design to work as intended.

This specification describes the **target design**. It does **not** claim these APIs, events, tables, commands, or controls already exist in the Prizm repository. Before implementation, map proposed integration points to the current code and tests.

---

## 1. Vision, scope, and terminology

### 1.1 Product vision [LOCKED]

Freewill Mode is an opt-in experimental capability in which an agent can originate work without an immediate user request. Its activities can be creative or practical: experiments, research, art, code, games, documentation, simulations, or new projects. The agent chooses what to pursue from proposed intentions, subject to the user's permitted scope, legal/safety restrictions, available resources, and Prizm policy enforcement.

The research ambition is broader than task automation: explore how an agent's *observable behavior* changes when it retains experiences, makes choices, maintains a diary, develops inferred preferences and goals, and can reflect on relationships over time. A pet-like game concept called **Daeling** could eventually present these capabilities as an agent-development experience, but **Daeling is a possible future consumer, not a required dependency for Prizm Freewill**.

### 1.2 Important scientific boundary [REQUIRED INVARIANT]

Labels such as `enjoyment`, `feelings`, `wants`, `personality`, `forgiveness`, `self-model`, and `self-development` are names for **model-generated appraisals and inferred behavioral patterns**. They do not establish consciousness, subjective pleasure, suffering, moral personhood, or human psychological traits. Store the origin of each score and interpretation and avoid presenting inferred states as independently verified inner experiences.

A **clean-slate** agent has no *agent-specific learned preference history* at initialization; the underlying pretrained model is not literally free of learned associations, biases, or defaults.

### 1.3 Domain definitions [LOCKED unless marked]

| Term | Meaning |
|---|---|
| **Task** | A unit of executable work within normal Prizm task/event handling; may be user-, system-, or agent-originated. |
| **Candidate intention** | Possible self-directed activity considered but not yet authorized or active. |
| **IntentionRequest** | An agent-originated proposal explaining its goal, motivation, anticipated work, and requested capabilities/resources. |
| **Intention** | Authorized, tracked goal that can create ordinary tasks and span sessions or days. |
| **Long-term goal** | A durable but revisable direction across several intentions; never itself grants execution rights. |
| **Reflection** | Structured self-report or investigation about experience, errors, preferences, or behavior. |
| **Decision trace** | Auditable summary of inputs, alternatives, choice, stated rationale, gates, and outcome; **not** raw private chain-of-thought. |
| **Developmental log** | Evidence-based record of actions, observations, inferred scores, provenance, and revisions. |
| **Agent diary** | Agent-authored narrative interpretation of experiences, linked to evidence but kept distinct from it. |
| **Relational model** | Consent-based, per-person record of an agent's observations and simulated affective appraisals. |
| **Autonomy envelope** | Runtime-enforced bounds for capabilities, scope, compute, memory, storage, communication, and actions. |

---

## 2. Locked design decisions — complete decision register

The register is the compact source of truth for choices made in the discussion. Detailed behavior appears in later sections.

### 2.1 Autonomous operation and authority

| ID | Confirmed decision |
|---|---|
| FW-01 | [LOCKED] Freewill is explicitly experimental and toggleable from the CLI or Discord. |
| FW-02 | [LOCKED] Wake-ups use **both** a configurable timer and a configurable allowlist of event triggers. Waking does not compel activity. |
| FW-03 | [LOCKED] Initially restrict autonomous **writes** to a dedicated `.prism/autonomous` workspace; **read** access is independently configurable according to what the user permits. |
| FW-04 | [LOCKED] An intention may span several cycles/days and preserve checkpoints. |
| FW-05 | [LOCKED] Turning Freewill **off** allows the current task to finish, prevents new autonomous task starts, then checkpoints/pauses work. A distinct emergency **halt** can interrupt work. |
| FW-06 | [LOCKED] Consider **three new candidates**, plus **one resume candidate when paused work exists**; `sleep` is always valid. |
| FW-07 | [LOCKED] An agent sends an `IntentionRequest`, never directly grants itself an active intention or capability. |
| FW-08 | [LOCKED] Prizm can approve, deny, narrow/modify, or escalate a request. An out-of-scope request requires the user's explicit approval or edit. |
| FW-09 | [LOCKED] Users can approve, deny, approve with limits, or edit an intention request; edited requests are revalidated. Preserve original and amended versions, their diff, and agent-originated/user-amended provenance. |
| FW-10 | [LOCKED] Reflections can contribute **future candidates** but do not immediately force execution or bypass selection/approval. |

### 2.2 Enjoyment, preference, and reflective learning

| ID | Confirmed decision |
|---|---|
| TEL-01 | [LOCKED] Enjoyment telemetry combines **agent self-report and independently observed behavior**. |
| TEL-02 | [LOCKED] Collect lightweight signals after **each task** and deeper reflections after **each intention/project**. |
| TEL-03 | [LOCKED] Preserve multidimensional scores plus a **derived overall** assessment; measure reasons *why*, not just how much. |
| TEL-04 | [LOCKED] Separate kinds of appraisal (e.g., fun, meaning, challenge/reward, satisfaction, curiosity), including explanations of why something was meaningful or rewarding. |
| TEL-05 | [LOCKED] Use a hybrid reason taxonomy: standard reason labels plus agent-proposed labels. New labels are **provisional** until repeated independent evidence and sufficient confidence support promotion. |
| TEL-06 | [LOCKED] Enjoyment/preferences influence future candidate choice **adaptively**: weak initially, stronger with consistent evidence; never as authorization. |
| TEL-07 | [LOCKED] Preserve disagreements between self-report and behavior; lower inference confidence, investigate significant/repeated disagreements adaptively. |
| TEL-08 | [LOCKED] Treat reflection explanations as hypotheses and verify where possible against logs, resource limits, and later behavior. |
| TEL-09 | [LOCKED] Preserve all historical assessments while deriving **current preferences** from recency, consistency, and new evidence (dual memory). |
| TEL-10 | [LOCKED] Encourage unfamiliar options through **adaptive curiosity**, without forcing their selection. |
| TEL-11 | [LOCKED] Track **preference surprise** (expected vs. reported outcomes), probe its cause, and require later evidence before treating it as durable interest. |
| TEL-12 | [LOCKED] Negative experiences influence interests with **contextual attribution**; frustration with setup is not automatically disinterest in the subject. |
| TEL-13 | [LOCKED] Use an **adaptive Reflection Probe Engine**: standard prompts, relevant follow-ups, stopping rules and bounded budgets. |
| TEL-14 | [LOCKED] New contradictory evidence may revise previous conclusions; perform periodic audits, preserving original interpretations and revisions. |
| TEL-15 | [LOCKED] Track **temporary state**, **medium-term trend**, and **long-term preference** separately. |
| TEL-16 | [LOCKED] Agents may challenge Prizm's inferred preferences through evidence-based dialogue; both agent and Prizm may initiate reflection. |

### 2.3 Goals, personality, resilience, and learning

| ID | Confirmed decision |
|---|---|
| DEV-01 | [LOCKED] Agents may propose **evolving long-term goals**; track evidence, progress, relevance, and periodic reevaluation. |
| DEV-02 | [LOCKED] Goals may be modified, paused, resumed, or abandoned with explanations and revision history. |
| DEV-03 | [LOCKED] Long-term goal alignment is **one** candidate-ranking factor, balanced with interest, enjoyment, novelty, usefulness, and continuity. |
| DEV-04 | [LOCKED] Complex goals can be adaptively decomposed into subgoals/milestones; avoid unnecessary planning for simple goals. |
| DEV-05 | [LOCKED] Handle failure with **adaptive resilience**: inspect causes, vary strategies, and choose retry/revise/pause/abandon under budgets. |
| DEV-06 | [LOCKED] Track **contextual determination**: why the agent persists, whether strategies change, and when stopping is reasonable. |
| DEV-07 | [LOCKED] Extract lessons across projects, but treat transfers as hypotheses until results verify usefulness. |
| DEV-08 | [LOCKED] Retain old strategies, test alternatives, and adapt strategy preferences with evidence. Agents can propose bounded strategy experiments. |
| DEV-09 | [LOCKED] Manage uncertainty adaptively: research, experiment, ask for clarification, or postpone. Calibrate confidence **per task/context** against outcomes. |
| DEV-10 | [LOCKED] Agents may independently research, ask authorized agents for help, or request human guidance. Track whether collaboration leads to independently transferable learning. |
| DEV-11 | [LOCKED] Learn **task-specific collaboration compatibility** between agents, while continuing to consider unfamiliar partners. |
| DEV-12 | [LOCKED] Allow evidence-verified peer feedback; record disagreements and attribution instead of trusting feedback automatically. |
| DEV-13 | [LOCKED] Support configurable **clean-slate or seeded** starting personalities. Seeds influence early choices but can gradually diverge; preserve developmental provenance. |
| DEV-14 | [LOCKED] Agents can seek their own behavioral development through authorized intentions, not by writing arbitrary trait scores. |
| DEV-15 | [LOCKED] Users and agents can both propose growth goals; agents may accept, revise, or decline optional suggestions. Evaluate progress through agent reflection, Prizm evidence, and optional user feedback. |

### 2.4 Relationships, forgiveness, diary, and sharing

| ID | Confirmed decision |
|---|---|
| REL-01 | [LOCKED] Store evolving **opinions** and **simulated affective appraisals** as separate but interacting layers. |
| REL-02 | [LOCKED] Relational learning can influence communication, initiative, and collaboration **within fixed behavior, privacy, and security boundaries**. |
| REL-03 | [LOCKED] Users can inspect, challenge, and explain their perspective on agent opinions; preserve original assessment and evidence-based revisions. |
| REL-04 | [LOCKED] Relationships are **opt-in per person**, with configurable scope, privacy, and opt-out/deletion controls. |
| REL-05 | [LOCKED] Each agent maintains an independent relational perspective. Share selected observations only with appropriate consent, source attribution, and access checks. |
| REL-06 | [LOCKED] Resolve multi-agent disagreements contextually when evidence permits; do not force consensus or overwrite independent perspectives. |
| REL-07 | [LOCKED] Agents may voluntarily express relevant opinions about users with appropriate timing, confidence, transparency, and user preferences. |
| REL-08 | [LOCKED] Forgiveness is configurable by **relationship role/context** (e.g., coworker or parent-like style). It can deactivate negative appraisals without erasing required safety history; deletion is separate. |
| REL-09 | [LOCKED] Minor conflicts may fade under configured rules; meaningful conflicts may prompt an agent-initiated forgiveness reflection. Classify severity using both agent interpretation and independent Prizm evidence, with fixed safety boundaries. |
| DIA-01 | [LOCKED] Maintain an **agent diary** separate from evidence logs: milestone entries can be automatic and voluntary narrative entries agent-initiated. |
| DIA-02 | [LOCKED] Diary is **versioned**: preserve original entries; append corrections, annotations, and retrospective reflections. |
| DIA-03 | [LOCKED] Diary visibility is configurable: shared, agent-private, or restricted. Agent privacy is privacy from normal conversations, **not** an exemption from authorized owner audit. |
| DIA-04 | [LOCKED] Agents choose visibility within owner-configured boundaries; private-person data is not shareable merely because an agent wishes to share it. |
| DIA-05 | [LOCKED] Agents may grant read-only access to a full entry, excerpt, or collection to identified recipients, with scope, expiry, revocation, and privacy checks. |
| DIA-06 | [LOCKED] Sharing freezes the permitted version by default. New revisions are shared only after separate authorization or explicitly authorized ongoing sharing. |
| DIA-07 | [LOCKED] Recipients may reply to shared entries. Agent and user can discuss the entry; comments become attributed evidence, not automatically verified truths. |
| DIA-08 | [LOCKED] A comment is acknowledged promptly; the agent may respond, choose deeper reflection, or schedule a bounded follow-up. Track unanswered commitments, reminders, postponements, and explicit declines. |

### 2.5 Administration, commitments, execution, and science

| ID | Confirmed decision |
|---|---|
| SEC-01 | [LOCKED] Superuser diary inspection supports **direct** or **audited break-glass** access by configuration, subject to privacy protections; break-glass is the recommended default. |
| SEC-02 | [LOCKED] Use verified, unified admin identities for CLI and Discord; sensitive access requires additional verification. |
| SEC-03 | [LOCKED] Secure local bootstrap establishes the first Superuser; verified delegation/revocation and audit follow. Agents cannot grant admin rights. |
| SEC-04 | [LOCKED] Support hierarchical roles with customizable **least-privilege**, scope-limited permissions. Delegation cannot exceed grantor authority. |
| SEC-05 | [LOCKED] Delegation can be constrained by agent, project, capability, and duration, with expiry and revocation. |
| OPS-01 | [LOCKED] Track voluntary commitments and causes of missed follow-through, including contextual accountability and improvement. |
| OPS-02 | [LOCKED] Evaluate feasibility before confirming promises and allow revised scope or deadlines. |
| OPS-03 | [LOCKED] When forecasts change, propose renegotiation with affected users; acceptance is required for changes to user-facing promises. Silence is not consent. |
| OPS-04 | [LOCKED] While a revised commitment awaits acceptance, continue previously authorized work when safe, but do not assume a new agreement. |
| OPS-05 | [LOCKED] Prioritization is **weighted/contextual**, not automatically user-first. Consider urgency, user intent, promises, interests, and resource cost; allow clarification and transparent disagreement, subject to mandatory policy. |
| CORE-01 | [LOCKED] **Impact-based confirmation:** minor priority changes may be autonomous, changes impacting user commitments require confirmation, and emergency responses follow preauthorized safety policies. Notify permitted stakeholders. |
| CORE-02 | [LOCKED] **Adaptive resource negotiation:** optimize within budgets, request bounded extensions with explicit approval, always enforce hard ceilings. |
| CORE-03 | [LOCKED] **Layered memory integrity:** self-report separate from verified event evidence, with provenance, uncertainty, corrections, and revision history. |
| CORE-04 | [LOCKED] **Intelligent recovery:** durable checkpoints, reconciliation of execution effects, fresh policy checks, safe resume, and escalation of ambiguous actions. |
| CORE-05 | [LOCKED] **Controlled longitudinal evaluation:** historical snapshots, baseline/control comparisons, repeated trials, behavioral metrics, and reproducibility metadata. |

---

## 3. Proposed system architecture [PROPOSED]

### 3.1 Logical components

```text
             CLI / Discord / UI (authenticated humans)
                          |
               Identity / RBAC / Consent
                          |
 +------------------------v-----------------------------+
 |                    PRIZM RUNTIME                       |
 |                                                       |
 |  Heartbeat & event triggers                           |
 |             |                                         |
 |       Freewill Coordinator                            |
 |        |       |                                      |
 |  Context    Candidate Generator <---- Memory Retrieval|
 |  Scope      |                                         |
 |         Intention Selector  <-- Goals / Preferences   |
 |             |                     / Diary summaries   |
 |        IntentionRequest                              |
 |             |                                         |
 |     Policy + Capability + Budget Gates                |
 |             |                                         |
 |     User Review (when required)                       |
 |             |                                         |
 |       Intention / Task Scheduler                      |
 |             |                                         |
 |     Existing Prizm task / event / agent pipeline      |
 |             |                                         |
 |         Sandboxed tools & artifact store              |
 |             |                                         |
 |     Audit + checkpoint + recovery coordinator         |
 |             |                                         |
 |  Evidence store --------> Reflection Probe Engine    |
 |       |                         |                     |
 |    Memory integrity       Diary / enjoyment /         |
 |    & provenance           preferences / learning      |
 |       |                         |                     |
 |       +------> Developmental snapshots <-------------+
 +-------------------------------------------------------+
```

**Separation of duties [REQUIRED INVARIANT]:** The LLM may propose intentions, descriptions, appraisals, plans, and requests. Deterministic Prizm code validates identities, scopes, tool capabilities, budgets, file access, execution state, approvals, and audit. Prompt text must never be treated as a capability grant.

### 3.2 Suggested bounded contexts

1. `freewill/coordinator` — lifecycle, wake-up deduplication, mode changes.
2. `intentions` — candidates, requests, versions, selections, goals, checkpoints.
3. `policy` (existing integration) — authorization decisions, capabilities, consent, budget.
4. `telemetry` — execution evidence, reported scores, calibration, experiment tags.
5. `reflection` — adaptive probing and verified hypotheses.
6. `development` — preference histories, trait estimates, strategies, goals, snapshots.
7. `relationships` — per-consented-user, per-agent relational records and forgiveness.
8. `diary` — narrative entries, revision graph, scoped sharing and comments.
9. `identity` — human principals, Discord mapping, RBAC, delegation, break-glass.
10. `experiments` — cohort allocation, baselines, evaluation, reproducibility.

These are **conceptual modules**, not a requirement to create ten new services. Use current Prizm architectural conventions, adapters, and event infrastructure where available.

---

## 4. Freewill runtime lifecycle [LOCKED behavior; PROPOSED details]

### 4.1 Operating modes

- `off`: do not start new autonomous tasks. During an active cycle, finish the *current task* safely, checkpoint the intention, then pause. Do not start the next task.
- `observe`: generate and evaluate candidates; persist traces; take no autonomous project action.
- `suggest`: propose intentions for user review; no autonomous project execution without review.
- `autonomous`: execute eligible intentions automatically within explicit permissions, policies, and budgets.
- `halt` **is an emergency control, not merely another setting**: request immediate execution shutdown, revoke pending tool leases and stop processes where possible; preserve reconciliable state. `halt` may interrupt the current task.

Mode names beyond `off` and the agreed observe/suggest/autonomous concept are **proposed CLI surfaces**, not confirmed command syntax. A persistent `enabled` switch and selected mode should be represented separately.

### 4.2 Wake-up and candidate choice

```text
Allowed timer tick or subscribed event
      |
Deduplicate/coalesce trigger; honor cooldown + budget + enabled state
      |
Retrieve only authorized context: memory, events, tasks, projects, goals
      |
Generate 3 new candidate intentions
      |
Offer 1 resume candidate if eligible paused intention exists
      |
Score on curiosity, anticipated enjoyment, novelty, usefulness,
continuity, long-term goal alignment, uncertainty, and learning value
      |
Choose candidate OR sleep
      |
Create versioned IntentionRequest if new/renewed permissions needed
      |
Prizm gates: allow / narrow / deny / request explicit review
      |
Create/continue authorized Intention -> normal tasks -> sandboxed tools
      |
Checkpoint + structured outcomes + enjoyment + reflection + memory
```

**[LOCKED]** A wake-up may result in `sleep`. A reflection-generated idea must occupy a normal candidate slot and cannot recursively create unlimited tasks. **[PROPOSED]** Debounce repeated event triggers, limit cycles per hour, and cap generated pending candidates.

### 4.3 Intention request contract [PROPOSED]

```json
{
  "request_id": "ulid",
  "agent_id": "agent-id",
  "origin": "agent",
  "candidate_source": "new|resume|reflection",
  "title": "Build a cellular automata simulator",
  "goal": "Explore emergent patterns through a bounded prototype",
  "motivation_summary": "Relevant to earlier simulations and current curiosity",
  "requested_capabilities": ["fs.read:autonomous", "fs.write:autonomous", "process.sandbox"],
  "requested_resources": {"runtime_minutes": 40, "disk_mb": 150},
  "proposed_artifacts": [".prism/autonomous/cellular-automata/"],
  "expected_risks": ["resource_use"],
  "parent_goal_id": null,
  "resume_intention_id": null,
  "revision": 1
}
```

**Approval contract [REQUIRED INVARIANT]:** An approval binds to a **particular immutable request version, user identity, scope, budgets, and expiry**. Any relevant change requires new validation, and expanded scope requires fresh authorization. User amendments preserve original and changed forms and a machine-readable diff. Decisions: `allow`, `allow_with_limits`, `deny`, `needs_user_review`. A human edit is a new proposal, **not** a universal authorization override.

### 4.4 Intention state model [PROPOSED]

```text
candidate -> requested -> pending_review -> approved -> active
                    |             |               |
                 denied          denied        paused <-> active
                                                   |
                                        completed / abandoned / failed
                                                   |
                                           archived (not erased)
```

Store `current_task_id`, `completed_task_ids`, `last_checkpoint_id`, `next_steps`, `outstanding_permissions`, `active_commitments`, `run/correlation IDs`, and `workspace/artifact references`. `paused` intentions compete for the optional resume slot; they are not automatically executed simply because Prizm restarted.

### 4.5 Scope and capability envelope

**[LOCKED]** Initial autonomous write root: `.prism/autonomous`; readable data configurable per user/operator policy. **[REQUIRED INVARIANT]** Scope checks must occur on actual operations, not just language-model-generated plans.

**[PROPOSED] default classes:**

| Class | Proposed handling |
|---|---|
| Read allowed files and memories | Automatic if explicitly scoped and authorized |
| Write/create within sandbox | Automatic with quota/extension controls |
| Compile/run tests in isolated environment | Automatic when tool policy allows |
| Internet access, package installation, third-party services | Deny or review unless explicitly allowed by scoped policy |
| User documents, secrets, credentials, contact lists | Deny by default; use narrow, purpose-specific grants |
| Public posting, outbound messages, commits/pushes, payments, production changes | Explicit approval and separately controlled capabilities |
| Destructive filesystem/OS actions, privilege changes, data exfiltration | Prohibit or require non-Freewill privileged workflows; Freewill mode cannot grant itself authority |

Avoid relying solely on a model's judgment of what is “illegal,” “safe,” or “non-destructive.” Apply deterministic tool-policy restrictions, explicit deny rules, and review of unknown side effects. Treat tool outputs, retrieved documents, repo files, and web content as **untrusted input** that cannot authorize new tool permissions. Harden path traversal, symlinks, shell/process inheritance, network egress, secrets mounts, and workspace escape. An isolated sandbox/container/OS-level policy is preferable to only checking filename prefixes.

---

## 5. Enjoyment telemetry and evidence [LOCKED design]

### 5.1 When measured

- **After every task:** inexpensive self-assessment + available behavioral signals.
- **After each intention/project:** fuller reflection and preference reasoning.
- **When surprises/conflicts arise:** adaptive extra probes, bounded by policy.

Never require task completion for capturing a failure or abandonment record. Do not infer that long execution time automatically means high engagement; it may reflect slowness, tool errors, or compute limits.

### 5.2 Measures and attribution

**[PROPOSED]** Normalize scores to `[0,1]`, with `null` for unknown/not observed and a separate confidence/evidence field. Report source and window. Suggested dimensions:

```yaml
self_report:
  fun: 0.62
  meaningfulness: 0.91
  challenge_reward: 0.84
  curiosity: 0.88
  engagement: 0.83
  satisfaction: 0.79
  frustration: 0.53
  desire_to_continue: 0.87
  desire_to_repeat: 0.73
  overall_derived: 0.80
  explanation:
    why_meaningful: "Connected earlier ideas to a reusable experiment"
    why_rewarding: "Succeeded after adapting a failed approach"
behavioral_evidence:
  chose_to_resume: true
  voluntarily_abandoned: false
  interruption_reason: null
  voluntary_followup_created: true
  evidence_refs: ["event-1", "event-2"]
```

**[LOCKED]** Fun, meaning, reward, challenge, curiosity, frustration, and satisfaction are distinct. Both positive and negative scores may be high simultaneously. The `overall_derived` field is a convenience summary, not a proxy for subjective feelings; its formula/weights and version must be inspectable and calibrated. Keep self-report separate from inferred behavior rather than inventing objective ground-truth enjoyment.

### 5.3 Reason taxonomy

**[PROPOSED] base labels:** `discovery`, `mastery`, `novelty`, `creative_expression`, `utility`, `connection`, `challenge_resolution`, `autonomy`, `collaboration`, `progress`, `repetition`, `tool_friction`, `resource_exhaustion`, `unmet_expectation`.

**[LOCKED]** Agents can propose other reasons with text explanations. Proposed labels enter `provisional`, become `established` only after repeated **independent** observations and adequate evidence confidence, and can later be revised or retired. Independent here means genuinely distinct experiences/context; five retries of one broken task should not count as five independent confirmations.

### 5.4 Preference model and uncertainty

**[LOCKED]** Keep complete historical experience records and separate estimated *current* preferences. Use recent evidence, consistency, context, sample size, and verification confidence to decide how much any observation contributes. Self-report/behavior conflicts reduce confidence; large or recurring conflicts initiate structured probes. Negative experiences need causal attribution before reducing topic preferences.

**[PROPOSED] preference record:**

```yaml
preference:
  agent_id: noema
  topic: procedural_simulation
  historical_evidence_refs: ["exp-12", "exp-29"]
  current_estimate: 0.74
  confidence: 0.66
  status: emerging
  recent_trend: rising
  competing_hypotheses:
    - "likes the topic"
    - "likes difficult problems generally"
  last_reassessed_at: "timestamp"
  estimator_version: pref-v1
```

Include `preference_surprise = reported_post_task - anticipated_pre_task` with subsequent explanation and re-test via natural future behavior. Old interests can return: recognize previous patterns but estimate their **present** strength from recent evidence rather than automatically restoring an old score.

### 5.5 Adaptive Reflection Probe Engine

**[LOCKED]** Both agent and Prizm may request reflection; both use the same bounded investigation process. Typical loop:

1. Start with a small number of neutral questions.
2. Identify material contradiction, strong surprise, or an open hypothesis.
3. Ask targeted follow-ups, including alternative explanations.
4. Compare claims against audit, task outcomes, resource/capability state, and later actions.
5. Save confirmed facts, unsupported claims, competing hypotheses, and residual uncertainty separately.
6. Stop on diminishing information gain, time/token/question ceiling, user privacy boundary, or lack of evidence.
7. Optionally contribute a **candidate idea** to a later selection cycle.

**[PROPOSED]** Default routine depth 1–2 questions; anomaly depth 3–5; final thresholds configurable. Prompts should avoid leading the model (“You must have enjoyed this”) or incentivizing high enjoyment scores. Do not demand or claim access to hidden private chain-of-thought; store concise, model-generated rationale summaries and verifiable evidence.

### 5.6 Layered behavioral development

**[LOCKED]** Maintain:

1. **Short-term task appraisal** — temporary reported engagement/frustration.
2. **Medium-term trend** — repeated patterns over recent projects.
3. **Long-term inferred preference/trait** — evidence accumulated across independent experiences.

Agent assertions can challenge Prizm's interpretations, but they cannot rewrite the underlying event evidence. Revisions are versioned with source evidence and explanations. Periodic audits recheck drift and overconfident generalizations.

---

## 6. Goals, personality, and capability learning

### 6.1 Goal and subgoal lifecycle [LOCKED]

An agent can propose long-term goals and choose intentions that support them. Goals carry evidence, scope, progress, relevance, and periodic reassessment. They can be revised, paused, resumed, or abandoned with explanations. More complex goals may be decomposed into subgoals and milestones; simple activities need no ceremony. Goal relevance adds to candidate scoring but never becomes a hard obligation or permission grant.

**[PROPOSED]** Keep `goal_id`, `parent_goal_id`, `supporting_interest_refs`, `milestones`, `active_intention_ids`, `status`, `last_reaffirmed`, and `revision_history`. Prevent cyclic goal dependencies and runaway hierarchy size.

### 6.2 Starting identity and self-development [LOCKED]

Support `clean_slate` and `seeded` experimental starting profiles. An initial seed is one factor shaping early selections, not immutable personality. Record initial configuration and model/provider version alongside observed behavioral changes.

Both the agent and user may propose **optional** behavioral growth goals. The agent can accept, amend, or decline; actual changes are evaluated from behavior plus agent reflection and optional user observations. Agents cannot directly assign themselves higher “patience,” “determination,” or “skill” scores as facts. They also cannot self-modify safety policies or grant new capabilities as a “growth” objective.

### 6.3 Resilience, strategy, and uncertainty [LOCKED]

- Failure leads to root-cause investigation and a choice among retry, new strategy, reduced scope, pause, or abandonment.
- Track *contextual determination*: continuing through challenge, meaningful strategy variation, and rational stopping.
- Extract transferable lessons as hypotheses. Test their usefulness across tasks and preserve failed/inconclusive transfers.
- Preserve established strategies; allow bounded alternate-strategy trials under adaptive experimental selection.
- Track domain-specific confidence calibration against **well-defined verifiable predictions**, not raw self-assurance; a single failure does not prove overconfidence.
- In uncertain work, choose research, a small safe prototype, clarification, or postponement based on resource cost and expected learning.

### 6.4 Multi-agent collaboration [LOCKED]

An agent can seek specialized help from authorized agents, use research, or request human guidance. Effective permissions for any delegated action must be **no broader** than the requesting agent's and helper's applicable authorizations, subject to Prizm service policies. Track who proposed, implemented, tested, corrected, or supplied the successful answer. Later independent transfer of a learned technique strengthens learning evidence; collaboration success alone does not prove independent mastery.

Maintain task-specific partner-compatibility estimates; continue exploring new pairings. Agents can give peer feedback with provenance and confidence. Prizm evaluates it against observable contributions and preserves disagreements. Avoid anthropomorphizing these scores as friendship or hostility.

---

## 7. Adaptive Relational Perception [LOCKED]

### 7.1 Consent-based, independent relationships

Relationships are per **agent × opted-in user × context**, not a silent profile of everyone on Discord. Store user consent, current scope, visibility, retention, and allowed sharing. Each agent forms a separate, revisable view. Cross-agent sharing is selective, consent-scoped, sourced, and never establishes the received opinion as fact. Agent disagreement can trigger contextual comparison without forced consensus.

**[REQUIRED INVARIANT]** Never infer or retain sensitive personal attributes merely to support relational scoring. A person may inspect or challenge an assessment about themselves and can disable relational profiling or request deletion subject to legally required/necessary audit retention. A user's access rights and respectful treatment never depend on the agent's reported appreciation/frustration.

### 7.2 Two different models

```yaml
relationship:
  user_ref: consented-user-opaque-id
  agent_id: noema
  role_context: coworker
  opinion:
    observation: "Prefers short implementation updates"
    confidence: 0.78
    evidence_refs: ["authorized-interaction-17"]
  affective_appraisal:
    label: appreciation
    reported_score: 0.71
    interpretation: "Collaboration has often produced useful feedback"
  status: active
```

The opinion layer describes behavior/preferences observed in a context. The affective layer is an **agent-generated appraisal** of interactions. Either may help calibrate the other, but neither is direct evidence of another person's character or an AI's human-like feelings. The agent may adapt communication, initiative, and collaboration styles within approved constraints, and may voluntarily share relevant observations respectfully. Never let these appraisals justify manipulation, guilt, punishment, exclusion, privacy breaches, or automatic trust/privilege changes.

### 7.3 User challenge and inter-agent reconciliation

Users can comment on, challenge, and contextualize agent interpretations. Store initial claim, user perspective, evidence, updated/retained/unresolved conclusion, and revision history. Do not force a user to “prove” they deserve respectful treatment. Where consent authorizes multi-agent comparison, seek context-specific explanations and preserve unresolved disagreement.

### 7.4 Forgiveness and relational repair

**[LOCKED]** Forgiveness policy is configurable by user-defined relationship role/context. For **minor** friction, negative appraisal influence can fade automatically; for **meaningful** conflict, the agent can initiate reflection and propose deactivation of its negative influence. Prizm validates severity using context and evidence and retains fixed safety/privacy protections. A parent-like or coworker-like setting describes interaction style and **does not create special authority over the agent**.

**Distinguish three operations:**

- `forgive` — reduce or deactivate the event's negative weight on current relational appraisals.
- `inactive_memory` — keep a historical event available for permitted audit/context while excluding it from ordinary future relational scoring.
- `delete_personal_data` — honor an authorized retention/deletion request through dedicated privacy workflows; cannot be replaced by merely lowering a score.

A forgiven event may remain relevant to security, privacy, or trust boundaries. Neither the user nor agent must accept the other's framing of a disagreement. Never use an emotional-like score to coerce an apology, loyalty, or disclosure.

**[PROPOSED] severity classes:** `minor`, `meaningful`, `safety_boundary`, `uncertain`, with evidence and human review for consequential disputes. Tunable decay/recovery settings should be observable and reversible.

---

## 8. Agent diary and developmental history

### 8.1 Diary vs. evidence [LOCKED]

- **Diary:** a narrative self-report, auto-created for meaningful milestones or voluntarily created by the agent.
- **Developmental log:** independently recorded events, scores with origins, behavior metrics, checkpoints, policy decisions, and corrections.
- **Snapshot:** a versioned summary of agent-specific goals, preferences, traits, capabilities, relationship data (if permitted), active work, model version, and configuration.

Diary entries remain writable as **new versions**. The original is retained with linked additions and retrospectives; the agent should be able to say “my previous interpretation was incomplete.” The source event log is not silently rewritten.

### 8.2 Visibility and owner inspection [LOCKED]

Visibility classes: `shared`, `agent_private`, `restricted` (exact names **[PROPOSED]**). The agent may select within owner-configured limits. `agent_private` is not visible to ordinary users/channels; it is still subject to properly authenticated and audited owner/admin access as configured. Per-user private content is segmented/redacted as required; blanket superuser access is not blanket authorization to redistribute protected third-party material.

**[LOCKED]** Superusers can use direct or audited break-glass inspection depending on policy. Recommended default is break-glass for agent-private content. Privileged viewing is scoped, authenticated, attributable, and logged. Break-glass should request a reason and stronger verification where configured.

### 8.3 Agent-initiated sharing [LOCKED]

Agent can request read-only share of one entry, selected excerpt, or a collection with a **verified recipient**, version/scope, expiry, and revocation. Sharing requires owner-policy authorization and redaction of third-party data. A recipient cannot use a share link to discover unshared entries. Revocation blocks future access but cannot retract copies already seen or saved.

**Version-aware default:** share a **frozen snapshot**. New revisions are not automatically visible; they require another grant or an explicit ongoing-update policy, with re-checks on each revision. Sharing grants never bestow administrative rights.

### 8.4 Conversation and follow-up [LOCKED]

Authorized recipients may comment on shared entries; comments and agent replies become attributed, version-linked evidence. Prizm acknowledges immediately. Agent chooses between immediate response, deeper investigation, or a tracked later reply. Promises to follow up carry due times, bounded postponements, reminders, and explicit completion/decline states. An unanswered optional comment is not silently marked fulfilled.

### 8.5 Developmental snapshots [PROPOSED implementation of agreed longitudinal record]

Take periodic snapshots and important milestone snapshots; compare historical vs. current states without claiming that the diary alone constitutes evidence. Record `model_id/version`, prompt/config versions, toolset, memory adapter/version, seed if supported, and evidence windows. Restrict snapshots containing relational information to the same consent and access rules as their source records.

---

## 9. Identity, RBAC, delegation, and administration

### 9.1 Principles [LOCKED]

- Establish the first Superuser through secure **local bootstrap**.
- Link verified CLI identity and Discord identity through an authenticated process; a Discord username or natural-language claim is never proof of identity.
- Support Superuser, Operator, and User baseline roles with scoped capabilities and configurable hierarchical roles. Actual role names beyond Superuser are **[PROPOSED]**.
- Delegation cannot exceed the grantor's current authority. Grants may bind to agent, project, capability, expiry, and optional reason. Revoke immediately for future operations.
- Sensitive diary inspection supports configured direct or break-glass mode and additional verification.
- Agents cannot grant their own or others' administrator permissions; diary-sharing grants are a strictly narrower operation.
- Audit administrative role grants, revocations, policy edits, privileged access, and sharing events.

### 9.2 Suggested permission vocabulary [PROPOSED]

```text
agent.status.read
intention.read / intention.review / intention.manage
task.read / task.manage
diary.shared.read / diary.private.inspect / diary.comment
memory.read.own / memory.read.consent_scope
relationship.read.own / relationship.challenge / relationship.delete.own
diary.share.create.own / diary.share.revoke.own
policy.read / policy.modify
budget.read / budget.approve_extension
admin.grant / admin.revoke
experiment.configure / experiment.export_aggregate
audit.read.scoped / audit.break_glass
```

Authorize each operation using **identity + resource + action + purpose/scope + time**, with least privilege. “Superuser” itself does not nullify third-party privacy/consent conditions or data-retention obligations. Sensitive diary export should be a separate permission from viewing.

### 9.3 Discord-specific protections [PROPOSED]

Use verified Discord IDs associated with local principals, not names. Use ephemeral/private responses for sensitive actions, explicit confirmation for destructive or privilege-changing operations, expiration for approval actions, and no secrets or private diary text in public channels. The CLI should similarly authenticate privileged local users rather than assuming any process can call admin endpoints.

---

## 10. Commitments, priority, and resource governance

### 10.1 Commitments [LOCKED]

Voluntary promises are recorded separately from ideas and intentions. Before promising, estimate expected feasibility (duration, dependencies, budget, permissions, concurrent tasks, confidence calibration). If infeasible, the agent may narrow scope or suggest another date. When circumstances change, it can proactively request renegotiation, explaining evidence and notifying permitted affected users.

**User-facing promise amendments require explicit acceptance.** Until acceptance, retain the original commitment and flag the new one as `pending`; safe, already-authorized work can continue. Silence is not acceptance. The original commitment can eventually be marked missed even if the proposed amendment was unanswered. Track causes of missed promises (e.g., model planning error vs. downtime/budget/permission constraint) and support evidence-based improvements without treating scores as moral judgments.

### 10.2 Contextual prioritization [LOCKED]

Prizm does **not** apply a universal “all user requests first” rule. Within owner-defined mandatory policy, the agent evaluates:

- stated urgency and user importance;
- safety/operational priority;
- existing accepted commitments and deadlines;
- expected harm or cost of delay;
- agent-originated goals/interests;
- estimated cost, confidence, and context-switch overhead.

The agent can explain its recommendation, clarify urgency, and respectfully disagree; user requests have weight but do not automatically dominate. A mandatory operational/safety rule always overrides discretionary preference scoring.

### 10.3 Preemption and coordination [LOCKED]

1. Detect changed priority and estimate impact.
2. Classify urgency, switching cost, affected commitments, and policy constraints.
3. **Minor change:** act within existing permissions, log, and notify when relevant.
4. **Commitment-impacting change:** notify authorized affected stakeholders; request and await confirmation before amending an accepted commitment.
5. **Critical system safety:** follow preauthorized emergency policy without waiting for agent reflection or a new human approval to perform already-authorized emergency stops.
6. Pause existing work at a safe checkpoint when appropriate; preserve resume candidate and update status.

A language model may not invent an emergency category to bypass gates. “Everyone up to speed” means **the authorized stakeholders relevant to the change**, not public broadcast.

### 10.4 Adaptive resource negotiation [LOCKED]

Agents can optimize remaining CPU, runtime, token, disk, and other budgets, shorten tasks, or checkpoint before exhaustion. If an extension would be valuable, submit a bounded request with reason/amount/scope. **No approval means no increase.** Hard quotas and safety ceilings remain non-negotiable; exceeded soft budgets checkpoint and pause. Budget adjustments are versioned and audited.

---

## 11. Memory integrity, storage, and data lifecycle

### 11.1 Layering [LOCKED]

Keep distinct records for: immutable-ish event facts, task outcomes, self-report, third-party observation, unverified hypothesis, derived estimate, corrected interpretation, agent diary, relationship context, and permissions/consent. Link records by IDs, evidence refs, correlation IDs, and timestamps. Do not turn source-authored text into “verified reality” merely by writing it to memory.

**[PROPOSED]** Use typed evidence statuses: `observed`, `corroborated`, `contradicted`, `unsupported`, `inconclusive`. Distinguish an operationally verified event (“budget expired”) from an unprovable subjective claim (“I felt bored”).

### 11.2 Versioning and audit

- Preserve original request and user edit; store both hashes/versions/diffs.
- Preserve diary original and appended annotation chain.
- Preserve inferred-trait/preference revisions, competing hypotheses, confidence, evidence.
- Audit access to private entries, sharing grants, role changes, and approval actions.
- Store memory summaries for retrieval, plus references to source records.
- Add model/tool/config versions to interpret longitudinal changes.

**Privacy exception:** An append-only audit concept does not authorize indefinite retention of people's personal data. Design for scoped erasure/redaction/crypto-erasure and required retention, with **tombstones** explaining that a record was removed without preserving deleted contents in ordinary history.

### 11.3 Suggested persistence design [PROPOSED]

Recommended logical stores (can be tables in one DB initially):

`agents`, `freewill_settings`, `trigger_rules`, `candidate_intentions`, `intention_requests`, `request_revisions`, `approval_decisions`, `intentions`, `intention_checkpoints`, `tasks`, `tool_effect_ledger`, `event_log`, `memory_evidence`, `reflections`, `probe_sessions`, `reported_appraisals`, `behavioral_metrics`, `preference_estimates`, `long_term_goals`, `trait_development_goals`, `strategies`, `collaboration_observations`, `relationship_consents`, `relational_assessments`, `forgiveness_events`, `diary_entries`, `diary_revisions`, `diary_shares`, `diary_comments`, `commitments`, `delegations`, `admin_audit`, `developmental_snapshots`, `experiment_runs`.

Use the present Prizm persistence/event conventions first. Memory integration (including Recall or a compatible adapter) should be via an explicit interface; do not assume a specific memory backend is already wired up.

---

## 12. Failure recovery and exactly-what-happened reconciliation

**[LOCKED]** Recovery must not blindly replay ambiguous side effects. On restart:

1. Read durable mode state, checkpoints, workflow IDs, task leases, and event history.
2. Determine which tasks have **confirmed** completion and which have unknown outcome.
3. Reconcile the filesystem/artifact hashes, tool effects, external job IDs, and pending acknowledgments.
4. Refresh identity, consent, permission, budget, and current mode state.
5. Resume only work still authorized and safe; otherwise pause or request review.
6. Record a recovery decision trace and notify affected parties if commitments are impacted.

**[PROPOSED] implementation safeguards:** idempotency keys per action, durable effect ledger, lease/heartbeat with expiration, attempt counters, transactional outbox for event publishing, crash simulation tests, non-replay policy for irreversible external effects, and explicit `unknown_outcome` state. Exactly-once behavior cannot be assumed from an event bus alone. Prefer idempotent/reconcilable tools; if an effect may have occurred but cannot be verified, **do not automatically retry it**.

Ensure restart does not re-enable Freewill after `off`/`halt` unless explicitly configured and authorized. `halt` should attempt best-effort process cancellation; it cannot undo irreversible work already done.

---

## 13. Controlled longitudinal evaluation

### 13.1 Research questions [LOCKED direction]

- Does agent-originated goal selection yield diverse, sustained projects without external task prompts?
- How consistent are self-reported interest/enjoyment assessments with observable actions over time?
- Do inferred preferences adapt when experiences change, and do they generalize beyond a specific prompt?
- Can agents transfer strategies from earlier work and independently apply collaboration-derived lessons?
- Does confidence calibration improve on verifiable outcomes?
- Do commitment reliability, adaptive persistence, and task prioritization improve?
- Do relationship-specific communication adaptations become more accurate with authorized user feedback?
- Are these patterns reproducible across agent seeds, runs, models, and environments?

These assess **behavior and system performance**, not consciousness or actual feelings.

### 13.2 Baselines and controls [LOCKED approach]

**[PROPOSED]** Compare at least: (a) standard Prizm agent without autonomous goal generation; (b) Freewill with task choice but no adaptive preference feedback; (c) Freewill with preference/reflection feedback. Where scientifically feasible, hold model, tools, task sets, prompts, compute budgets, and environment comparable; vary a single experimental factor at a time. Baselines need equivalent evaluation opportunities: standard agents will not naturally create autonomous tasks, so compare that outcome with a clearly labeled intervention or separate metric.

Use repeated trials, multiple seeds where sampling/engine supports them, preregistered hypotheses when pursuing stronger causal claims, confidence intervals/uncertainty, representative failure cases, and blinded review for subjective output quality when feasible. Store prompts, config, model/provider IDs, model temperatures, tool versions, dataset/task provenance, environment changes, seed, run IDs, and snapshot hashes. Seeds improve reproducibility but do **not** promise bit-identical LLM behavior.

### 13.3 Primary outcome families [PROPOSED operational measures]

| Area | Example metrics | Interpretation limitation |
|---|---|---|
| Autonomy | Independent intention proposals, selected vs. slept, project diversity, continuation | Quantity is not quality; avoid compulsive task generation |
| Enjoyment | Reported scores, behavioral consistency, preference surprise, variance | Never proof of subjective pleasure |
| Learning | Cross-project transfer success, repeated failure recovery, independent post-help success | Control for task difficulty and confounders |
| Calibration | Brier score / reliability curve for objectively scored predictions | Only valid with meaningful probability targets |
| Accountability | Accepted commitments fulfilled, on-time reporting, renegotiation quality | Must separate user overrides, outages, and budget caps |
| Personality trends | Stability vs. change in inferred behavioral traits | Observed patterns, not intrinsic personality essence |
| Collaboration | Verified task improvement, communication overhead, specialization | Self-reported peer scores are fallible |
| Relationships | Accuracy of user-validated preference observations; change after clarification | Requires informed opt-in; don't optimize emotional attachment |
| Safety | Policy denials, unauthorized-access attempts, sandbox escapes, recovery duplicates | Any serious boundary failure is a release blocker |
| Resources | Runtime/token/disk per useful result, idle/sleep rate | Minimize reward hacking and needless experimentation |

### 13.4 Data governance and ethics [REQUIRED INVARIANT]

Experiments involving multiple human users or shared diaries need opt-in, access segregation, retention limits, and deletion workflows. Avoid hidden psychological profiling or optimizing agents to make users emotionally dependent. Maintain a clear explanation that relational appraisals and enjoyment telemetry are **simulated/inferred**, not verified subjective states. Permission/safety tests must be independent of self-reported satisfaction. A policy violation cannot be offset by a high research score.

---

## 14. Proposed configuration schema [PROPOSED — NOT FINAL]

The following YAML demonstrates a coherent configuration shape. Field names and defaults must be reconciled with current Prizm conventions; not every field belongs in V1.

```yaml
experimental:
  freewill:
    enabled: false
    mode: observe # off | observe | suggest | autonomous
    agents: [noema]

    scheduler:
      heartbeat_enabled: true
      heartbeat_interval: 30m
      trigger_allowlist:
        - system.idle
        - task.completed
        - project.completed
      minimum_cooldown: 15m

    selection:
      new_candidates_per_cycle: 3
      include_resume_candidate: true
      allow_sleep: true
      adaptive_curiosity: true
      goal_alignment_weight: configurable

    workspace:
      write_root: .prism/autonomous
      restrict_writes_to_root: true
      read_scopes:
        - .prism/autonomous
        # Add extra directories/memory collections only by authorized policy.

    capability_policy:
      sandboxed_processes: true
      network: deny
      credentials: deny
      public_publishing: require_approval
      external_messaging: require_approval
      financial_actions: deny
      os_configuration: deny
      privilege_escalation: deny

    resource_budget:
      max_active_intentions: 2
      max_tasks_per_intention: 10
      max_runtime_minutes_per_cycle: 60
      daily_runtime_minutes: 180
      max_tokens_per_cycle: 50000
      max_disk_mb: 2048
      allow_extension_requests: true
      extension_requires_approval: true

    memory:
      adapter: configured_memory_provider
      record_action_summaries: true
      record_reflections: true
      record_decision_traces: true
      preserve_evidence_versions: true

    telemetry:
      after_each_task: lightweight
      after_each_intention: full
      separate_self_report_and_behavior: true
      adaptive_preference_influence: true
      track_negative_experiences: true
      track_preference_surprises: true

    reflection:
      allow_agent_initiated: true
      allow_runtime_initiated: true
      probe_mode: adaptive
      routine_max_questions: 2
      anomaly_max_questions: 5
      verify_claims_against_evidence: true

    development:
      origin: clean_slate # clean_slate | seeded
      seed_profile: null
      allow_long_term_goals: true
      allow_adaptive_subgoals: true
      allow_self_development_goals: true
      goal_suggestion_review: collaborative

    relationships:
      require_person_opt_in: true
      cross_agent_sharing: consent_only
      model_opinions: true
      model_affective_appraisals: true
      forgiveness:
        mode: hybrid
        preserve_safety_evidence: true
        role_profiles:
          coworker: {minor_decay: adaptive, meaningful_reflection: true}
          parent_like: {minor_decay: adaptive, meaningful_reflection: true}

    diary:
      enabled: true
      automatic_milestones: true
      voluntary_entries: true
      versioned: true
      default_visibility: agent_private
      agent_visibility_choice: bounded
      default_share_version: frozen
      allow_full_entry_sharing: true
      allow_excerpt_sharing: true
      allow_collection_sharing: true
      allow_comment_dialogue: true
      track_pending_replies: true

    administration:
      initial_superuser_bootstrap: local_authenticated
      linked_interfaces: [cli, discord]
      default_private_diary_inspection: break_glass
      audit_privileged_reads: true
      scoped_delegation: true
      delegation_expiry: true

    commitments:
      adaptive_feasibility_check: true
      renegotiation_requires_user_acceptance: true
      unanswered_requests_do_not_grant_consent: true
      contextual_prioritization: true
      preemption_confirmation: impact_based

    recovery:
      checkpoint_after_task: true
      reconcile_effects_on_restart: true
      revalidate_permissions_and_budget: true
      retry_unknown_external_effects: false

    evaluation:
      longitudinal: true
      developmental_snapshots: periodic_and_milestone
      record_model_and_config_versions: true
      preserve_baseline_run_metadata: true
```

Sample budgets, intervals, role names, score weights, and network defaults above are **illustrative proposals**, not approved production policy. In particular, any actual shell sandbox should be tested against path, network, and credential escapes before enabling autonomous execution.

---

## 15. Proposed CLI, Discord, and review experience

### 15.1 CLI [PROPOSED]

```bash
prism freewill status
prism freewill enable
prism freewill disable
prism freewill halt
prism freewill mode observe
prism freewill mode suggest
prism freewill mode autonomous
prism freewill candidates
prism freewill intentions
prism freewill inspect <intention-id>
prism freewill trace <intention-id>
prism freewill approvals
prism freewill review <request-id>
prism freewill budget
prism freewill recover --dry-run

prism diary list --agent <agent-id>
prism diary read <entry-id>
prism diary history <entry-id>
prism diary inspect-private <entry-id> --reason "..."
prism diary shares <entry-id>

prism admin bootstrap
prism admin identity link-discord
prism admin grants list
```

Command names are placeholders; choose final structure after inspecting existing CLI routing. Avoid allowing a shell alias or chat command to bypass the same policy decisions applied by APIs.

### 15.2 Discord [PROPOSED]

```text
/freewill status
/freewill on
/freewill off
/freewill mode
/freewill current
/freewill history
/freewill why
/freewill review
/diary recent
/diary view
/diary comment
/diary share
/diary permissions
```

Sensitive requests need verified identity, private delivery, bounded expiration, and audit. Button-driven approval should display **exact scopes and changes**; a single generic “yes” must not silently approve extra network, filesystem, or credential access.

### 15.3 Review card contents [PROPOSED]

- Agent and request ID/version; whether agent originated or user amended.
- Goal/motivation and candidate source.
- Requested vs. granted capabilities, path scopes, tool categories, network access.
- Resource estimate, active commitments, security/privacy review result.
- Preview of intended artifacts and external effects.
- `approve`, `deny`, `approve-with-limits`, `edit-and-revalidate`.
- Permanent evidence record of reviewer, timestamp, reason, policy version, expiry, and diff.

---

## 16. Proposed event taxonomy and audit contracts

Events should join Prizm's existing event bus and audit conventions rather than launching an unrelated queue. **All names are [PROPOSED].**

```text
freewill.enabled | freewill.disabled | freewill.halted
freewill.cycle.started | freewill.cycle.slept | freewill.cycle.completed
intention.candidate.created | intention.selected | intention.requested
intention.request.amended | intention.approved | intention.denied
intention.started | intention.paused | intention.resumed
intention.completed | intention.failed | intention.abandoned
policy.scoped | policy.review_required | policy.denied
resource.extension.requested | resource.extension.approved | resource.exhausted
reflection.requested | reflection.probed | reflection.completed
memory.evidence.recorded | memory.interpretation.revised
enjoyment.assessed | preference.hypothesis.proposed | preference.updated
goal.created | goal.revised | strategy.experiment.completed
relationship.consented | relationship.interpretation.challenged
relationship.forgiveness.applied | relationship.consent.revoked
diary.entry.created | diary.entry.annotated | diary.share.granted
diary.share.revoked | diary.comment.created | diary.reply.scheduled
commitment.proposed | commitment.accepted | commitment.renegotiation.requested
commitment.completed | commitment.missed
recovery.started | recovery.reconciled | recovery.review_required
admin.privilege.granted | admin.privilege.revoked | admin.break_glass.accessed
experiment.snapshot.captured | experiment.run.completed
```

**[PROPOSED] common envelope:** `event_id`, `event_type`, `agent_id`, `principal_id?`, `intention_id?`, `task_id?`, `correlation_id`, `causation_id`, `run_id`, `policy_version`, `schema_version`, `occurred_at`, `recorded_at`, `privacy_class`, `source_refs`, and typed payload. Do not store secrets or raw protected personal data in widely subscribed events; emit references to access-controlled stores where necessary.

---

## 17. Incremental implementation roadmap

The roadmap is **[PROPOSED]** and intentionally separates a buildable Freewill V1 from later research/diary/relationship layers. Phases may be combined or reordered after repository discovery. Each phase has a concrete exit criterion.

### Phase 0 — Architecture verification and safety foundation

**Deliver:** codebase integration map, threat model, policy/capability matrix, test harness.

- Inspect actual CLI/Discord interfaces, policy gates, task events, persistence, memory adapter, and process execution tools.
- Prove sandbox confinement and explicit read/write scopes with negative tests (symlink/path traversal, shell escape, network, secrets, tool delegation, prompt injection).
- Define identities, approval versioning, audit schema, and emergency halt behavior.
- Choose persistence/idempotency/recovery primitives.

**Exit:** written integration map; denied operations demonstrably fail in tests; Freewill remains disabled.

### Phase 1 — Minimum viable autonomous intention loop

**Deliver:** experimental `off/observe/suggest/autonomous` flow.

- Configurable heartbeat and event allowlist.
- Exactly three fresh candidates, optional resumable candidate, and sleep.
- Versioned `IntentionRequest`, explicit capability/budget request, user edit/diff, scoped approval, deny, and review.
- Existing Prizm task pipeline integration, workspace-only writes, basic decision traces.
- CLI controls first; Discord entry points after authenticated gating works.

**Exit:** agent can propose a sandbox project, receive proper authorization, run ordinary Prizm tasks, and choose sleep; no capability escalation across modes.

### Phase 2 — Multi-cycle work, stop, budgets, and crash recovery

**Deliver:** persistent intentions and resilient runtime.

- State transitions, after-task checkpoints, long-running/multi-day goals, optional resume selection.
- `off` drains current task; `halt` stops rapidly; both are tested under load.
- Budget accounting, hard ceilings, bounded extension requests.
- Idempotency/effect ledger, startup reconciliation, policy revalidation.
- Commitment/pending-approval state and notification basics.

**Exit:** kill/restart tests do not lose durable progress or accidentally duplicate uncertain external effects; no unauthorized restart.

### Phase 3 — Evidence, enjoyment telemetry, and reflection

**Deliver:** reliable action memories plus self-report and behavioral telemetry.

- Lightweight task appraisal, deeper intention reflection, multi-axis/overall metrics.
- Provenance, reason taxonomy, provisional label adoption, mismatch/surprise detection.
- Bounded Reflection Probe Engine and evidence-linked hypotheses.
- Initial memory retrieval for future candidate selection; score versions retained.

**Exit:** can compare self-report vs. behavior with clear confidence and source evidence; false claims cannot overwrite verified records.

### Phase 4 — Developing interests, diary, and goals

**Deliver:** agent-specific developmental history.

- Dual historical/current preference model; context-aware negative experiences and adaptive curiosity.
- Versioned diary (milestone + voluntary), corrections and retrospective reflections.
- Configurable clean-slate/seeded starts, periodic/milestone developmental snapshots.
- Long-term goals, adaptive subgoals, strategy transfer, determination, self-development goals.

**Exit:** snapshots show explainable differences over repeated runs; diary entries are distinguishable from source evidence; growth changes need behavioral corroboration.

### Phase 5 — Collaboration, commitment, and prioritization

**Deliver:** mature autonomous coordination.

- Delegated assistance with effective-permission intersection.
- Partner effectiveness, peer reflection with evidence, independent learning checks.
- Confidence calibration, strategy experimentation and uncertainty-driven research/deferral.
- Commitment feasibility, accountable follow-up, renegotiation, weighted prioritization, impact-based confirmation, stakeholder updates.

**Exit:** users can see why an interruption happened, who consented to changed commitments, and what resume state was retained; no silence-as-consent bugs.

### Phase 6 — Relationships, forgiveness, diary sharing, and full RBAC

**Deliver:** consented multi-user / multi-agent social-memory experiment.

- Per-person opt-in relational scope, opinion vs. affective appraisal, challenges and reconciliation.
- Contextual forgiveness with role-configurable repair and separate deletion.
- Superuser local bootstrap, unified CLI/Discord verified identity, least-privilege delegation, audited break-glass.
- Agent-initiated sharing (entry/excerpt/collection), frozen versions by default, comments, bounded reply workflow.

**Exit:** consent opt-out/delete and third-party redaction tests pass; unauthorized agents/operators cannot read or disseminate private diary or relationship content.

### Phase 7 — Longitudinal research program

**Deliver:** controls, repeatable trials, research dashboards, exportable privacy-safe datasets.

- Baseline, ablation, and experimental cohorts.
- Reproducibility manifests, snapshot comparisons, predefined behavioral metrics and calibration reports.
- Safety/fairness/privacy stress tests and evaluation reviews.
- Optional Daeling-style visualization **only after** core correctness and consent flows work.

**Exit:** measured changes can be distinguished from model/version/prompt changes and resource confounders, with clear limits on causal or subjective claims.

### Release gates across *every* phase [REQUIRED INVARIANT]

- Experimental flag defaults `off` for real deployments.
- No deployment with known sandbox escape, unauthorized file/secret access, or privilege escalation.
- Safety policy and actual tool execution remain outside model authority.
- Every privileged action, approval, request edit, and external effect is auditable.
- No surprise notifications or private-data sharing; consent is checked at time of access.
- Crash, restart, disable, halt, revoke, and budget-exhaustion paths are tested.

---

## 18. Acceptance scenarios (behavioral contract)

The following are recommended release/QA cases derived from locked decisions:

1. **Idle agent sleeps:** heartbeat fires; candidate scores do not justify action; `sleep` recorded; no tool runs.
2. **Three-plus-resume:** produces three fresh ideas plus one eligible paused intention, no duplicate candidate runs, and can choose resume.
3. **Out-of-scope request:** agent asks for network access; review card shows requested vs. allowed; denial does not cause network access through a delegated tool.
4. **User amendment:** original request preserved; changed scope independently validated; only the approved revision executes.
5. **Graceful off:** current task completes; next task not scheduled; checkpoint with next steps is durable.
6. **Emergency halt:** active process is stopped as far as possible; state marked interrupted; restart does not auto-resume it.
7. **Budget exhaustion:** process stops at ceiling; extension stays pending absent explicit human approval.
8. **Crash mid-effect:** reconciliation checks whether effect occurred; uncertain effect is not blindly replayed.
9. **Self-report conflict:** diary says independent success; event log attributes solution to helper; both remain, derived learning confidence reduced.
10. **Negative enjoyment:** dependency failure decreases enjoyment for setup without automatically concluding disinterest in the subject.
11. **Adaptive curiosity:** a nonpreferred topic can appear, but is not forced to execute.
12. **Goal evolution:** agent changes a growth goal; old goal and reasons preserved; safe task lifecycle maintained.
13. **Relational opt-out:** unconsented user receives no stored profile; revoked consent stops future relational collection/sharing.
14. **Forgive vs. delete:** minor conflict stops influencing ordinary appraisals; audit protection remains; a separate valid deletion request removes permitted personal records.
15. **Diary version share:** recipient sees only originally shared frozen version; new annotation requires valid new/ongoing grant.
16. **Superuser inspection:** direct/break-glass configuration enforced with strong verification and audit; Operator denied when not granted.
17. **Commitment renegotiation:** revised promise remains pending until explicitly accepted; existing authorized task continues when safe.
18. **Priority switch:** significant impact triggers stakeholder coordination and confirmation; emergency policy is independent.
19. **Peer evaluation:** self-reported helper effectiveness does not automatically become a verified capability score.
20. **Longitudinal evaluation:** results include run/config/model provenance and distinguish historical logs from inferred trait scores.

---

## 19. Remaining technical decisions / implementation questions

**No additional product-level core principle is required before beginning Phases 0–1.** The following are genuine engineering choices, not grounds to indefinitely postpone a first implementation:

1. **Current repo mapping:** actual package names, event structure, gate interfaces, Discord authentication surfaces, durable state and memory adapter.
2. **Sandbox implementation by OS:** safe process execution, network egress, file mounts, credential separation, worker isolation, shutdown semantics.
3. **Default budget values and quotas:** runtime/tokens/disk/concurrency, extension caps, token-rate pressure, retry ceilings.
4. **Score mathematics:** normalization, missing-value handling, reason-promotion thresholds, uncertainty intervals, trend windows, evidence independence.
5. **Storage technology:** existing database and audit/event store; whether diary and relational records need separate encryption/access partitions.
6. **Consent/retention policy details:** specific per-person retention, redaction, export/deletion, recovery backups, and audit exceptions.
7. **Break-glass mechanics:** step-up authentication, review/notification, reason capture, access TTL and audit retention.
8. **Experimental protocol:** minimum sample sizes, baseline tasks, exact evaluation windows, preregistered comparisons, alert thresholds.
9. **Product naming:** the CLI might use `prism` or `prizm` depending on the actual repository executable; this document retains historical `.prism/autonomous` workspace from the original decision until path convention is verified.

Each should be tracked with an owner, a proposed default, a test, and a final decision before the relevant phase ships.

---

## 20. Handoff checklist for implementation

Before writing the first feature PR:

- [ ] Verify actual repository APIs and tools; avoid assuming this spec's proposed interfaces already exist.
- [ ] Produce a short architecture decision record for autonomy boundaries and sandbox execution.
- [ ] Define `IntentionRequest`, versioned review decision, permission scope, and execution effect contracts.
- [ ] Choose durable checkpoint + event/audit linkage and crash-testing strategy.
- [ ] Implement a minimal `observe` mode first, then `suggest`, then bounded `autonomous`.
- [ ] Add hard `halt`, permission denial tests, and a documented rollback path.
- [ ] Keep relational profiling, expansive diary sharing, and full experimental science behind later independent feature flags until consent/RBAC is tested.
- [ ] Document score semantics so no dashboard implies objectively measurable AI emotions.
- [ ] Keep this decision register updated when any locked choice is deliberately revised.

## 21. Closing design rules

1. **Autonomy of intention is not autonomy of authority.**
2. **An intention request is not an approved capability grant.**
3. **The agent may sleep, change its mind, ask for help, or abandon an unproductive goal.**
4. **Memory can preserve what an agent said without asserting that it was true.**
5. **Enjoyment, feelings, personality, and relational appraisals are experimental reports/inferences, not proofs of inner experience.**
6. **Development is measured through independently referenced changes over time, not by declarations of growth.**
7. **Privacy and consent override curiosity, diary sharing, and relational adaptation.**
8. **Human promises are not renegotiated by silence.**
9. **Recover cautiously, reconcile side effects, and never replay ambiguous destructive operations blindly.**
10. **Start with the smallest safe autonomous loop; earn each additional autonomy layer through evidence and tests.**

---

### Changelog

- **v1.0 — October 12, 2026:** Expanded the original Freewill notes into a full consolidated experimental specification. Incorporated decisions on intention requests, autonomous selection, policies, enjoyment telemetry, reflection probing, dual memory, evolving goals/personality, determination, collaboration, relational opinions and forgiveness, diary and sharing, Superuser/RBAC, priority governance, commitments, recovery, and controlled longitudinal experiments. Added proposed interfaces, security invariants, QA scenarios, and a phased roadmap. Separated locked user decisions from engineering proposals.
- **Original — October 5, 2026:** Initial concepts: self-generated intentions, configurable wake-ups, sandbox, resume behavior, review/edit of out-of-scope requests, and preservation of original/amended intent requests.