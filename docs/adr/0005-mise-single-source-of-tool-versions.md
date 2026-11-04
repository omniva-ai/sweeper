# 0005: Use mise.toml as the single source of tool versions

## Status

Accepted

## Context

The `Makefile` installed `golangci-lint`, `controller-gen`, `setup-envtest` and `kustomize` into
`bin/` with its own versions. `mise.toml` also pinned `golangci-lint`, `controller-gen` and
`setup-envtest`. The two `setup-envtest` versions were different. `kustomize` was only in the
`Makefile`. CI installed the mise tools through `jdx/mise-action`, but the `make` targets used
the `bin/` copies. Thus the local and CI tools did not always agree.

The Go version is in three files: `mise.toml`, `go.mod` and the `Dockerfile`. Dependabot updates
`go.mod` and the `Dockerfile`, but Dependabot cannot update `mise.toml`.

## Decision

- `mise.toml` is the only file that pins the development tools. The `Makefile` uses the tools on
  `PATH` and stops with an error when a tool is missing. The `Makefile` does not install tools.
- Renovate updates `mise.toml` only (`enabledManagers: ["mise"]`). Dependabot continues to
  update Go modules, GitHub Actions and the `Dockerfile`.
- The Go version in `mise.toml`, the `go` directive in `go.mod` and the `golang` image tag in the
  `Dockerfile` must be equal. `make check-go-version` does this check, and CI runs it.
- Renovate ignores `go` in `mise.toml`. A Dependabot pull request for the `golang` image starts a
  Go update. The CI check fails on that pull request until a maintainer adds the same version to
  `mise.toml` and `go.mod`.

## Considered options

- **Keep the versions in the `Makefile`** and remove the duplicate entries from `mise.toml`. This
  is the kubebuilder default, but then the tool versions are in two files, because `kind`,
  `kubectl`, `oras` and `cosign` stay in `mise.toml`.
- **Use the `PATH` tool and fall back to a `bin/` install.** The fallback needs a second version
  pin, so the problem stays.
- **Replace Dependabot with Renovate.** One updater is simpler, but the change is larger than
  necessary.
- **Let Renovate update Go in all three files.** This needs Renovate rules for the `Dockerfile`
  and `go.mod`, and Dependabot must ignore Go. The two updaters then share one ecosystem.

## Consequences

- Each developer must run `mise install` before `make`. A plain Go installation is not sufficient.
- A Go update is not fully automatic. A maintainer must change two files on the Dependabot pull
  request.
- The Renovate GitHub App must be installed on the repository.
