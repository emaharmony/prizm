# Security Policy

## Project Status

Prizm is source-available and experimental. It is not production-ready.

Do not use Prizm for unattended high-risk automation, live trading, deployment, destructive operations, or sensitive production workflows without independent review and hardening.

Security support is currently best-effort for the `main` branch.

## Reporting Security Issues

Please report security issues privately to the repository owner.

Do not open public issues for vulnerabilities involving:

- secret leakage
- command execution
- path traversal
- approval bypass
- unsafe mutation behavior
- trading or financial execution risks

## Security Principles

Prizm is designed around these principles:

- The framework controls lifecycle and policy.
- Models may request actions but must not directly execute unsafe operations.
- Tool execution is allowlisted and auditable.
- File mutations require approval gates.
- Validation commands are allowlisted.
- Generated run artifacts may contain sensitive data and should not be committed.
- Secrets must never be committed.

## Known Risk Areas

- LLM prompt/output artifacts
- tool execution
- file mutation workflows
- approval gates
- validation commands
- future trading/domain adapters
- local configuration and `.env` files