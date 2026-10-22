# Product: sweeper

## Purpose

`sweeper` is a Kubernetes operator that removes stale `Evicted`/`Failed` pods from a cluster
after a configurable delay, so cluster operators don't have to script `kubectl delete pod`
cron jobs or accumulate a growing pile of dead pod objects that show up in `kubectl get pods`,
dashboards, and etcd.

It only ever acts on pods it has been explicitly told to act on: no rules configured means no
behavior at all.

## Core use case

1. A cluster operator installs `sweeper` and defines one or more `PodSweepPolicy` rules,
   each scoping a namespace/pod selector and a TTL (how long a pod may sit in the failed state
   before it's eligible for deletion).
2. Every rule starts in `report` mode: it evaluates and emits metrics (how many pods are
   currently identified as candidates) without deleting anything, so an operator can observe the
   blast radius before committing to real deletion.
3. Once satisfied, the operator flips the rule to `enforce` mode, and `sweeper` begins deleting
   pods that cross the TTL for that rule.
4. Certain namespaces (`kube-system` by default) are protected at the operator
   level regardless of what any rule's selector says, and require an explicit operator-level flag
   to unlock — a single rule author cannot accidentally sweep cluster-critical namespaces.

## Non-goals

- **Not a general pod-lifecycle or descheduling tool.** `sweeper` does not rebalance, evict, or
  reschedule `Running`/`Pending` pods, and does not replace tools like the Kubernetes
  descheduler.
- **Does not touch `Succeeded` pods.** Completed Job pods are out of scope; only pods in the
  `Failed` phase (which covers kubelet eviction and other terminal container failures) are
  candidates.
- **Does not manage node-level eviction policy.** `sweeper` cleans up the pod objects left behind
  by eviction; it does not configure kubelet eviction thresholds, taints, or `PriorityClass`
  behavior.
- **Not a backup or audit trail for deleted pods.** Deleted pod specs/logs are not preserved by
  the operator; if that's needed, pair `sweeper` with existing log/event shipping.
