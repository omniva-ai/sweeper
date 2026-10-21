# Architecture: sweeper

See [`PRODUCT.md`](PRODUCT.md) for purpose and non-goals. See [`docs/adr/`](adr/) for the
rationale behind decisions marked (ADR-000N) below.

## Overview

`sweeper` is a single-binary Kubernetes operator built on `controller-runtime`. Its only
reconciled resource is its own CRD, `PodSweepPolicy` (ADR-0001); it does not reconcile `Pod`
objects directly, though it watches them to trigger re-evaluation.

```mermaid
flowchart LR
    subgraph Cluster
        P[PodSweepPolicy CRs]
        Pods[Pod objects]
    end
    Ctrl[sweeper controller]
    Metrics[/metrics endpoint/]

    P -- watch --> Ctrl
    Pods -- watch: phase=Failed --> Ctrl
    Ctrl -- list/delete --> Pods
    Ctrl -- expose --> Metrics
```

## The `PodSweepPolicy` CRD

Cluster-scoped (it's a statement about cluster-wide pod hygiene, not owned by one namespace),
group `sweeper.omniva.com`, version `v1alpha1`, kind `PodSweepPolicy`.

```yaml
apiVersion: sweeper.omniva.com/v1alpha1
kind: PodSweepPolicy
metadata:
  name: stale-batch-pods
spec:
  namespaceSelector: {}   # metav1.LabelSelector; empty = all namespaces, subject to the
                          # protected-namespace gate below
  podSelector: {}         # metav1.LabelSelector; empty = all pods
  ttl: 1h                 # metav1.Duration; how long a pod may sit in Failed before it's
                          # eligible for deletion
  mode: report            # enforce | report | disable — defaults to report if omitted
status:
  observedGeneration: 1
  conditions:
    - type: Ready
      status: "True"
    - type: NamespacesSkippedProtected
      status: "False"
```

### Rule modes

Mode semantics are deliberately explicit, since this is the easiest thing to get subtly wrong:

| Mode      | Evaluated? | Metrics emitted? | Deletes pods? |
|-----------|:----------:|:-----------------:|:--------------:|
| `disable` | No         | No                 | No             |
| `report` (default) | Yes | Yes           | No             |
| `enforce` | Yes        | Yes                | Yes            |

- `disable` — the rule is not evaluated at all: no listing, no metrics, as if it didn't exist.
- `report` — the rule is evaluated (matching namespaces/pods are listed, the TTL cutoff is
  applied) and metrics are emitted for identified candidates, but **no delete calls are made**.
  This is the default so that installing a rule is always safe-by-default; an operator must take
  a deliberate action to start deleting anything.
- `enforce` — same evaluation as `report`, plus pods that have crossed their TTL are deleted, and
  the deletion counter increments.

### Identifying "evicted/failed" pods

A pod is a candidate if `pod.Status.Phase == v1.PodFailed`. This phase covers both kubelet
eviction (`pod.Status.Reason == "Evicted"`) and other terminal container failures under
`restartPolicy: Never`.

There is no single authoritative field for "when did this pod become Failed", so the "failed
since" timestamp is computed with this fallback order:

1. max of `containerStatuses[].state.terminated.finishedAt` across all containers, if any are
   terminated
2. else `pod.Status.StartTime`
3. else `pod.CreationTimestamp`

A pod becomes a deletion candidate once `now - failedSince >= spec.ttl`.

### No rules → no action

If zero `PodSweepPolicy` objects exist in the cluster, `sweeper` takes no action of any kind —
no listing, no deleting, no metrics beyond zero. This isn't a special-cased guard; it falls out
directly from the fact that all work happens inside `Reconcile(ctx, req)` for a specific
`PodSweepPolicy`, and with no such objects the reconcile loop is simply never invoked.

## Protected namespaces (operator-level gate)

Certain namespaces are protected regardless of what any individual rule's `namespaceSelector`
says. This is an **operator-level** invariant, not something a `PodSweepPolicy` author can
override:

- Default protected list: `kube-system`.
- Operator flag `--protected-namespace` (repeatable/comma-separated) overrides the default list
  entirely, for environments that need a different baseline.
- Operator flag `--allow-protected-namespace` (repeatable) explicitly unlocks specific namespaces
  from the protected list, so a cluster operator who really does want `sweeper` cleaning up
  `kube-system` pods can say so at the operator level.

When a rule's `namespaceSelector` would otherwise match a protected-and-not-unlocked namespace,
that namespace is excluded from evaluation for that rule. The exclusion is never silent: the
rule's `status.conditions` gets a `NamespacesSkippedProtected` entry and a Warning event is
recorded against the `PodSweepPolicy`.

## Sub-reconciler wiring

`sweeper` uses a sub-reconciler pattern (ADR-0002). The `reconciler.Reconciler` interface in
`internal/reconciler` is:

```go
type Reconciler interface {
    Name() string
    Reconcile(context.Context) (ctrl.Result, error)
    Delete(context.Context) (ctrl.Result, error)
}

type Watcher interface {
    Watches() []WatchFunc
}
```

The main controller's `Reconcile` is keyed on `PodSweepPolicy`. It manages the finalizer
`sweeper.omniva.com/finalizer`: a non-deleting policy that doesn't have it yet gets it added (and
the reconcile returns immediately, resuming on the `Update`-triggered requeue) before any
sub-reconciler runs. This guarantees `Delete` actually gets a chance to run cleanup — without a
finalizer, the API server could remove a `PodSweepPolicy` before any reconcile observes a live
`DeletionTimestamp`. On every subsequent call it walks an ordered list of sub-reconcilers, calling
`Reconcile` on each in turn (or `Delete` on all of them, instead, when the `PodSweepPolicy` has a
deletion timestamp), stopping at the first error or the first non-zero `ctrl.Result`; once the
`deleting` pass completes cleanly, the finalizer is removed so the object can actually be
deleted:

```mermaid
sequenceDiagram
    participant Mgr as controller-runtime Manager
    participant Main as PodSweepPolicy controller
    participant V as ValidationSubReconciler
    participant E as EvaluationSubReconciler

    Mgr->>Main: Reconcile(req)
    Main->>V: Reconcile(ctx)
    V-->>Main: ctrl.Result{}, nil
    Main->>E: Reconcile(ctx)
    E-->>Main: ctrl.Result{RequeueAfter: jittered}, nil
    Main-->>Mgr: ctrl.Result{RequeueAfter: jittered}, nil
```

1. **`ValidationSubReconciler`** — parses `spec.ttl`, resolves `spec.mode` (empty → `report`),
   applies the protected-namespace gate to `spec.namespaceSelector`, and sets `status.conditions`
   accordingly. Fails fast (returns an error) on an unparseable `ttl` or unknown `mode` value so
   the object's status reflects a clear validation failure rather than silently no-op'ing.
2. **`EvaluationSubReconciler`** — for the resolved (post-protected-namespace-gate) namespace and
   pod selectors: lists `Failed`-phase pods, computes failed-since + TTL-eligibility for each,
   updates the "currently identified" gauge, and — only when `mode == enforce` — deletes eligible
   pods and increments the deletion counter. It also implements `Watcher`, registering a watch on
   `v1.Pod` (filtered to `phase == Failed` transitions) mapped back to every `PodSweepPolicy`
   whose selectors could match it, via a controller-runtime field index, so a pod entering
   `Failed` triggers re-evaluation without waiting for the next poll.
3. **`Delete`** is a no-op on `ValidationSubReconciler`, and on `EvaluationSubReconciler` purges
   that policy's metric series — deleting a `PodSweepPolicy` only stops its evaluation; it must
   never cascade into deleting pods. The main controller's finalizer (above) is what guarantees
   this method is actually reached before the object disappears.

### Requeue

TTL expiry is time-based, not event-based, so the controller still needs periodic re-evaluation
even with the pod watch in place (a pod might already be past `failedSince` but the TTL only
elapses later, with no further pod event to trigger a re-check). `EvaluationSubReconciler`
computes:

```go
RequeueAfter = jitter.JitteredIntervalDuration(min(defaultPollInterval, timeUntilNextTTLExpiry))
```

using `internal/jitter` (ADR-0002).
`jitter.SetGlobalIntervalJitter` is configured once at manager startup with a named constant
(e.g. `requeueJitterPercent = 0.10`, not a magic number inlined at the call site).

## Metrics

Exposed on controller-runtime's standard `/metrics` endpoint (manager setup must not disable the
default metrics server, so the standard controller-runtime metrics — reconcile total/duration/
errors, workqueue depth — ship for free).

Custom metrics, both labeled by `namespace` and `policy` (the owning `PodSweepPolicy` name):

- `sweeper_evicted_failed_pods_current` (Gauge) — currently identified candidates at last
  evaluation. Updated by `EvaluationSubReconciler` regardless of mode, **including `report`** —
  this is the metric an operator watches before flipping a rule to `enforce`.
- `sweeper_pods_deleted_total` (Counter) — cumulative deletions. Only incremented in `enforce`
  mode.

## Invariants

These must hold regardless of implementation detail; a code change that would violate one of
these needs to stop and either be redesigned or have this doc explicitly updated first:

- Must never evaluate or act on a `disable`d rule.
- Must never delete a pod outside `enforce` mode.
- Must never evaluate a protected namespace unless explicitly unlocked at the operator level via
  `--allow-protected-namespace`.
- Must never take any action (listing, metrics, or deletion) when zero `PodSweepPolicy` objects
  exist in the cluster.
- Deleting a `PodSweepPolicy` must never cascade into deleting the pods it was evaluating.

## Delivery

### Toolchain: mise + devbox

Both are configured and kept in sync, pinning: Go, `controller-gen`, `golangci-lint`, `kind`,
`kubectl`, and `setup-envtest` (for the envtest binaries unit tests need). Either toolchain works
locally; CI uses mise to install the pinned versions (simpler to script in a GitLab CI image than
devbox's Nix-based install).

### Tests

- **Unit tests** — `sigs.k8s.io/controller-runtime/pkg/envtest`, table-driven, one file per
  sub-reconciler plus dedicated tests for the protected-namespace gate and the failed-since
  algorithm.
- **e2e tests** — `kind`-based, under `test/e2e`: build the image, load it into a `kind` cluster,
  apply the CRDs and sample `PodSweepPolicy` objects, and assert on both `report` (pod survives,
  gauge increments) and `enforce` (pod is deleted, counter increments) behavior.

### CI (`.gitlab-ci.yml`)

Stages: `lint` (`golangci-lint`) → `build` (`go build ./...`) → `test` (`go test -race ./...` via
envtest) → `e2e` (spin up `kind`, run `test/e2e`).

### Release pipeline

Triggered on a semver tag push (`v*.*.*`):

1. Build and push a multi-arch application OCI image tagged with the semver.
2. Render the kustomize manifests and push them as a second OCI artifact tagged with the same
   semver (ADR-0003 records the exact mechanism).
3. Attach the same rendered manifests to the GitLab Release as a release asset link, so the
   release page has them directly downloadable without pulling the OCI artifact.

## Deep dives

None yet — this hub stays the single canonical entry point. If a topic here grows enough detail
to need its own page (e.g. the failed-since algorithm's edge cases), add it under
`docs/architecture/` and link it here rather than expanding this file indefinitely.
