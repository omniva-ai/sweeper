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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
)

const (
	reasonValidated                   = "Validated"
	reasonInvalidMode                 = "InvalidMode"
	reasonInvalidTTL                  = "InvalidTTL"
	reasonInvalidNamespaceSelector    = "InvalidNamespaceSelector"
	reasonInvalidPodSelector          = "InvalidPodSelector"
	reasonNamespaceListFailed         = "NamespaceListFailed"
	reasonNotReady                    = "NotReady"
	reasonProtectedNamespacesExcluded = "ProtectedNamespacesExcluded"
	reasonNoProtectedNamespaces       = "NoProtectedNamespaces"
	reasonDisabled                    = "Disabled"
)

// errStopChain tells the main reconciler to stop the sub-reconciler chain and return no error.
// A sub-reconciler returns it for a failure that only a spec edit can fix: the spec edit triggers
// a new reconcile, so a retry with backoff only adds error logs and error metrics.
var errStopChain = errors.New("stop the sub-reconciler chain")

// ValidationSubReconciler validates spec.ttl, spec.mode, spec.namespaceSelector, and
// spec.podSelector, applies the operator-level protected-namespace gate to spec.namespaceSelector,
// and sets status.conditions accordingly. It implements reconciler.Reconciler.
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

// Reconcile validates spec.ttl, spec.mode, and both selectors, applies the protected-namespace
// gate, and sets status.conditions. The CRD schema already rejects a malformed or non-positive
// ttl, an unknown mode, and a bad selector operator at admission; the checks here are defense in
// depth for objects stored before those rules existed, and for selector errors CEL cannot see
// (such as an invalid label value). Every failure path goes through failReady or
// failReadyPermanent, so the object's status reflects a clear validation failure rather than
// keeping a stale Ready=True.
func (r *ValidationSubReconciler) Reconcile(ctx context.Context) (ctrl.Result, error) {
	mode := r.Policy.Spec.Mode
	if mode == "" {
		mode = sweeperv1alpha1.ModeReport
	}
	if mode != sweeperv1alpha1.ModeEnforce && mode != sweeperv1alpha1.ModeReport && mode != sweeperv1alpha1.ModeDisable {
		err := fmt.Errorf("invalid mode %q: must be one of %s, %s, %s", r.Policy.Spec.Mode,
			sweeperv1alpha1.ModeEnforce, sweeperv1alpha1.ModeReport, sweeperv1alpha1.ModeDisable)
		return r.failReadyPermanent(ctx, reasonInvalidMode, err)
	}

	if r.Policy.Spec.TTL.Duration <= 0 {
		err := fmt.Errorf("invalid ttl %q: must be greater than zero", r.Policy.Spec.TTL.Duration)
		return r.failReadyPermanent(ctx, reasonInvalidTTL, err)
	}

	// Both selectors are parsed before the disable early return, like ttl and mode, so a disabled
	// rule with a bad selector reports it before the mode changes. The parsed values are not kept:
	// resolveNamespaceGate and EvaluationSubReconciler parse again where they use them.
	if _, err := asSelector(r.Policy.Spec.NamespaceSelector); err != nil {
		return r.failReadyPermanent(ctx, reasonInvalidNamespaceSelector, fmt.Errorf("invalid spec.namespaceSelector: %w", err))
	}
	if _, err := asSelector(r.Policy.Spec.PodSelector); err != nil {
		return r.failReadyPermanent(ctx, reasonInvalidPodSelector, fmt.Errorf("invalid spec.podSelector: %w", err))
	}

	// disable means "not evaluated at all" (docs/ARCHITECTURE.md): no namespace listing, no
	// event, as if the rule didn't exist. Must be checked before resolveNamespaceGate.
	if mode == sweeperv1alpha1.ModeDisable {
		skippedChanged := setCondition(r.Policy, sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
			metav1.ConditionFalse, reasonDisabled, "rule is disabled; namespaces are not evaluated")
		readyChanged := setCondition(r.Policy, sweeperv1alpha1.ConditionTypeReady,
			metav1.ConditionTrue, reasonDisabled, "mode is disable; rule is not evaluated")
		return ctrl.Result{}, writeStatusIfChanged(ctx, r.Client, r.Policy, skippedChanged || readyChanged)
	}

	_, skipped, err := resolveNamespaceGate(ctx, r.Client, r.Policy.Spec.NamespaceSelector,
		r.ProtectedNamespaces, r.AllowedNamespaces)
	if err != nil {
		return r.failReady(ctx, reasonNamespaceListFailed, fmt.Errorf("listing namespaces: %w", err))
	}

	var skippedChanged bool
	if len(skipped) > 0 {
		message := fmt.Sprintf("namespaces excluded as protected and not explicitly allowed: %s",
			strings.Join(skipped, ", "))
		skippedChanged = setCondition(r.Policy, sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
			metav1.ConditionTrue, reasonProtectedNamespacesExcluded, message)
		recordWarningIfChanged(r.Recorder, r.Policy, skippedChanged, reasonProtectedNamespacesExcluded, message)
	} else {
		skippedChanged = setCondition(r.Policy, sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected,
			metav1.ConditionFalse, reasonNoProtectedNamespaces, "no matched namespaces are protected")
	}

	readyChanged := setCondition(r.Policy, sweeperv1alpha1.ConditionTypeReady,
		metav1.ConditionTrue, reasonValidated, "spec.ttl, spec.mode, spec.namespaceSelector and spec.podSelector are valid")

	if err := writeStatusIfChanged(ctx, r.Client, r.Policy, skippedChanged || readyChanged); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// Delete is a no-op: deleting a PodSweepPolicy must never cascade into deleting pods, and
// ValidationSubReconciler owns no external state that needs cleanup.
func (r *ValidationSubReconciler) Delete(_ context.Context) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// failReady sets the conditions of setNotReady for a transient failure, writes status, and
// returns cause joined with any status error so the object is requeued.
func (r *ValidationSubReconciler) failReady(ctx context.Context, reason string, cause error) (ctrl.Result, error) {
	changed := r.setNotReady(reason, cause)
	return ctrl.Result{}, writeFailure(ctx, r.Client, r.Policy, changed, cause)
}

// failReadyPermanent sets the conditions of setNotReady for a failure that only a spec edit can
// fix, writes status, and returns errStopChain so the main reconciler stops the chain without an
// error. If the status write fails, it returns only the status error, so the object is requeued
// until the condition is persisted.
func (r *ValidationSubReconciler) failReadyPermanent(ctx context.Context, reason string, cause error) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("spec is not valid; waiting for a spec change", "reason", reason, "message", cause.Error())
	changed := r.setNotReady(reason, cause)
	if statusErr := writeStatusIfChanged(ctx, r.Client, r.Policy, changed); statusErr != nil {
		return ctrl.Result{}, statusErr
	}
	return ctrl.Result{}, errStopChain
}

// setNotReady sets Ready=False with reason and cause's message, records a Warning when Ready
// transitioned to this failure, and sets Evaluated=False/NotReady because evaluation does not
// run while Ready is False. It returns true when either condition changed.
func (r *ValidationSubReconciler) setNotReady(reason string, cause error) bool {
	readyChanged := setFailureCondition(r.Recorder, r.Policy, sweeperv1alpha1.ConditionTypeReady, reason, cause.Error())
	evaluatedChanged := setCondition(r.Policy, sweeperv1alpha1.ConditionTypeEvaluated,
		metav1.ConditionFalse, reasonNotReady, "rule is not evaluated while Ready is False")
	return readyChanged || evaluatedChanged
}
