# Prizm Documentation Standard

The conventions for docs in this repository. It exists so every doc reads as one
consistent set. Follow it when adding or editing documentation.

> This standard was derived from the curated guides added during the public-preview
> docs pass (e.g. [Getting Started](getting-started/GETTING_STARTED.md),
> [Architecture](architecture/ARCHITECTURE.md), [Capability Status](reference/CAPABILITY_STATUS.md)).

## Where a Doc Goes

- **Curated docs** — guides, reference, and architecture that a user or contributor
  reads today — live in a topic subdirectory of `docs/`: `getting-started/`,
  `operations/`, `architecture/`, `concepts/`, `reference/`, `dashboard/`, or
  `quality/`. Pick the subdirectory that matches the doc's audience/purpose
  (see the [Documentation Hub](README.md) for what lives where); don't add
  new curated docs directly at the top level of `docs/`.
- **Design notes** — per-version `V<n>-…-DESIGN.md` and planning reviews — live in
  [`docs/history/milestones/`](history/README.md). They record how something was built; they are
  archival, not current guides. Add new design notes there.

## Filenames

- Curated docs use `UPPER_SNAKE_CASE.md` (e.g. `GETTING_STARTED.md`,
  `YAML_REFERENCE.md`).
- Design notes use `V<n>-TOPIC-DESIGN.md` (e.g. `V59-AGENT-INVOCATION-API-DESIGN.md`).
- A few legacy top-level docs predate this rule and keep their names
  (`PRIZM-VISION.md`, `EVENT-MANUAL.md`, `natural-gates-workflow.md`); don't rename
  them casually — it breaks inbound links.

## Structure

1. **Title** — a single `#` H1, product-prefixed where natural: `# Prizm <Thing>`
   or `# <Action> with Prizm`.
2. **Purpose** — 1–3 sentences immediately under the title saying what the doc is
   and who it's for.
3. **Preview/license note** — where relevant, a `>` blockquote linking
   [`../LICENSE`](../LICENSE) and [Capability Status](reference/CAPABILITY_STATUS.md), noting
   Prizm is source-available and preview-stage.
4. **Body** — `##` sections. Use tables for structured facts (requirements,
   options, capabilities). Use fenced code blocks with a language hint, and give
   both `bash` and PowerShell variants where the commands differ on Windows.
5. **See Also** — a closing `## See Also` section with relative links to related
   docs.

## Status Vocabulary

When describing maturity, use the shared terms from
[Capability Status](reference/CAPABILITY_STATUS.md):

- **Preview/Stable** — implemented and exercised by demos/tests; interface fairly
  settled.
- **Preview** — implemented and usable; interface may still change.
- **Experimental** — implemented but advanced/optional; expect rough edges.

## Links

- Use **relative** links between docs (`[Configuration](operations/CONFIGURATION.md)`,
  `[V58](history/milestones/V58-FULL-AUTONOMY-DESIGN.md)`).
- When you move or rename a doc, update inbound links across `docs/`, the root
  `README.md`, and code comments. A quick check: search the repo for the old path.

## Tone

Concise, present tense, practical. State what is true and how to do the thing. Avoid
marketing language and unearned superlatives — "It is a guide, not a guarantee."

## See Also

- [Documentation Hub](README.md)
- [Capability Status](reference/CAPABILITY_STATUS.md)
- [Contributing](../CONTRIBUTING.md)
