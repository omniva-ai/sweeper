/*
Copyright 2026 Omniva.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
	"github.com/omniva-ai/sweeper/internal/metrics"
	"github.com/omniva-ai/sweeper/internal/reconciler"
	"github.com/omniva-ai/sweeper/internal/requeue"
)

// defaultPollInterval bounds how long EvaluationSubReconciler waits before re-checking TTL
// expiry when no Pod watch event arrives in the meantime.
const defaultPollInterval = 5 * time.Minute

const (
	reasonEvaluated     = "Evaluated"
	reasonPodListFailed = "PodListFailed"
	reasonDeleteFailed  = "DeleteFailed"
)

// EvaluationSubReconciler lists Failed-phase pods in the namespaces resolveNamespaceGate matches
// for this policy, deletes those that have crossed TTL when Mode is enforce, and re-triggers
// evaluation on Pod phase=Failed transitions. It records the outcome of each pass in the
// Evaluated condition (True after a clean pass, False with the failure reason otherwise) and
// records a Warning event when that condition turns False. It implements reconciler.Reconciler
// and Watcher.
type EvaluationSubReconciler struct {
	client.Client

	Recorder            record.EventRecorder
	Policy              *sweeperv1alpha1.PodSweepPolicy
	ProtectedNamespaces sets.Set[string]
	AllowedNamespaces   sets.Set[string]

	// Now returns the current time; overridden in tests. Defaults to time.Now.
	Now func() time.Time
}

// NewEvaluationSubReconciler constructs an EvaluationSubReconciler for a single reconcile
// request against policy.
func NewEvaluationSubReconciler(cl client.Client, recorder record.EventRecorder,
	policy *sweeperv1alpha1.PodSweepPolicy, protected, allowed sets.Set[string]) *EvaluationSubReconciler {
	return &EvaluationSubReconciler{
		Client:              cl,
		Recorder:            recorder,
		Policy:              policy,
		ProtectedNamespaces: protected,
		AllowedNamespaces:   allowed,
	}
}

// Name implements reconciler.Reconciler.
func (r *EvaluationSubReconciler) Name() string {
	return "Evaluation"
}

// Reconcile implements reconciler.Reconciler.
func (r *EvaluationSubReconciler) Reconcile(ctx context.Context) (ctrl.Result, error) {
	if r.Policy.Spec.Mode == sweeperv1alpha1.ModeDisable {
		metrics.DeletePolicy(r.Policy.Name)
		changed := setCondition(r.Policy, sweeperv1alpha1.ConditionTypeEvaluated,
			metav1.ConditionFalse, reasonDisabled, "mode is disable; rule is not evaluated")
		return ctrl.Result{}, writeStatusIfChanged(ctx, r.Client, r.Policy, changed)
	}

	matched, _, err := resolveNamespaceGate(ctx, r.Client, r.Policy.Spec.NamespaceSelector,
		r.ProtectedNamespaces, r.AllowedNamespaces)
	if err != nil {
		return ctrl.Result{}, r.failEvaluated(ctx, reasonNamespaceListFailed, fmt.Errorf("listing namespaces: %w", err))
	}

	podSelector, err := asSelector(r.Policy.Spec.PodSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("parsing pod selector: %w", err)
	}

	var candidates []corev1.Pod
	for _, ns := range matched {
		var podList corev1.PodList
		opts := []client.ListOption{client.InNamespace(ns)}
		if !podSelector.Empty() {
			opts = append(opts, client.MatchingLabelsSelector{Selector: podSelector})
		}
		if err := r.List(ctx, &podList, opts...); err != nil {
			return ctrl.Result{}, r.failEvaluated(ctx, reasonPodListFailed, fmt.Errorf("listing pods in namespace %s: %w", ns, err))
		}
		for i := range podList.Items {
			if podList.Items[i].Status.Phase == corev1.PodFailed && podList.Items[i].DeletionTimestamp == nil {
				candidates = append(candidates, podList.Items[i])
			}
		}
	}

	now := time.Now
	if r.Now != nil {
		now = r.Now
	}

	eligible, requeueAfter := evaluate(candidates, r.Policy.Spec.TTL.Duration, now(), defaultPollInterval)

	eligibleByNamespace := make(map[string]float64, len(matched))
	for _, ns := range matched {
		eligibleByNamespace[ns] = 0
	}
	for i := range eligible {
		eligibleByNamespace[eligible[i].Namespace]++
	}
	metrics.ResetCurrent(r.Policy.Name, eligibleByNamespace)

	var deleteErr error
	if r.Policy.Spec.Mode == sweeperv1alpha1.ModeEnforce {
		for i := range eligible {
			pod := &eligible[i]
			if err := r.Client.Delete(ctx, pod, client.Preconditions{UID: &pod.UID}); err != nil {
				// With only a UID precondition, Conflict means a new pod now has this name.
				if !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
					deleteErr = errors.Join(deleteErr, fmt.Errorf("deleting pod %s/%s: %w", pod.Namespace, pod.Name, err))
				}
				continue
			}
			metrics.IncDeleted(pod.Namespace, r.Policy.Name)
		}
	}

	if deleteErr != nil {
		return requeue.WithJitterAndError(requeueAfter, r.failEvaluated(ctx, reasonDeleteFailed, deleteErr))
	}

	changed := setCondition(r.Policy, sweeperv1alpha1.ConditionTypeEvaluated,
		metav1.ConditionTrue, reasonEvaluated, "pods were listed and evaluated")
	if err := writeStatusIfChanged(ctx, r.Client, r.Policy, changed); err != nil {
		return ctrl.Result{}, err
	}

	return requeue.WithJitterAndError(requeueAfter, nil)
}

// failEvaluated sets Evaluated=False with reason and cause's message, records a Warning when
// the condition transitioned to this failure, writes status, and returns cause joined with any
// status error. Callers pick the result: an empty one, or the jittered requeue after a delete
// failure.
func (r *EvaluationSubReconciler) failEvaluated(ctx context.Context, reason string, cause error) error {
	changed := setFailureCondition(r.Recorder, r.Policy, sweeperv1alpha1.ConditionTypeEvaluated, reason, cause.Error())
	return writeFailure(ctx, r.Client, r.Policy, changed, cause)
}

// Delete implements reconciler.Reconciler. It is a no-op: EvaluationSubReconciler owns no
// finalizer-managed external state, and per the architecture's invariant, deleting a
// PodSweepPolicy must never cascade into deleting the pods it was evaluating.
func (r *EvaluationSubReconciler) Delete(_ context.Context) (ctrl.Result, error) {
	metrics.DeletePolicy(r.Policy.Name)
	return ctrl.Result{}, nil
}

// Watches implements reconciler.Watcher: a Pod entering the Failed phase re-triggers evaluation
// of every PodSweepPolicy whose (raw, spec-level) selectors could match it, without waiting for
// the next poll.
func (r *EvaluationSubReconciler) Watches() []reconciler.WatchFunc {
	return []reconciler.WatchFunc{r.podWatch}
}

func (r *EvaluationSubReconciler) podWatch(_ ctrl.Manager, log logr.Logger, _ client.Object) reconciler.Watch {
	return reconciler.Watch{
		Object:       &corev1.Pod{},
		EventHandler: handler.EnqueueRequestsFromMapFunc(r.mapFailedPodToPolicies(log)),
	}
}

func (r *EvaluationSubReconciler) mapFailedPodToPolicies(log logr.Logger) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		pod, ok := obj.(*corev1.Pod)
		if !ok || pod.Status.Phase != corev1.PodFailed {
			return nil
		}

		var ns corev1.Namespace
		if err := r.Get(ctx, client.ObjectKey{Name: pod.Namespace}, &ns); err != nil {
			log.Error(err, "getting namespace while mapping pod watch event", "namespace", pod.Namespace)
			return nil
		}

		var policies sweeperv1alpha1.PodSweepPolicyList
		if err := r.List(ctx, &policies); err != nil {
			log.Error(err, "listing PodSweepPolicy while mapping pod watch event")
			return nil
		}

		var reqs []reconcile.Request
		for i := range policies.Items {
			if policyMatchesPod(&policies.Items[i], pod, ns.Labels) {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&policies.Items[i])})
			}
		}
		return reqs
	}
}

// policyMatchesPod reports whether a PodSweepPolicy's raw spec-level selectors match the given
// pod's namespace labels and pod labels. A nil selector matches everything, per
// PodSweepPolicySpec's documented semantics.
func policyMatchesPod(policy *sweeperv1alpha1.PodSweepPolicy, pod *corev1.Pod, namespaceLabels labels.Set) bool {
	nsSelector, err := asSelector(policy.Spec.NamespaceSelector)
	if err != nil || !nsSelector.Matches(namespaceLabels) {
		return false
	}
	podSelector, err := asSelector(policy.Spec.PodSelector)
	if err != nil {
		return false
	}
	return podSelector.Matches(labels.Set(pod.Labels))
}

// asSelector converts a LabelSelector to a labels.Selector, treating nil as "match everything"
// per PodSweepPolicySpec's documented semantics (metav1.LabelSelectorAsSelector treats nil as
// "match nothing", which is the opposite of what this CRD documents).
func asSelector(ls *metav1.LabelSelector) (labels.Selector, error) {
	if ls == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(ls)
}

// failedSince computes when a Failed-phase pod became failed, using the fallback order from
// docs/ARCHITECTURE.md: max terminated container finishedAt, else pod.Status.StartTime, else
// pod.CreationTimestamp. There is no single authoritative field for this, so callers must not
// assume any one of these is always present.
func failedSince(pod *corev1.Pod) time.Time {
	var latest time.Time
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Terminated == nil {
			continue
		}
		if t := cs.State.Terminated.FinishedAt.Time; t.After(latest) {
			latest = t
		}
	}
	if !latest.IsZero() {
		return latest
	}
	if pod.Status.StartTime != nil {
		return pod.Status.StartTime.Time
	}
	return pod.CreationTimestamp.Time
}

// evaluate splits pods into those eligible for deletion (failed for at least ttl) and computes
// how long until the next not-yet-eligible pod becomes eligible, capped at defaultPollInterval.
func evaluate(pods []corev1.Pod, ttl time.Duration, now time.Time, defaultPollInterval time.Duration) (eligible []corev1.Pod, requeueAfter time.Duration) {
	requeueAfter = defaultPollInterval
	for i := range pods {
		age := now.Sub(failedSince(&pods[i]))
		if age >= ttl {
			eligible = append(eligible, pods[i])
			continue
		}
		if untilEligible := ttl - age; untilEligible < requeueAfter {
			requeueAfter = untilEligible
		}
	}
	return eligible, requeueAfter
}
