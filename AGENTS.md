# AGENTS.md — sweeper

Guidance for AI coding agents (and humans) contributing to this repository. See
[`CONTRIBUTING.md`](CONTRIBUTING.md) for the full contribution workflow.

## Project Context

Read [`docs/PRODUCT.md`](docs/PRODUCT.md) (purpose/non-goals),
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) (components, CRD shape, invariants) and
[`GLOSSARY.md`](GLOSSARY.md) (domain language) before making any non-trivial change. Relevant
ADRs live in [`docs/adr/`](docs/adr/). A change that would violate one of the invariants in
`ARCHITECTURE.md` must update that document first.

## Keeping the docs current

Treat `docs/ARCHITECTURE.md`, `GLOSSARY.md` and the ADRs as part of the code. For every change,
consider whether they need an update, and make that update in the same PR.

- **`docs/ARCHITECTURE.md`**: update it when a change alters components, the CRD shape, the
  reconcile flow, metrics, or an invariant.
- **`GLOSSARY.md`**: use its terms in code, docs and commit messages, not the `_Avoid_`
  synonyms. Add or change an entry when a change introduces a term or changes what a term means.
- **ADRs**: write a new ADR in `docs/adr/` for each significant decision — a choice between real
  alternatives, a new dependency or pattern, a change to the CRD/API shape or an invariant, or a
  change to the release or security posture. Bug fixes and refactors that keep behaviour do not
  need one.
  - Name it `NNNN-kebab-title.md` with the next free number, title it `# NNNN: <Title>`, and use
    the sections of the existing ADRs: `Status`, `Context`, `Decision`, `Consequences` and,
    optionally, `Alternatives rejected`.
  - Do not rewrite an accepted ADR. Supersede it with a new ADR and set the old one's status to
    `Superseded by NNNN`.
  - Add the new ADR to the ADR list in [`README.md`](README.md).

## Tech Stack

- Go, `controller-runtime`, kubebuilder-style layout (`api/`, `internal/controller/`, `config/`).
- Toolchain pinned via [`mise`](https://mise.jdx.dev/) (`mise.toml`).
- CI and the tag-triggered release pipeline run in GitHub Actions (`.github/workflows/`).

## Before submitting

```
make lint
make test
make manifests generate   # commit any regenerated files
make check-go-version
```

Check that `docs/ARCHITECTURE.md`, `GLOSSARY.md` and `docs/adr/` reflect the change (see
[Keeping the docs current](#keeping-the-docs-current)).

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
