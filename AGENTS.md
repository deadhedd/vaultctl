# vaultctl

## Stack

- **Language / Runtime**: Go 1.26
- **Framework**: None
- **Key dependencies**: Go standard library, installed Git executable
- **Package manager**: Go modules

## Build approach

Tracer Bullet (one real path through every layer before broadening it).

## Commands

```bash
# Install
go mod download

# Dev server
# None, this is a command line tool

# Build
mkdir -p bin
go build -trimpath -o bin/vaultctl ./cmd/vaultctl

# Verify locally (the full suite runs in Linux CI)
go mod tidy
git diff --exit-code -- go.mod go.sum
test -z "$(gofmt -l .)"
go vet ./...
go test -race -shuffle=on ./...
# Windows CI runs vet, shuffled race tests, and the build.
# OpenBSD verification remains manual
```

## Specs

Stored in `docs/specs/`. Format: `docs/specs/NNNN-title.md`.

## Rules

- Use the Go standard library and call the installed Git executable with argument arrays.
- Keep client mode and server mode capabilities separate. Server mode does not run `sync`.
- Do not guess remotes or upstream branches, change privileges, or change Git configuration automatically.
- Stop on failed Git commands and unsafe or incomplete repository states.
- Never force push, discard local changes, or choose automatic conflict resolutions.
- Keep tests isolated in disposable temporary repositories. Do not operate on a configured or production vault.
- Use `gofmt` and named table driven test cases for Go changes.

## Agent skills

- [golang-testing](.agents/skills/golang-testing/): `samber/cc-skills-golang`, Go test design and integration testing
- [golang-safety](.agents/skills/golang-safety/): `samber/cc-skills-golang`, defensive Go correctness and safe data handling

## Context files

- [internal/vaultctl/AGENTS.md](internal/vaultctl/AGENTS.md): command behavior, Git boundaries, synchronization rules, and local tests

_Drafted by /audit from the repo, worth a quick human pass. Edit freely: once a line stops matching this draft, later runs treat it as curated and will flag rather than overwrite it._
