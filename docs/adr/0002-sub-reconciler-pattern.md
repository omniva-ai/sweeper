# 0002: Use a sub-reconciler pattern and a shared jitter package

## Status

Accepted

## Context

A common way to structure Kubernetes controllers is a main reconciler that composes an ordered
list of focused "sub-reconcilers", each responsible for one concern. Periodic requeues across many
objects on the same interval also benefit from randomized spread (jitter) to avoid
thundering-herd re-reconciles. `sweeper` could either adopt these patterns or write a single
monolithic `Reconcile`.

## Decision

`sweeper`'s controller is composed of `ValidationSubReconciler` and `EvaluationSubReconciler`,
each implementing `reconciler.Reconciler` from `internal/reconciler` (and
`EvaluationSubReconciler` additionally implementing `Watcher` for the `Pod` watch):

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

Requeue delays use `internal/jitter` (`SetGlobalIntervalJitter` + `JitteredIntervalDuration`,
via `internal/requeue`) rather than a bespoke jitter calculation at each call site:

```go
func Percent(p float64, r *rand.Rand) Duration
func SetGlobalIntervalJitter(p float64, r *rand.Rand)
func JitteredIntervalDuration(d time.Duration) time.Duration
```

## Rationale

- **Focused, independently testable units.** Validation and evaluation each have their own
  sub-reconciler and test file, and adding a new concern means adding a new sub-reconciler rather
  than growing one function.
- **Proven jitter math.** `internal/jitter` is adapted from Flux's `runtime/jitter` package and
  already handles the edge cases (p <= 0 or p >= 1 returns `NoJitter`, seeding via crypto/rand
  when no `*rand.Rand` is supplied).

## Consequences

- `internal/reconciler`, `internal/requeue`, and `internal/jitter` are small in-tree packages
  that `sweeper` owns and maintains. `internal/jitter` retains its Flux attribution in the file
  headers.
- The "main controller composes sub-reconcilers in order, stopping at the first error or non-zero
  result" wiring is `sweeper`'s own implementation, documented in
  [`ARCHITECTURE.md`](../ARCHITECTURE.md#sub-reconciler-wiring).
