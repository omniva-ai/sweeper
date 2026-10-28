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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
	"github.com/omniva-ai/sweeper/internal/metrics"
)

func trackedPod(name string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: types.UID(name + "-uid")},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func seenAt(t *testing.T, tracker *firstSeenTracker, uid types.UID) (time.Time, bool) {
	t.Helper()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	e, ok := tracker.pods[uid]
	return e.at, ok
}

func TestFirstSeenTracker_Observe(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	tracker := newFirstSeenTracker()
	a := trackedPod("a", corev1.PodFailed)
	b := trackedPod("b", corev1.PodFailed)

	if got := tracker.observe(a, t0); !got.Equal(t0) {
		t.Errorf("first observe = %v, want %v", got, t0)
	}
	if got := tracker.observe(a, t0.Add(time.Hour)); !got.Equal(t0) {
		t.Errorf("later observe = %v, want the first sighting %v", got, t0)
	}
	if got := tracker.observe(b, t0.Add(2*time.Hour)); !got.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("observe of another UID = %v, want %v", got, t0.Add(2*time.Hour))
	}
}

func TestFirstSeenTracker_NewTrackerRestartsClock(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(3 * time.Hour)
	pod := trackedPod("a", corev1.PodFailed)

	newFirstSeenTracker().observe(pod, t0)
	if got := newFirstSeenTracker().observe(pod, t1); !got.Equal(t1) {
		t.Errorf("observe on a new tracker = %v, want %v", got, t1)
	}
}

func TestFirstSeenTracker_Prune(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}

	replaced := trackedPod("a", corev1.PodFailed)
	replaced.UID = "other-uid"
	terminating := trackedPod("a", corev1.PodFailed)
	terminating.Finalizers = []string{"example.com/hold"}
	terminating.DeletionTimestamp = ptrTime(t0)

	tests := map[string]struct {
		stored   *corev1.Pod
		wantKept bool
	}{
		"pod deleted":              {stored: nil, wantKept: false},
		"uid replaced":             {stored: replaced, wantKept: false},
		"phase no longer Failed":   {stored: trackedPod("a", corev1.PodRunning), wantKept: false},
		"deletion timestamp set":   {stored: terminating, wantKept: false},
		"pod still Failed is kept": {stored: trackedPod("a", corev1.PodFailed), wantKept: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{ns}
			if tc.stored != nil {
				objs = append(objs, tc.stored)
			}
			c := newFakeClientForEval(t, objs...)
			tracker := newFirstSeenTracker()
			tracker.observe(trackedPod("a", corev1.PodFailed), t0)

			if err := tracker.prune(context.Background(), c); err != nil {
				t.Fatalf("prune() error = %v", err)
			}
			if _, kept := seenAt(t, tracker, "a-uid"); kept != tc.wantKept {
				t.Errorf("entry kept = %v, want %v", kept, tc.wantKept)
			}
		})
	}

	t.Run("get error keeps the entry and returns the error", func(t *testing.T) {
		c := newInterceptedClientForEval(t, interceptor.Funcs{
			Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
				return fmt.Errorf("injected Get failure for %s", key)
			},
		}, ns)
		tracker := newFirstSeenTracker()
		tracker.observe(trackedPod("a", corev1.PodFailed), t0)

		if err := tracker.prune(context.Background(), c); err == nil {
			t.Fatal("prune() error = nil, want an error")
		}
		if at, kept := seenAt(t, tracker, "a-uid"); !kept || !at.Equal(t0) {
			t.Errorf("entry = %v (kept %v), want %v kept", at, kept, t0)
		}
	})
}

func chainPolicy(name, mode string, podSel *metav1.LabelSelector) *sweeperv1alpha1.PodSweepPolicy {
	return &sweeperv1alpha1.PodSweepPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Finalizers: []string{finalizerName}},
		Spec: sweeperv1alpha1.PodSweepPolicySpec{
			TTL:         metav1.Duration{Duration: time.Hour},
			Mode:        mode,
			PodSelector: podSel,
		},
	}
}

func TestPodSweepPolicyReconciler_SharesFirstSeenTracker(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	shared := trackedPod("shared", corev1.PodFailed)
	onlyB := trackedPod("only-b", corev1.PodFailed)
	onlyB.Labels = map[string]string{tierLabelKey: tierValueBatch}
	policyA := chainPolicy("policy-a", sweeperv1alpha1.ModeReport,
		&metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{
			{Key: tierLabelKey, Operator: metav1.LabelSelectorOpDoesNotExist},
		}})
	policyB := chainPolicy("policy-b", sweeperv1alpha1.ModeReport, nil)
	t.Cleanup(func() {
		metrics.DeletePolicy(policyA.Name)
		metrics.DeletePolicy(policyB.Name)
	})
	c := newFakeClientForEval(t, ns, shared, onlyB, policyA, policyB)
	r := &PodSweepPolicyReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: policyA.Name}}); err != nil {
		t.Fatalf("Reconcile(policy-a) error = %v", err)
	}
	first, ok := seenAt(t, r.firstSeenTracker(), shared.UID)
	if !ok {
		t.Fatal("policy-a did not record the shared pod in the reconciler's tracker")
	}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: policyB.Name}}); err != nil {
		t.Fatalf("Reconcile(policy-b) error = %v", err)
	}
	if got, _ := seenAt(t, r.firstSeenTracker(), shared.UID); !got.Equal(first) {
		t.Errorf("shared pod first seen = %v after policy-b, want policy-a's %v", got, first)
	}
	if _, ok := seenAt(t, r.firstSeenTracker(), onlyB.UID); !ok {
		t.Error("policy-b did not record its pod in the reconciler's tracker")
	}
}

func TestPodSweepPolicyReconciler_PrunesWhenPolicyDisabled(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	policy := chainPolicy("disabled", sweeperv1alpha1.ModeDisable, nil)
	t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
	c := newFakeClientForEval(t, ns, policy)
	r := &PodSweepPolicyReconciler{Client: c, Recorder: record.NewFakeRecorder(10)}
	gone := trackedPod("gone", corev1.PodFailed)
	r.firstSeenTracker().observe(gone, time.Now())

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: policy.Name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if _, ok := seenAt(t, r.firstSeenTracker(), gone.UID); ok {
		t.Error("entry for a pod that no longer exists was not pruned")
	}
}
