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
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
	"github.com/omniva-ai/sweeper/internal/reconciler"
)

// finalizerName blocks a PodSweepPolicy from being removed by the API server until this
// controller has run every sub-reconciler's Delete (e.g. EvaluationSubReconciler's metrics
// cleanup) against it. Without it, a normal zero/expired-grace deletion could remove the object
// before any reconcile observes a live DeletionTimestamp, and Delete would never run.
const finalizerName = "sweeper.omniva.com/finalizer"

// PodSweepPolicyReconciler reconciles a PodSweepPolicy object.
type PodSweepPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// ProtectedNamespaces is the operator-level protected-namespace list (from
	// --protected-namespace, default kube-system).
	ProtectedNamespaces []string
	// AllowedNamespaces explicitly unlocks protected namespaces (from --allow-protected-namespace).
	AllowedNamespaces []string

	firstSeenOnce sync.Once
	firstSeen     *firstSeenTracker
}

// +kubebuilder:rbac:groups=sweeper.omniva.com,resources=podsweeppolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=sweeper.omniva.com,resources=podsweeppolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sweeper.omniva.com,resources=podsweeppolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile walks the ordered sub-reconciler chain for a PodSweepPolicy (ADR-0002), stopping at
// the first error or first non-zero ctrl.Result. A sub-reconciler that returns errStopChain
// stops the chain, and Reconcile then returns no error and no requeue.
func (r *PodSweepPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var policy sweeperv1alpha1.PodSweepPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	deleting := !policy.DeletionTimestamp.IsZero()

	if !deleting {
		if controllerutil.AddFinalizer(&policy, finalizerName) {
			return ctrl.Result{}, r.Update(ctx, &policy)
		}
	}

	tracker := r.firstSeenTracker()
	if err := tracker.prune(ctx, r.Client); err != nil {
		logf.FromContext(ctx).Error(err, "pruning first-seen Failed times")
	}

	protected := sets.New(r.ProtectedNamespaces...)
	allowed := sets.New(r.AllowedNamespaces...)

	subReconcilers := []reconciler.Reconciler{
		NewValidationSubReconciler(r.Client, r.Recorder, &policy, protected, allowed),
		NewEvaluationSubReconciler(r.Client, r.Recorder, &policy, protected, allowed, tracker),
	}

	for _, sub := range subReconcilers {
		var (
			result ctrl.Result
			err    error
		)
		if deleting {
			result, err = sub.Delete(ctx)
		} else {
			result, err = sub.Reconcile(ctx)
		}
		if errors.Is(err, errStopChain) {
			return ctrl.Result{}, nil
		}
		if err != nil || result != (ctrl.Result{}) {
			return result, err
		}
	}

	if deleting && controllerutil.RemoveFinalizer(&policy, finalizerName) {
		return ctrl.Result{}, r.Update(ctx, &policy)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodSweepPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	bldr := ctrl.NewControllerManagedBy(mgr).
		For(&sweeperv1alpha1.PodSweepPolicy{}).
		Named("podsweeppolicy")

	eval := &EvaluationSubReconciler{Client: r.Client}
	for _, w := range eval.Watches() {
		watch := w(mgr, mgr.GetLogger(), &sweeperv1alpha1.PodSweepPolicy{})
		bldr = bldr.Watches(watch.Object, watch.EventHandler, watch.Options...)
	}

	return bldr.Complete(r)
}

// firstSeenTracker returns the tracker shared by every reconcile, creating it on first use so a
// PodSweepPolicyReconciler built as a struct literal works without extra setup.
func (r *PodSweepPolicyReconciler) firstSeenTracker() *firstSeenTracker {
	r.firstSeenOnce.Do(func() { r.firstSeen = newFirstSeenTracker() })
	return r.firstSeen
}
