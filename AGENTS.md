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
# Run OpenBSD integration checks on the dedicated lab as part of functional changes.
```

## Specs

Stored in `docs/specs/`. Format: `docs/specs/NNNN-title.md`.

## Default OpenBSD integration environment

Use the **reusable OpenBSD 7.9 lab** for integration verification of vaultctl changes, especially Git operations, client/server behavior, external documentation, submodules, and filesystem/platform-specific behavior. Do this **by default during development and verification**, without waiting for the operator to request a lab test.

- Develop in the normal `~/src/vaultctl` checkout on **Ghost**; continue local Go tests, formatting, and vet there. The lab is the integration target, not the authoritative source checkout.
- Hypervisor: **Scholomance**; ephemctl: `/usr/local/bin/ephemctl`; environment: **`vaultctl-lab-proof`**; profile: `vaultctl-lab`. The older VM named `vaultctl-lab` uses the clean OpenBSD base and is **not** the reusable test target.
- Follow [`docs/testing/openbsd-lab.md`](docs/testing/openbsd-lab.md) for the current guest address, SSH host-key verification, Ghost-to-OpenBSD cross-compilation and transfer, guest paths, synthetic repositories, server/client configuration, and smoke tests. Do not hardcode an old DHCP address.
- For functional changes, build the OpenBSD/amd64 executable, install it **into the lab guest**, and exercise relevant behavior using its synthetic Git repositories in addition to local tests. Run server- and client-mode smoke checks where relevant. Record what was actually verified; if the lab is unreachable or unavailable, report OpenBSD integration as **not run**, rather than silently skipping it or claiming success.
- **Never use `daemon`, production vault paths, real vault content, or real project remotes for tests.** Treat the lab's repositories as fixtures; keep test operations scoped to them.
- Do not automatically `reset` or `destroy` any VM, replace the golden image, or discard existing lab state. Such actions are destructive and require explicit operator approval; the runbook describes the recovery procedure.


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
