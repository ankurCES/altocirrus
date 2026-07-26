# Contributing to AltoCirrus

Thank you for your interest in contributing to AltoCirrus. This guide covers everything you need to get started.

## Prerequisites

- **Go 1.22+**
- **Docker** (optional, for integration tests and local service emulation)

## Getting Started

### Clone and build

```bash
git clone <repo-url>
cd altocirrus
go build -o altocirrus ./cmd/altocirrus
```

### Run locally

```bash
./altocirrus
# or
go run ./cmd/altocirrus
```

### Run tests

```bash
go test ./...
```

## Adding a New Service

1. Create a package under `internal/azure/` or `internal/gcp/`.
2. Define a `RegisterRoutes(mux, store, cfg)` function in that package.
3. Import and call `RegisterRoutes` in `cmd/altocirrus/main.go`.
4. Add integration tests for the new service.
5. Update the services table in `README.md`.

## Commit Conventions

This project follows [Conventional Commits](https://www.conventionalcommits.org/):

- `feat:` -- a new feature
- `fix:` -- a bug fix
- `docs:` -- documentation-only changes
- `chore:` -- maintenance tasks (deps, CI, tooling)

All commits **must** include the following trailer:

```
Co-Authored-By: luminordagent <lumi.nordagent@gmail.com>
```

## Pull Request Guidelines

- Keep changes focused -- one logical change per PR.
- Include tests for any new or modified behaviour.
- Make sure `go test ./...` passes before opening a PR.
- Reference related issues in the PR description when applicable.
