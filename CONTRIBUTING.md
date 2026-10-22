# Contributing to sweeper

Thanks for your interest in contributing! This document explains how to set up a development
environment and get a change merged.

By participating in this project you agree to abide by the [Code of Conduct](CODE_OF_CONDUCT.md).

## Reporting issues

- **Bugs and feature requests**: open a [GitHub issue](https://github.com/omniva-ai/sweeper/issues/new/choose)
  using one of the templates.
- **Security vulnerabilities**: do **not** open a public issue. Follow [SECURITY.md](SECURITY.md).

For larger changes (new CRD fields, behavior changes, new dependencies) please open an issue to
discuss the design before writing code. Significant design decisions are recorded as ADRs in
[`docs/adr/`](docs/adr/).

## Development setup

All toolchain versions are pinned in [`mise.toml`](mise.toml). Install [mise](https://mise.jdx.dev/),
then from the repository root:

```
mise install
```

You also need Docker (or a compatible container runtime) for image builds and the e2e tests.

## Making a change

1. Fork the repository and create a branch from `main`.
2. Read [`docs/PRODUCT.md`](docs/PRODUCT.md) and [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).
   A change that would break one of the invariants in `ARCHITECTURE.md` must update that
   document in the same pull request.
3. Make your change, with tests.
4. Run the checks locally:

   ```
   make lint                 # golangci-lint, including license header checks
   make test                 # envtest-backed unit tests
   make test-e2e             # kind-based e2e tests (optional locally, always run in CI)
   make manifests generate   # regenerate CRDs, RBAC and deepcopy code; commit the result
   ```

5. Open a pull request against `main` and fill in the template.

Every Go source file must start with the Apache 2.0 header in
[`hack/boilerplate.go.txt`](hack/boilerplate.go.txt).

## Commit messages

Commits follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>[optional scope]: <description>

[optional body]

[optional footer(s)]
```

Allowed types: `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`,
`chore`, `revert`. Use the imperative mood, keep the subject under 72 characters, and explain
*why* in the body.

## Developer Certificate of Origin

All commits must be signed off to certify that you wrote the change or otherwise have the right
to submit it under the project's license, per the [Developer Certificate of Origin](https://developercertificate.org/).
Add the sign-off with `git commit -s`, which appends:

```
Signed-off-by: Your Name <you@example.com>
```

The DCO check on pull requests fails if any commit is missing a sign-off. To fix existing
commits, run `git rebase --signoff main` and force-push your branch.

## Review and merge

- At least one maintainer (see [MAINTAINERS.md](MAINTAINERS.md)) must approve the pull request.
- All CI checks must pass.
- Pull requests are merged with rebase-merge, so keep each commit meaningful. Squash fixup
  commits before merge.

## Releases

Maintainers cut releases by pushing a SemVer tag (e.g. `v0.2.0`) to `main`. The release workflow
builds, signs and publishes the image and manifests, and creates the GitHub Release with
generated notes.
