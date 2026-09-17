# AGENTS.md

## Token-Saving Context Gathering Rule

Before reading large files, scanning broad folders, or loading large code blocks
into context, use the locally installed Ollama model as a context scout.

Goal:

- Save Codex tokens.
- Avoid dumping full files into the main context unless necessary.
- Build a compressed, task-specific context pack first.

Default local model:

- `qwen3.5:9b`

Process:

1. Restate the user's task in one sentence.
2. Identify likely relevant files using cheap local commands first:
   - `git ls-files`
   - `rg`
   - `fd` / `find`
   - package/project manifests
   - route/module names
3. Do not open entire large files immediately.
4. Send only filenames, symbols, small snippets, errors, and search hits to
   Ollama.
5. Ask Ollama to produce a compact context summary with:
   - relevant files
   - likely entry points
   - important functions/classes
   - architecture notes
   - risks/unknowns
   - recommended files to inspect next
6. Save the result to `.codex/context/last-context.md`.
7. Read that context pack before doing deeper analysis.
8. Only load full source files into Codex context when the context pack says
   they are directly relevant.
9. If the Ollama context scout fails, continue with normal search, but keep
   context minimal.

Never send secrets to Ollama:

- `.env` files
- private keys
- tokens
- passwords
- production credentials
- customer/user data
- SSH keys
- cloud credentials

Preferred command shape:

```bash
mkdir -p .codex/context
git ls-files | rg "REPLACE_WITH_TASK_KEYWORDS" > .codex/context/candidate-files.txt
ollama run qwen3.5:9b < .codex/context/context-scout-prompt.txt > .codex/context/last-context.md
```

## Package Boundary Rules

Before generating or changing Prizm code, read and follow
[Package Boundaries](docs/architecture/PACKAGE_BOUNDARIES.md).

- Packages MUST represent bounded domains, not helper categories.
- Code MUST be placed with the domain that owns its invariants, not with the
  most convenient caller.
- A package MUST NOT be introduced only because a file is large or code appears
  reusable.
- Utility-style packages require explicit architectural justification and a
  domain-specific name.
- Every architectural PR that introduces a new package MUST include the
  package-justification template from `PACKAGE_BOUNDARIES.md`.
- If ownership is ambiguous, resolve the boundary before generating code.
