# Sweeper

[![CI](https://github.com/omniva-ai/sweeper/actions/workflows/ci.yml/badge.svg)](https://github.com/omniva-ai/sweeper/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/omniva-ai/sweeper)](https://goreportcard.com/report/github.com/omniva-ai/sweeper)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/omniva-ai/sweeper/badge)](https://scorecard.dev/viewer/?uri=github.com/omniva-ai/sweeper)
[![License](https://img.shields.io/github/license/omniva-ai/sweeper)](LICENSE)

Policy-driven eviction of stale Kubernetes resources.

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

## Install

Install the latest release (CRDs, RBAC and the manager Deployment) with:

```
kubectl apply -f https://github.com/omniva-ai/sweeper/releases/latest/download/install.yaml
```

To install a specific version, replace `latest/download` with `download/<version>`, e.g.
`download/v0.1.0`. The same manifests are also published as an OCI artifact:

```
oras pull ghcr.io/omniva-ai/sweeper-manifests:v0.1.0
kubectl apply -f install.yaml
```

The manager image is `ghcr.io/omniva-ai/sweeper:<version>`.

### Verifying releases

Release images and manifests are signed with [cosign](https://github.com/sigstore/cosign)
keyless signing from the release workflow:

```
cosign verify ghcr.io/omniva-ai/sweeper:v0.1.0 \
  --certificate-identity-regexp '^https://github\.com/omniva-ai/sweeper/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Use the same command with `ghcr.io/omniva-ai/sweeper-manifests:<version>` to verify the
manifests artifact.

## Uninstall

Delete the policies first, while the controller is still running. Every PodSweepPolicy carries
the `sweeper.omniva.com/finalizer` finalizer, and only the controller can remove it:

```
kubectl delete podsweeppolicies --all
```

Then remove the release manifests:

```
kubectl delete -f https://github.com/omniva-ai/sweeper/releases/latest/download/install.yaml
```

For a deploy from source, `make undeploy` does the first step for you.

If the controller was removed first, the policies (and the CRD) stay in `Terminating`. To
recover, remove sweeper's finalizer from every policy while keeping any finalizer owned by
another controller. This needs [`jq`](https://jqlang.org/):

```
kubectl get podsweeppolicies -o json | jq '.items[].metadata.finalizers |= map(select(. != "sweeper.omniva.com/finalizer"))' | kubectl replace -f -
```

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

## Development

### Prerequisites

Toolchain versions (Go, kind, kubectl, golangci-lint, controller-gen,
setup-envtest) are pinned via [mise](https://mise.jdx.dev/) (`mise.toml`):

```
mise install
```

### Build

```
make build          # builds bin/manager
make docker-build IMG=<tag>   # builds the manager image
```

### Run locally

```
make install         # installs the PodSweepPolicy CRD against your current kubeconfig context
make run             # runs the controller as a local process against that cluster
```

### Deploy from source

```
make install                   # CRDs only
make deploy IMG=<image>        # CRDs + RBAC + manager Deployment
```

Or render a single manifest with `make build-installer`, which writes
`dist/install.yaml`.

### Testing

```
make test           # envtest-backed unit tests
make test-e2e        # kind-cluster e2e tests (test/e2e/)
```

`make test-e2e` provisions a kind cluster via `make setup-test-e2e` and tears
it down via `make cleanup-test-e2e`.

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for how to get started. All
commits must be signed off under the [DCO](https://developercertificate.org/). This project
follows the [Code of Conduct](CODE_OF_CONDUCT.md). To report a security vulnerability, see
[SECURITY.md](SECURITY.md).

## License

Sweeper is licensed under the [Apache License 2.0](LICENSE).

## Further reading

- [`docs/PRODUCT.md`](docs/PRODUCT.md) — product scope and non-goals
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — components, CRD shape, invariants
- [`docs/adr/0001-crd-based-rules.md`](docs/adr/0001-crd-based-rules.md) — sweep rules via CRD, not static config
- [`docs/adr/0002-sub-reconciler-pattern.md`](docs/adr/0002-sub-reconciler-pattern.md) — sub-reconciler pattern and jitter package
- [`docs/adr/0003-oci-manifest-release.md`](docs/adr/0003-oci-manifest-release.md) — OCI-packaged manifest release artifact
