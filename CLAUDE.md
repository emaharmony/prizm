# CLAUDE.md

## Package Boundaries

Claude MUST read and follow
[Package Boundaries](docs/architecture/PACKAGE_BOUNDARIES.md) before generating
code that changes Prizm architecture or package placement.

In particular:

- Generate code within bounded domains, not helper or convenience categories.
- Place behavior with the package that owns its invariants.
- Keep domain behavior separate from composition, transport, persistence,
  presentation, and external integrations.
- Do not create a package because a file is large or code merely appears
  reusable.
- Do not introduce utility packages without a specific bounded-domain
  justification and a domain-specific name.
- Preserve the runtime-owned authority path across policy, approval,
  validation, execution, persistence, and audit.
- For every architectural PR that introduces a package, include the
  package-justification template required by `PACKAGE_BOUNDARIES.md`.

If a requested change conflicts with these rules, identify the conflict before
generating code.
