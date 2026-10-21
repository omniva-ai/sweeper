# 0003: Publish rendered manifests as a standalone OCI artifact

## Status

Accepted (mechanism revisitable — see Consequences)

## Context

The release pipeline (triggered on a semver tag push) needs to produce, in addition to the
application's OCI image:

- an OCI artifact containing the operator's Kubernetes manifests (CRDs, RBAC, Deployment, etc.),
  versioned the same way as the application image
- the same manifests attached to the GitHub Release itself as a downloadable asset

Two mechanisms were considered for the manifest OCI artifact:

1. Render the manifests with `kustomize build` and push the resulting YAML as a generic OCI
   artifact using `oras push`.
2. Package the manifests as a Helm chart and publish it to an OCI registry via `helm push`
   (Helm 3.8+ native OCI support).

## Decision

Render with `kustomize build` (kubebuilder's default scaffolding already produces a
`config/` kustomize tree) and publish the rendered YAML as a generic OCI artifact via
`oras push`, tagged with the release semver. The same rendered YAML is attached to the GitHub
Release as an asset via `gh release create` in the same workflow job. Both the image and the
manifests artifact are published to GHCR and signed keyless with `cosign`.

## Rationale

- **No new deploy-time dependency introduced for consumers.** Consumers who just want raw
  manifests (`kubectl apply -f` or pull the OCI artifact and apply it) don't need Helm installed;
  `oras pull` + `kubectl apply` is the whole workflow.
- **Matches the existing scaffolding.** Kubebuilder-style operators already produce a
  `config/` kustomize tree; this avoids adding a parallel Helm chart that has to be kept in sync
  with it.
- **Avoids a new templating dependency** for a v1 release, consistent with the general
  dependency-management principle of not introducing new external tooling as a default when a
  simpler standard-library-adjacent path (kustomize, which is part of `kubectl` already) covers
  the requirement.

## Consequences

- `oras` is added to the toolchain (`mise.toml`, per
  [`ARCHITECTURE.md`](../ARCHITECTURE.md#toolchain-mise)) purely as a release-time build tool,
  not a project dependency.
- If downstream consumers later want a Helm-native install path (e.g. `helm install` with values
  overrides), that would need its own follow-up ADR superseding this one — this decision is
  explicitly the "simplest thing that satisfies the current ask" rather than a permanent
  commitment to kustomize-only distribution.
