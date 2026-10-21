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
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
)

// listErrorClient fails every List call, to prove a code path never reaches the namespace gate.
type listErrorClient struct {
	client.Client
}

func (c *listErrorClient) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return fmt.Errorf("unexpected List call")
}

// countingStatusClient counts Status().Update calls, to prove a code path skips redundant
// status writes.
type countingStatusClient struct {
	client.Client
	statusUpdates int
}

func (c *countingStatusClient) Status() client.SubResourceWriter {
	return &countingStatusWriter{SubResourceWriter: c.Client.Status(), count: &c.statusUpdates}
}

type countingStatusWriter struct {
	client.SubResourceWriter
	count *int
}

func (w *countingStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	*w.count++
	return w.SubResourceWriter.Update(ctx, obj, opts...)
}

// newValidationTestClient builds a fake client for testing ValidationSubReconciler in
// isolation, independent of the CRD's admission-time enum/required validation (which envtest
// would enforce and which would prevent constructing some of the fail-fast scenarios below).
func newValidationTestClient(objs ...client.Object) client.Client {
	sch := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(sch)).To(Succeed())
	Expect(sweeperv1alpha1.AddToScheme(sch)).To(Succeed())

	return fake.NewClientBuilder().
		WithScheme(sch).
		WithStatusSubresource(&sweeperv1alpha1.PodSweepPolicy{}).
		WithObjects(objs...).
		Build()
}

func readyCondition(policy *sweeperv1alpha1.PodSweepPolicy) *metav1.Condition {
	return meta.FindStatusCondition(policy.Status.Conditions, sweeperv1alpha1.ConditionTypeReady)
}

func skippedCondition(policy *sweeperv1alpha1.PodSweepPolicy) *metav1.Condition {
	return meta.FindStatusCondition(policy.Status.Conditions, sweeperv1alpha1.ConditionTypeNamespacesSkippedProtected)
}

var _ = Describe("ValidationSubReconciler", func() {
	var (
		ctx      context.Context
		recorder *record.FakeRecorder
	)

	BeforeEach(func() {
		ctx = context.Background()
		recorder = record.NewFakeRecorder(10)
	})

	It("resolves an empty mode to report and sets Ready=True", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "valid"},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Hour}},
		}
		cl := newValidationTestClient(policy)

		result, err := NewValidationSubReconciler(cl, recorder, policy, sets.New[string](), sets.New[string]()).Reconcile(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		ready := readyCondition(policy)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.Reason).To(Equal(reasonValidated))
	})

	It("fails fast on an unknown mode and sets Ready=False", func() {
		// spec.mode's kubebuilder enum would reject this value at real admission; this
		// exercises ValidationSubReconciler's own defensive check directly.
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-mode"},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				TTL:  metav1.Duration{Duration: time.Hour},
				Mode: "bogus",
			},
		}
		cl := newValidationTestClient(policy)

		_, err := NewValidationSubReconciler(cl, recorder, policy, sets.New[string](), sets.New[string]()).Reconcile(ctx)
		Expect(err).To(HaveOccurred())

		ready := readyCondition(policy)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(reasonInvalidMode))
	})

	It("fails fast on a non-positive ttl and sets Ready=False", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-ttl"},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: 0}},
		}
		cl := newValidationTestClient(policy)

		_, err := NewValidationSubReconciler(cl, recorder, policy, sets.New[string](), sets.New[string]()).Reconcile(ctx)
		Expect(err).To(HaveOccurred())

		ready := readyCondition(policy)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal(reasonInvalidTTL))
	})

	It("skips a protected namespace, sets the condition, and records a Warning event", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "protected"},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Hour}},
		}
		kubeSystem := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespaceKubeSystem}}
		cl := newValidationTestClient(policy, kubeSystem)

		_, err := NewValidationSubReconciler(cl, recorder, policy, sets.New(testNamespaceKubeSystem), sets.New[string]()).Reconcile(ctx)
		Expect(err).NotTo(HaveOccurred())

		skipped := skippedCondition(policy)
		Expect(skipped).NotTo(BeNil())
		Expect(skipped.Status).To(Equal(metav1.ConditionTrue))
		Expect(skipped.Reason).To(Equal(reasonProtectedNamespacesExcluded))
		Expect(skipped.Message).To(ContainSubstring(testNamespaceKubeSystem))

		Eventually(recorder.Events).Should(Receive(ContainSubstring(testNamespaceKubeSystem)))
	})

	It("unlocks a protected namespace via the allowed set and sets the condition False", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "allowed"},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Hour}},
		}
		kubeSystem := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespaceKubeSystem}}
		cl := newValidationTestClient(policy, kubeSystem)

		_, err := NewValidationSubReconciler(cl, recorder, policy, sets.New(testNamespaceKubeSystem), sets.New(testNamespaceKubeSystem)).Reconcile(ctx)
		Expect(err).NotTo(HaveOccurred())

		skipped := skippedCondition(policy)
		Expect(skipped).NotTo(BeNil())
		Expect(skipped.Status).To(Equal(metav1.ConditionFalse))
		Expect(skipped.Reason).To(Equal(reasonNoProtectedNamespaces))
	})

	It("skips the namespace gate entirely for a disabled rule", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "disabled"},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				TTL:  metav1.Duration{Duration: time.Hour},
				Mode: sweeperv1alpha1.ModeDisable,
			},
		}
		cl := &listErrorClient{Client: newValidationTestClient(policy)}

		_, err := NewValidationSubReconciler(cl, recorder, policy, sets.New(testNamespaceKubeSystem), sets.New[string]()).Reconcile(ctx)
		Expect(err).NotTo(HaveOccurred(), "Reconcile must never call List for a disabled rule")

		ready := readyCondition(policy)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Status).To(Equal(metav1.ConditionTrue))
		Expect(ready.Reason).To(Equal(reasonDisabled))

		skipped := skippedCondition(policy)
		Expect(skipped).NotTo(BeNil())
		Expect(skipped.Status).To(Equal(metav1.ConditionFalse))
		Expect(skipped.Reason).To(Equal(reasonDisabled))

		Consistently(recorder.Events).ShouldNot(Receive())
	})

	It("does not re-write status or re-record the event on an unchanged subsequent reconcile", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "stable"},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Hour}},
		}
		kubeSystem := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespaceKubeSystem}}
		cl := &countingStatusClient{Client: newValidationTestClient(policy, kubeSystem)}

		sub := NewValidationSubReconciler(cl, recorder, policy, sets.New(testNamespaceKubeSystem), sets.New[string]())

		_, err := sub.Reconcile(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(cl.statusUpdates).To(Equal(1))
		Eventually(recorder.Events).Should(Receive(ContainSubstring(testNamespaceKubeSystem)))

		_, err = sub.Reconcile(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(cl.statusUpdates).To(Equal(1), "an unchanged reconcile must not write status again")
		Consistently(recorder.Events).ShouldNot(Receive(), "an unchanged reconcile must not re-record the event")
	})

	It("Delete is a no-op", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "delete-noop"},
			Spec:       sweeperv1alpha1.PodSweepPolicySpec{TTL: metav1.Duration{Duration: time.Hour}},
		}
		cl := newValidationTestClient(policy)

		result, err := NewValidationSubReconciler(cl, recorder, policy, sets.New[string](), sets.New[string]()).Delete(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))

		var fresh sweeperv1alpha1.PodSweepPolicy
		Expect(cl.Get(ctx, client.ObjectKeyFromObject(policy), &fresh)).To(Succeed())
		Expect(fresh.Status.Conditions).To(BeEmpty())
	})
})

var _ = Describe("resolveNamespaceGate against a real API server", func() {
	It("matches default and skips the protected kube-system namespace, both of which always exist in envtest", func() {
		matched, skipped, err := resolveNamespaceGate(context.Background(), k8sClient, nil,
			sets.New(testNamespaceKubeSystem, testNamespacePlatformSystem), sets.New[string]())
		Expect(err).NotTo(HaveOccurred())
		Expect(matched).To(ContainElement(corev1.NamespaceDefault))
		Expect(skipped).To(ContainElement(testNamespaceKubeSystem))
	})
})
