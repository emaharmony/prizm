# Contributing to Prizm

Thanks for your interest in Prizm.

Prizm is an experimental event-native AI agent framework. Contributions should preserve the core design principle:

> The framework controls the lifecycle; models generate outputs inside that lifecycle.

## Before Contributing

Please read:

- `README.md`
- `SECURITY.md`

## Development Setup

```bash
gofmt -w ./cmd ./internal ./remembrance/go
go vet ./...
go build ./...
go test ./... -count=1
```

Run `go test -race ./... -count=1` on a host with CGO and a C compiler, plus
the Python and documentation jobs described in [CI](docs/operations/ci.md).

If working on Remembrance, follow the Python setup instructions in the Remembrance docs.

## Licensing and Contributions

Prizm is currently source-available under an all-rights-reserved license.

By submitting a pull request or contribution, you agree that your contribution may be incorporated into Prizm under the repository's current license unless a separate written agreement is made.

If you want to use Prizm or contribute under different terms, contact Emmanuel Vinas through GitHub first.

## Contribution Guidelines

- Keep changes scoped.
- Do not introduce arbitrary shell execution or approval-gate bypasses
  without an explicit, reviewed, narrowly-scoped design (the existing
  precedent is Free Mode — a single-owner, opt-in, off-by-default exception;
  see [docs/concepts/SAFETY.md](docs/concepts/SAFETY.md#free-mode-owner-authorized-mutation-mode)).
  Do not widen that exception's scope without the same level of review.
- Do not allow LLMs to approve their own mutations.
- Do not commit generated `runs/` artifacts.
- Do not commit secrets or `.env` files.
- Add tests for new event types, tools, approvals, gates, or validation behavior.
- Update README/docs when behavior changes.

## Pull Requests

A good PR should include:

- clear summary
- why the change exists
- tests pass
- known limitations

## Project Direction

Prizm is designed to remain domain-agnostic. Domain-specific systems (trading, Roblox, developer automation, deployment workflows) should integrate through adapters/gates rather than changing Prizm core into a domain-specific framework.
