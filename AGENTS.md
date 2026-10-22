# AGENTS.md — sweeper

Guidance for AI coding agents (and humans) contributing to this repository. See
[`CONTRIBUTING.md`](CONTRIBUTING.md) for the full contribution workflow.

## Project Context

Read [`docs/PRODUCT.md`](docs/PRODUCT.md) (purpose/non-goals) and
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) (components, CRD shape, invariants) before making
any non-trivial change. Relevant ADRs live in [`docs/adr/`](docs/adr/). A change that would
violate one of the invariants in `ARCHITECTURE.md` must update that document first.

## Tech Stack

- Go, `controller-runtime`, kubebuilder-style layout (`api/`, `internal/controller/`, `config/`).
- Toolchain pinned via [`mise`](https://mise.jdx.dev/) (`mise.toml`).
- CI and the tag-triggered release pipeline run in GitHub Actions (`.github/workflows/`).

## Before submitting

```
make lint
make test
make manifests generate   # commit any regenerated files
```

Every Go source file carries the Apache 2.0 header from `hack/boilerplate.go.txt`.

## Commit Message Format: Conventional Commits

Commit messages use [Conventional Commits](https://www.conventionalcommits.org/) and must be
signed off for the [DCO](https://developercertificate.org/) (`git commit -s`):

```
<type>[optional scope]: <description>

[optional body]

Signed-off-by: Your Name <you@example.com>
```

Allowed `type` values: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`,
`chore`, `revert`.

Good:
```
feat(policy): add TTL validation to PodSweepPolicy
fix(evaluator): correct failed-since fallback when no containers ran
docs: fix typo in README quickstart command
```

Bad:
```
fix bug
updated files
```

Use the imperative mood, keep the subject line under 72 characters, and explain *why* in the
body, not just *what*. Reference GitHub issues in the footer (e.g. `Fixes #123`) where one
exists.
