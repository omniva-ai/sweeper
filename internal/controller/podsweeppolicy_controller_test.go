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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
	"github.com/omniva-ai/sweeper/internal/metrics"
)

var _ = Describe("PodSweepPolicyReconciler", func() {
	var (
		ctx context.Context
		req ctrl.Request
	)

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("no-ops when the PodSweepPolicy no longer exists", func() {
		reconciler := &PodSweepPolicyReconciler{
			Client:   newValidationTestClient(),
			Recorder: record.NewFakeRecorder(1),
		}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: "does-not-exist"}}

		result, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
	})

	It("calls Delete instead of Reconcile on the sub-reconciler chain once a deletion timestamp is set", func() {
		now := metav1.Now()
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "being-deleted",
				Finalizers:        []string{finalizerName},
				DeletionTimestamp: &now,
			},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Hour}},
		}
		cl := newValidationTestClient(policy)

		reconciler := &PodSweepPolicyReconciler{
			Client:              cl,
			Recorder:            record.NewFakeRecorder(1),
			ProtectedNamespaces: []string{testNamespaceKubeSystem},
		}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}

		result, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		var fresh sweeperv1alpha1.PodSweepPolicy
		err = cl.Get(ctx, types.NamespacedName{Name: policy.Name}, &fresh)
		// ValidationSubReconciler.Delete is a no-op: if Reconcile (not Delete) had been called
		// instead, it would have set the Ready condition. The finalizer being removed (and the
		// object thus gone from the fake client) proves the delete branch ran to completion.
		Expect(errors.IsNotFound(err)).To(BeTrue(), "expected the finalizer to be removed and the object deleted, got: %v / %+v", err, fresh)
	})

	It("adds the finalizer on first reconcile of a new policy, before running any sub-reconciler", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "fresh-policy"},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Hour}},
		}
		cl := newValidationTestClient(policy)

		reconciler := &PodSweepPolicyReconciler{Client: cl, Recorder: record.NewFakeRecorder(1)}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}

		result, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		var fresh sweeperv1alpha1.PodSweepPolicy
		Expect(cl.Get(ctx, types.NamespacedName{Name: policy.Name}, &fresh)).To(Succeed())
		Expect(fresh.Finalizers).To(ContainElement(finalizerName))
		// Validation hasn't run yet on this pass (it only adds the finalizer), so no Ready
		// condition should be set until the next reconcile.
		Expect(fresh.Status.Conditions).To(BeEmpty())
	})

	It("removes the finalizer and lets the object be deleted after a full lifecycle", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-policy"},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Hour}},
		}
		cl := newValidationTestClient(policy)
		reconciler := &PodSweepPolicyReconciler{Client: cl, Recorder: record.NewFakeRecorder(1)}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}
		DeferCleanup(func() { metrics.DeletePolicy(policy.Name) })

		// First reconcile adds the finalizer.
		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		metrics.SetCurrent(testNamespace, policy.Name, 3)

		Expect(cl.Delete(ctx, policy)).To(Succeed())

		var deleting sweeperv1alpha1.PodSweepPolicy
		Expect(cl.Get(ctx, types.NamespacedName{Name: policy.Name}, &deleting)).To(Succeed())
		Expect(deleting.DeletionTimestamp).NotTo(BeNil())
		Expect(deleting.Finalizers).To(ContainElement(finalizerName))

		_, err = reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Get(ctx, types.NamespacedName{Name: policy.Name}, &sweeperv1alpha1.PodSweepPolicy{})
		Expect(errors.IsNotFound(err)).To(BeTrue(), "expected object to be gone once the finalizer is removed, got: %v", err)

		gauge := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name))
		Expect(gauge).To(BeZero(), "expected EvaluationSubReconciler.Delete to purge metrics for the deleted policy")
	})

	It("chains Validation into Evaluation to delete a pod past TTL in enforce mode", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "enforce-policy", Finalizers: []string{finalizerName}},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				TTL:  metav1.Duration{Duration: time.Minute},
				Mode: sweeperv1alpha1.ModeEnforce,
			},
		}
		DeferCleanup(func() { metrics.DeletePolicy(policy.Name) })
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: testNamespace},
			Status: corev1.PodStatus{
				Phase:     corev1.PodFailed,
				StartTime: ptrTime(time.Now().Add(-time.Hour)),
			},
		}
		cl := newValidationTestClient(policy, ns, pod)

		reconciler := &PodSweepPolicyReconciler{Client: cl, Recorder: record.NewFakeRecorder(1)}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		err = cl.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &corev1.Pod{})
		Expect(err).To(HaveOccurred())
	})

	It("chains Validation into Evaluation but never deletes in report mode", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "report-policy", Finalizers: []string{finalizerName}},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				TTL:  metav1.Duration{Duration: time.Minute},
				Mode: sweeperv1alpha1.ModeReport,
			},
		}
		DeferCleanup(func() { metrics.DeletePolicy(policy.Name) })
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: testNamespace},
			Status: corev1.PodStatus{
				Phase:     corev1.PodFailed,
				StartTime: ptrTime(time.Now().Add(-time.Hour)),
			},
		}
		cl := newValidationTestClient(policy, ns, pod)

		reconciler := &PodSweepPolicyReconciler{Client: cl, Recorder: record.NewFakeRecorder(1)}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(cl.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, &corev1.Pod{})).To(Succeed())

		var fresh sweeperv1alpha1.PodSweepPolicy
		Expect(cl.Get(ctx, client.ObjectKeyFromObject(policy), &fresh)).To(Succeed())
		expectCondition(&fresh, sweeperv1alpha1.ConditionTypeReady, metav1.ConditionTrue, reasonValidated)
		expectCondition(&fresh, sweeperv1alpha1.ConditionTypeEvaluated, metav1.ConditionTrue, reasonEvaluated)
	})

	It("flips a stale Ready=True to False when spec.namespaceSelector is invalid", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "stale-chain", Finalizers: []string{finalizerName}, Generation: 2},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				TTL:               metav1.Duration{Duration: time.Minute},
				NamespaceSelector: badOperatorSelector(),
			},
			Status: sweeperv1alpha1.PodSweepPolicyStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{
					{Type: sweeperv1alpha1.ConditionTypeReady, Status: metav1.ConditionTrue,
						Reason: reasonValidated, ObservedGeneration: 1, LastTransitionTime: metav1.Now()},
					{Type: sweeperv1alpha1.ConditionTypeEvaluated, Status: metav1.ConditionTrue,
						Reason: reasonEvaluated, ObservedGeneration: 1, LastTransitionTime: metav1.Now()},
				},
			},
		}
		cl := newValidationTestClient(policy)

		reconciler := &PodSweepPolicyReconciler{Client: cl, Recorder: record.NewFakeRecorder(10)}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).To(HaveOccurred())

		var fresh sweeperv1alpha1.PodSweepPolicy
		Expect(cl.Get(ctx, client.ObjectKeyFromObject(policy), &fresh)).To(Succeed())
		expectCondition(&fresh, sweeperv1alpha1.ConditionTypeReady, metav1.ConditionFalse, reasonInvalidNamespaceSelector)
		expectCondition(&fresh, sweeperv1alpha1.ConditionTypeEvaluated, metav1.ConditionFalse, reasonNotReady)
		Expect(fresh.Status.ObservedGeneration).To(BeEquivalentTo(2))
	})

	It("sets Ready=True/Disabled and Evaluated=False/Disabled without an event in disable mode", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "disabled-chain", Finalizers: []string{finalizerName}},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				TTL:  metav1.Duration{Duration: time.Minute},
				Mode: sweeperv1alpha1.ModeDisable,
			},
		}
		DeferCleanup(func() { metrics.DeletePolicy(policy.Name) })
		cl := newValidationTestClient(policy)
		recorder := record.NewFakeRecorder(10)

		reconciler := &PodSweepPolicyReconciler{Client: cl, Recorder: recorder}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		var fresh sweeperv1alpha1.PodSweepPolicy
		Expect(cl.Get(ctx, client.ObjectKeyFromObject(policy), &fresh)).To(Succeed())
		expectCondition(&fresh, sweeperv1alpha1.ConditionTypeReady, metav1.ConditionTrue, reasonDisabled)
		expectCondition(&fresh, sweeperv1alpha1.ConditionTypeEvaluated, metav1.ConditionFalse, reasonDisabled)
		Consistently(recorder.Events).ShouldNot(Receive())
	})

	It("persists Evaluated=False/PodListFailed through the full chain when listing pods fails", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "podlist-chain", Finalizers: []string{finalizerName}},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Minute}},
		}
		DeferCleanup(func() { metrics.DeletePolicy(policy.Name) })
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		cl := &listErrorForClient{Client: newValidationTestClient(policy, ns), failFor: &corev1.PodList{}}
		recorder := record.NewFakeRecorder(10)

		reconciler := &PodSweepPolicyReconciler{Client: cl, Recorder: recorder}
		req = ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}

		_, err := reconciler.Reconcile(ctx, req)
		Expect(err).To(HaveOccurred())

		var fresh sweeperv1alpha1.PodSweepPolicy
		Expect(cl.Get(ctx, client.ObjectKeyFromObject(policy), &fresh)).To(Succeed())
		expectCondition(&fresh, sweeperv1alpha1.ConditionTypeReady, metav1.ConditionTrue, reasonValidated)
		expectCondition(&fresh, sweeperv1alpha1.ConditionTypeEvaluated, metav1.ConditionFalse, reasonPodListFailed)
		Expect(recorder.Events).To(Receive(ContainSubstring(reasonPodListFailed)))
	})
})

// expectCondition asserts one condition's status and reason on a policy read back from the
// client, so the specs above prove the status write, not only the in-memory object.
func expectCondition(policy *sweeperv1alpha1.PodSweepPolicy, condType string, status metav1.ConditionStatus, reason string) {
	GinkgoHelper()
	cond := meta.FindStatusCondition(policy.Status.Conditions, condType)
	Expect(cond).NotTo(BeNil(), "condition %s is missing", condType)
	Expect(cond.Status).To(Equal(status))
	Expect(cond.Reason).To(Equal(reason))
}
