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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
)

const (
	reasonValidated                   = "Validated"
	reasonInvalidMode                 = "InvalidMode"
	reasonInvalidTTL                  = "InvalidTTL"
	reasonProtectedNamespacesExcluded = "ProtectedNamespacesExcluded"
	reasonNoProtectedNamespaces       = "NoProtectedNamespaces"
	reasonDisabled                    = "Disabled"
)

// ValidationSubReconciler validates spec.ttl/spec.mode, applies the operator-level
// protected-namespace gate to spec.namespaceSelector, and sets status.conditions accordingly. It
// implements reconciler.Reconciler.
type ValidationSubReconciler struct {
	Client              client.Client
	Recorder            record.EventRecorder
	Policy              *sweeperv1alpha1.PodSweepPolicy
	ProtectedNamespaces sets.Set[string]
	AllowedNamespaces   sets.Set[string]
}

// NewValidationSubReconciler constructs a ValidationSubReconciler for a single reconcile
// request against policy.
func NewValidationSubReconciler(cl client.Client, recorder record.EventRecorder,
	policy *sweeperv1alpha1.PodSweepPolicy, protected, allowed sets.Set[string]) *ValidationSubReconciler {
	return &ValidationSubReconciler{
		Client:              cl,
		Recorder:            recorder,
		Policy:              policy,
		ProtectedNamespaces: protected,
		AllowedNamespaces:   allowed,
	}
}

// Name returns the sub-reconciler's name.
func (r *ValidationSubReconciler) Name() string {
	return "validation"
}

// Reconcile validates spec.ttl/spec.mode, applies the protected-namespace gate, and sets
// status.conditions. The CRD schema already rejects a malformed or non-positive ttl and an
// unknown mode at admission; the checks here are defense in depth for objects stored before
// those rules existed. They fail fast (return an error) so the object's status reflects a clear
// validation failure rather than silently no-op'ing.
func (r *ValidationSubReconciler) Reconcile(ctx context.Context) (ctrl.Result, error) {
	mode := r.Policy.Spec.Mode
	if mode == "" {
		mode = sweeperv1alpha1.ModeReport
	}
	if mode != sweeperv1alpha1.ModeEnforce && mode != sweeperv1alpha1.ModeReport && mode != sweeperv1alpha1.ModeDisable {
		err := fmt.Errorf("invalid mode %q: must be one of %s, %s, %s", r.Policy.Spec.Mode,
			sweeperv1alpha1.ModeEnforce, sweeperv1alpha1.ModeReport, sweeperv1alpha1.ModeDisable)
		changed := r.setReady(metav1.ConditionFalse, reasonInvalidMode, err.Error())
		if statusErr := r.writeStatusIfChanged(ctx, changed); statusErr != nil {
			return ctrl.Result{}, errors.Join(err, statusErr)
		}
		return ctrl.Result{}, err
	}

	if r.Policy.Spec.TTL.Duration <= 0 {
		err := fmt.Errorf("invalid ttl %q: must be greater than zero", r.Policy.Spec.TTL.Duration)
		changed := r.setReady(metav1.ConditionFalse, reasonInvalidTTL, err.Error())
		if statusErr := r.writeStatusIfChanged(ctx, changed); statusErr != nil {
			return ctrl.Result{}, errors.Join(err, statusErr)
		}
		return ctrl.Result{}, err
	}

	// disable means "not evaluated at all" (docs/ARCHITECTURE.md): no namespace listing, no
	// event, as if the rule didn't exist. Must be checked before resolveNamespaceGate.
	if mode == sweeperv1alpha1.ModeDisable {
		skippedChanged := meta.SetStatusCondition(&r.Policy.Status.Conditions, metav1.Condition{
			Type:               sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
			Status:             metav1.ConditionFalse,
			Reason:             reasonDisabled,
			Message:            "rule is disabled; namespaces are not evaluated",
			ObservedGeneration: r.Policy.Generation,
		})
		readyChanged := r.setReady(metav1.ConditionTrue, reasonDisabled, "mode is disable; rule is not evaluated")
		return ctrl.Result{}, r.writeStatusIfChanged(ctx, skippedChanged || readyChanged)
	}

	_, skipped, err := resolveNamespaceGate(ctx, r.Client, r.Policy.Spec.NamespaceSelector,
		r.ProtectedNamespaces, r.AllowedNamespaces)
	if err != nil {
		return ctrl.Result{}, err
	}

	var skippedChanged bool
	if len(skipped) > 0 {
		message := fmt.Sprintf("namespaces excluded as protected and not explicitly allowed: %s",
			strings.Join(skipped, ", "))
		skippedChanged = meta.SetStatusCondition(&r.Policy.Status.Conditions, metav1.Condition{
			Type:               sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
			Status:             metav1.ConditionTrue,
			Reason:             reasonProtectedNamespacesExcluded,
			Message:            message,
			ObservedGeneration: r.Policy.Generation,
		})
		if skippedChanged {
			r.Recorder.Event(r.Policy, corev1.EventTypeWarning, reasonProtectedNamespacesExcluded, message)
		}
	} else {
		skippedChanged = meta.SetStatusCondition(&r.Policy.Status.Conditions, metav1.Condition{
			Type:               sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
			Status:             metav1.ConditionFalse,
			Reason:             reasonNoProtectedNamespaces,
			Message:            "no matched namespaces are protected",
			ObservedGeneration: r.Policy.Generation,
		})
	}

	readyChanged := r.setReady(metav1.ConditionTrue, reasonValidated, "spec.ttl and spec.mode are valid")

	if err := r.writeStatusIfChanged(ctx, skippedChanged || readyChanged); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// Delete is a no-op: deleting a PodSweepPolicy must never cascade into deleting pods, and
// ValidationSubReconciler owns no external state that needs cleanup.
func (r *ValidationSubReconciler) Delete(_ context.Context) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (r *ValidationSubReconciler) setReady(status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&r.Policy.Status.Conditions, metav1.Condition{
		Type:               sweeperv1alpha1.ConditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: r.Policy.Generation,
	})
}

// writeStatusIfChanged writes status only if changed is true or the object's generation has
// moved on since the last observed status, so a reconcile pass that leaves conditions
// semantically unchanged doesn't bump resourceVersion and re-trigger the watch that invoked it.
func (r *ValidationSubReconciler) writeStatusIfChanged(ctx context.Context, changed bool) error {
	if !changed && r.Policy.Status.ObservedGeneration == r.Policy.Generation {
		return nil
	}
	return r.updateStatus(ctx)
}

func (r *ValidationSubReconciler) updateStatus(ctx context.Context) error {
	r.Policy.Status.ObservedGeneration = r.Policy.Generation
	return r.Client.Status().Update(ctx, r.Policy)
}
