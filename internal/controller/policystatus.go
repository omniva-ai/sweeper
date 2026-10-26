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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
)

// setCondition sets condType on policy with ObservedGeneration = policy.Generation. It returns
// true when the condition's status, reason, message, or observed generation changed.
func setCondition(policy *sweeperv1alpha1.PodSweepPolicy, condType string,
	status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: policy.Generation,
	})
}

// writeStatusIfChanged writes status only if changed is true or the object's generation has
// moved on since the last observed status, so a reconcile pass that leaves conditions
// semantically unchanged doesn't bump resourceVersion and re-trigger the watch that invoked it.
// It sets Status.ObservedGeneration before the write.
func writeStatusIfChanged(ctx context.Context, cl client.Client,
	policy *sweeperv1alpha1.PodSweepPolicy, changed bool) error {
	if !changed && policy.Status.ObservedGeneration == policy.Generation {
		return nil
	}
	policy.Status.ObservedGeneration = policy.Generation
	return cl.Status().Update(ctx, policy)
}

// setFailureCondition sets condType to False with reason and message and records a Warning
// event only when the condition's status or reason transitioned. A message or observed
// generation change alone updates the condition without a new event, so a persistent failure
// produces one event however many passes or spec edits repeat it. It returns true when the
// condition changed in any way, for the status write.
func setFailureCondition(recorder record.EventRecorder, policy *sweeperv1alpha1.PodSweepPolicy,
	condType, reason, message string) bool {
	prev := meta.FindStatusCondition(policy.Status.Conditions, condType)
	transitioned := prev == nil || prev.Status != metav1.ConditionFalse || prev.Reason != reason
	changed := setCondition(policy, condType, metav1.ConditionFalse, reason, message)
	recordWarningIfChanged(recorder, policy, transitioned, reason, message)
	return changed
}

// writeFailure writes status after a failure when changed is true and returns cause joined with
// any status error, so every failure path returns the same shape and the object is requeued.
func writeFailure(ctx context.Context, cl client.Client, policy *sweeperv1alpha1.PodSweepPolicy,
	changed bool, cause error) error {
	if statusErr := writeStatusIfChanged(ctx, cl, policy, changed); statusErr != nil {
		return errors.Join(cause, statusErr)
	}
	return cause
}

// recordWarningIfChanged records a Warning event with reason and message only when changed is
// true, so a condition that stays in the same state across passes produces one event.
func recordWarningIfChanged(recorder record.EventRecorder,
	policy *sweeperv1alpha1.PodSweepPolicy, changed bool, reason, message string) {
	if changed {
		recorder.Event(policy, corev1.EventTypeWarning, reason, message)
	}
}
