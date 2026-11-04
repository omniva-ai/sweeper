# sweeper

The language of sweeper: an operator that removes stale Failed pods after a delay, scoped by
operator-defined policies.

## Policies

**Policy**:
A `PodSweepPolicy` object: a namespace selector, a pod selector, a TTL and a mode that together
say which Failed pods sweeper may remove and when.
_Avoid_: Rule, sweep rule

**Mode**:
The setting that says what a policy does with the pods it matches: `report`, `enforce` or
`disable`.
_Avoid_: Action, level

**Report mode**:
The default mode, in which a policy evaluates and counts candidate pods but deletes nothing.
_Avoid_: Dry run

**Enforce mode**:
The mode in which a policy deletes candidate pods that are past their TTL.

**Disable mode**:
The mode in which a policy is not evaluated at all, as if it did not exist.
_Avoid_: Paused, off

**Overlapping policies**:
Two or more policies whose selectors match the same pod; each evaluates it independently.
_Avoid_: Conflicting policies, competing policies

## Pods

**Candidate pod**:
A pod in the `Failed` phase that is not terminating and that a policy's selectors match.
_Avoid_: Stale pod, dead pod, target

**Evicted pod**:
A candidate pod that the kubelet failed with reason `Evicted`; sweeper treats it like any other
candidate pod.

**Failed since**:
The time sweeper takes as the moment a pod became Failed, from which its TTL is counted.
_Avoid_: Failure time, age

**First-seen time**:
The time sweeper first observed a pod as Failed, used when the pod's own signals do not explain
the failure.

**TTL**:
How long a candidate pod may stay Failed before a policy in enforce mode may delete it.
_Avoid_: Grace period, retention

## Namespaces

**Protected namespace**:
A namespace that the operator, not a policy, marks as off-limits; `kube-system` by default.
_Avoid_: System namespace, reserved namespace

**Allowed namespace**:
A protected namespace that the operator has explicitly unlocked so policies may act in it.
_Avoid_: Unlocked namespace, whitelisted namespace

**Namespace gate**:
The check that removes protected namespaces, other than allowed ones, from the namespaces a
policy selects.
