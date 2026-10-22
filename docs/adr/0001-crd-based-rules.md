# 0001: Rules are defined via a CRD, not static operator config

## Status

Accepted

## Context

`sweeper` needs a way for cluster operators to define which pods it should sweep (namespace/pod
selector) and after how long (TTL). Two options were considered:

1. A `PodSweepPolicy` CRD, reconciled like any other Kubernetes resource.
2. A single ConfigMap or Helm-values file read by the operator at startup (with reload-on-change
   via an fsnotify watch or similar).

## Decision

Rules are a cluster-scoped CRD, `PodSweepPolicy` (`sweeper.omniva.com/v1alpha1`). See
[`ARCHITECTURE.md`](../ARCHITECTURE.md#the-podsweeppolicy-crd) for the full shape.

## Rationale

- **RBAC per rule.** A CRD lets platform teams grant "can create/edit `PodSweepPolicy`" to
  specific teams via standard Kubernetes RBAC, rather than everyone sharing edit access to one
  ConfigMap.
- **`kubectl` visibility.** `kubectl get podsweeppolicies` and `kubectl describe` give operators
  a first-class way to see what rules exist and their `status.conditions`, the same way any
  other operator's custom resources are inspectable.
- **GitOps-friendly.** A CRD reconciles continuously and reacts to changes the same way any other
  Kubernetes object does; a ConfigMap-based design would need a bespoke watch-and-reload
  mechanism to get the same behavior, duplicating what `controller-runtime` already provides for
  free.
- **No operator restart required.** Adding, editing, or removing a rule takes effect on the next
  reconcile, not on the next operator rollout.

## Consequences

- Requires shipping and versioning a CRD (`v1alpha1` to start), including a schema migration
  story if the shape needs to change later.
- Slightly more moving parts for a first-time installer than "edit a ConfigMap" — mitigated by
  the CRD manifests being part of the same OCI-published release artifact as the operator image.
