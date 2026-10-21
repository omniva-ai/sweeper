# Sweeper

A Kubernetes operator that deletes stale `Evicted`/`Failed` pods after a
configurable TTL, driven entirely by `PodSweepPolicy` custom resources.

## Overview

Sweeper takes no action unless a `PodSweepPolicy` exists. Each policy selects
namespaces and pods, sets a TTL, and starts in `report` mode — counting
matches via metrics only — before being flipped to `enforce` mode, where
matching pods past their TTL are deleted. `kube-system` is
protected from deletion by default at the operator level; see
[Protected namespaces](#protected-namespaces).

Sweeper is not a general descheduler: it never touches `Running`, `Pending`,
or `Succeeded` pods, doesn't manage kubelet eviction policy, and isn't an
audit trail for deleted pods.

See [`docs/PRODUCT.md`](docs/PRODUCT.md) for full product scope and
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for components, CRD shape, and
invariants.

## Prerequisites

Toolchain versions (Go, kind, kubectl, golangci-lint, controller-gen,
setup-envtest) are pinned via [mise](https://mise.jdx.dev/) (`mise.toml`) or
[devbox](https://www.jetify.com/devbox) (`devbox.json`):

```
mise install
# or
devbox shell
```

## Build

```
make build          # builds bin/manager
make docker-build IMG=<tag>   # builds the manager image
```

## Run locally

```
make install         # installs the PodSweepPolicy CRD against your current kubeconfig context
make run             # runs the controller as a local process against that cluster
```

## Deploy to a cluster

```
make install                   # CRDs only
make deploy IMG=<image>        # CRDs + RBAC + manager Deployment
```

Or render a single manifest with `make build-installer`, which writes
`dist/install.yaml`.

Production releases also publish a standalone OCI-packaged manifest artifact
alongside the app image — see
[`docs/adr/0003-oci-manifest-release.md`](docs/adr/0003-oci-manifest-release.md).

## Testing

```
make test           # envtest-backed unit tests
make test-e2e        # kind-cluster e2e tests (test/e2e/)
```

`make test-e2e` provisions a kind cluster via `make setup-test-e2e` and tears
it down via `make cleanup-test-e2e`.

## Protected namespaces

Namespaces on the operator-level protected list are never evaluated, whatever a
`PodSweepPolicy`'s `namespaceSelector` says. The default list is `kube-system`.
Two manager flags change this:

| Flag | Effect |
|---|---|
| `--protected-namespace` | Replaces the default list. Repeatable and comma-separated. |
| `--allow-protected-namespace` | Unlocks specific namespaces from the protected list. Repeatable and comma-separated. |

For example, to protect a platform namespace as well as `kube-system`:

```
--protected-namespace=kube-system,platform-system
```

The flag replaces the default, so include `kube-system` when you set it. See
[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md#protected-namespaces-operator-level-gate)
for details.

## Example

[`config/samples/sweeper_v1alpha1_podsweeppolicy.yaml`](config/samples/sweeper_v1alpha1_podsweeppolicy.yaml)
is a starting `PodSweepPolicy` you can apply to a cluster.

## Further reading

- [`docs/PRODUCT.md`](docs/PRODUCT.md) — product scope and non-goals
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — components, CRD shape, invariants
- [`docs/adr/0001-crd-based-rules.md`](docs/adr/0001-crd-based-rules.md) — sweep rules via CRD, not static config
- [`docs/adr/0002-sub-reconciler-pattern.md`](docs/adr/0002-sub-reconciler-pattern.md) — sub-reconciler pattern and jitter package
- [`docs/adr/0003-oci-manifest-release.md`](docs/adr/0003-oci-manifest-release.md) — OCI-packaged manifest release artifact
