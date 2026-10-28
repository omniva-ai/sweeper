# 0004: Compute failed-since from failure signals and an in-memory first-seen time

## Status

Accepted

## Context

The TTL of a `PodSweepPolicy` counts from when a pod failed. Kubernetes has no field that records
this. The time must come from other fields, and each field has a different clock:

- `containerStatuses[].state.terminated.finishedAt` and `initContainerStatuses[]` come from the
  node clock, through the kubelet.
- Pod condition `lastTransitionTime` values come from the component that sets the condition. For
  the kubelet that is the node clock. For the node lifecycle controller and the eviction API it
  is the control-plane clock.
- `status.startTime` and `metadata.creationTimestamp` say when the pod started, not when it
  failed.

The first implementation used the latest terminated `finishedAt` and fell back to
`status.startTime`. A pod that ran for weeks and then failed with no container termination (for
example after `NodeLost`, a node shutdown or `UnexpectedAdmissionError`) looked expired at once
and was deleted on the first pass.

An old signal can also predate the failure. Kubernetes keeps a condition's `lastTransitionTime`
when the status does not change, so a pod that was `Ready=False` for weeks keeps the old time
after it fails. In a multi-container pod, one container can end weeks before the pod fails.

## Decision

- **Candidates.** Take `finishedAt` of terminated main and init containers and the
  `lastTransitionTime` of `Ready=False`, `ContainersReady=False` and `DisruptionTarget=True`.
  Zero timestamps are not candidates. `PodScheduled`, `Initialized` and
  `PodReadyToStartContainers` carry no failure signal and are ignored. Ephemeral containers and
  `lastTerminationState` are ignored: a debug container or an earlier restart that ends does not
  mean the pod failed. `startTime` and `creationTimestamp` are never read.
- **Latest wins.** Use the latest candidate, not a priority order. The bug class is early
  deletion, so clock skew must err toward keeping the pod longer.
- **Cap at now.** `failedSince = min(latest, now)`. A node clock ahead of the controller cannot
  give a negative age, and the full TTL then applies from now.
- **Explained failure.** The candidates decide alone only when they explain the failure: every
  container in `spec.containers` has a terminated status with a `finishedAt`, or
  `DisruptionTarget=True` has a time. Otherwise the time when sweeper first saw the pod Failed
  joins the max. With no candidates it is the result.
- **In-memory first-seen.** The first-seen time lives in a map keyed by pod UID on
  `PodSweepPolicyReconciler`, shared by all policies and safe for concurrent use. Sweeper does
  not write to pods. Each reconcile prunes entries whose pod is gone, replaced, no longer Failed
  or terminating, read through the cached client.

## Consequences

- After a controller restart or failover the map is empty. A pod whose failure is not explained
  gets a new first-seen time and waits a full TTL again. A restart can only delay a deletion,
  never bring one forward.
- Ordinary container failures under `restartPolicy: Never` and evictions that set
  `DisruptionTarget` are explained, so restarts do not change their timing.
- No RBAC change: sweeper still needs only `get;list;watch;delete` on pods.
- A node clock that runs behind the controller by more than the TTL can still make an explained
  failure look expired early. Only the first-seen time would protect against this, and using it
  always would restart every TTL on each restart. Node clocks are expected to be NTP-synced.
- The map is pruned only while a reconcile runs. After the last policy is deleted, the remaining
  entries stay in memory until the next restart.

## Alternatives rejected

- **Pod annotation with the first-seen time.** It survives restarts, but it needs `patch` on
  pods and makes sweeper mutate the objects it only evaluates.
- **First-seen times in the policy status.** The status grows with the number of Failed pods, a
  pod matched by two policies has two times, and every sighting is a status write.
- **Use now every time there is no explained failure.** The pod would never become eligible,
  because now moves on with each pass.
- **Priority order of candidates** (for example conditions before containers). Any fixed order
  can pick an old signal over a newer one, which deletes early.
- **First-seen as an always-on candidate.** It would restart every TTL on each controller
  restart, also for pods whose failure time is known.
