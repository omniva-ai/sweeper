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
	mathrand "math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
	"github.com/omniva-ai/sweeper/internal/jitter"
	"github.com/omniva-ai/sweeper/internal/metrics"
)

// newFakeClientForEval builds a fake client for testing EvaluationSubReconciler with plain
// testing.T tests. It must not use Gomega's Expect: that requires Ginkgo's fail handler to
// already be registered, which only happens once the Ginkgo suite (suite_test.go) runs, and
// plain testing.T tests in this package can run before it does.
func newFakeClientForEval(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return newFakeClientBuilderForEval(t, objs...).Build()
}

// newInterceptedClientForEval is newFakeClientForEval with funcs wrapped around the fake client.
func newInterceptedClientForEval(t *testing.T, funcs interceptor.Funcs, objs ...client.Object) client.Client {
	t.Helper()
	return newFakeClientBuilderForEval(t, objs...).WithInterceptorFuncs(funcs).Build()
}

func newFakeClientBuilderForEval(t *testing.T, objs ...client.Object) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding corev1 to scheme: %v", err)
	}
	if err := sweeperv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding sweeperv1alpha1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sweeperv1alpha1.PodSweepPolicy{}).
		WithObjects(objs...)
}

func TestFailedSince(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	started := created.Add(1 * time.Minute)
	finishedEarly := started.Add(2 * time.Minute)
	finishedLate := started.Add(5 * time.Minute)

	tests := map[string]struct {
		pod  corev1.Pod
		want time.Time
	}{
		"single terminated container uses finishedAt": {
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
				Status: corev1.PodStatus{
					StartTime: ptrTime(started),
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							FinishedAt: metav1.NewTime(finishedEarly),
						}}},
					},
				},
			},
			want: finishedEarly,
		},
		"multiple terminated containers use max finishedAt": {
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
				Status: corev1.PodStatus{
					StartTime: ptrTime(started),
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							FinishedAt: metav1.NewTime(finishedEarly),
						}}},
						{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							FinishedAt: metav1.NewTime(finishedLate),
						}}},
					},
				},
			},
			want: finishedLate,
		},
		"no terminated containers falls back to StartTime": {
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
				Status: corev1.PodStatus{
					StartTime: ptrTime(started),
					ContainerStatuses: []corev1.ContainerStatus{
						{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}},
					},
				},
			},
			want: started,
		},
		"no StartTime falls back to CreationTimestamp": {
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
				Status:     corev1.PodStatus{},
			},
			want: created,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := failedSince(&tc.pod)
			if !got.Equal(tc.want) {
				t.Errorf("failedSince() = %v, want %v", got, tc.want)
			}
		})
	}
}

func ptrTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}

func podFailedAt(t time.Time) corev1.Pod {
	return corev1.Pod{Status: corev1.PodStatus{StartTime: ptrTime(t)}}
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Minute
	defaultPollInterval := 5 * time.Minute

	t.Run("pod past TTL is eligible", func(t *testing.T) {
		pods := []corev1.Pod{podFailedAt(now.Add(-11 * time.Minute))}
		eligible, _ := evaluate(pods, ttl, now, defaultPollInterval)
		if len(eligible) != 1 {
			t.Fatalf("eligible = %d pods, want 1", len(eligible))
		}
	})

	t.Run("pod not yet past TTL is not eligible", func(t *testing.T) {
		pods := []corev1.Pod{podFailedAt(now.Add(-5 * time.Minute))}
		eligible, _ := evaluate(pods, ttl, now, defaultPollInterval)
		if len(eligible) != 0 {
			t.Fatalf("eligible = %d pods, want 0", len(eligible))
		}
	})

	t.Run("requeue is time until nearest TTL expiry when sooner than default poll", func(t *testing.T) {
		pods := []corev1.Pod{podFailedAt(now.Add(-8 * time.Minute))} // 2m until eligible
		_, requeueAfter := evaluate(pods, ttl, now, defaultPollInterval)
		if requeueAfter != 2*time.Minute {
			t.Errorf("requeueAfter = %v, want %v", requeueAfter, 2*time.Minute)
		}
	})

	t.Run("requeue falls back to default poll interval when no pod expires sooner", func(t *testing.T) {
		pods := []corev1.Pod{podFailedAt(now.Add(-1 * time.Minute))} // 9m until eligible, past defaultPollInterval
		_, requeueAfter := evaluate(pods, ttl, now, defaultPollInterval)
		if requeueAfter != defaultPollInterval {
			t.Errorf("requeueAfter = %v, want %v", requeueAfter, defaultPollInterval)
		}
	})

	t.Run("requeue with no pods falls back to default poll interval", func(t *testing.T) {
		_, requeueAfter := evaluate(nil, ttl, now, defaultPollInterval)
		if requeueAfter != defaultPollInterval {
			t.Errorf("requeueAfter = %v, want %v", requeueAfter, defaultPollInterval)
		}
	})

	t.Run("eligible pods are excluded from requeue calculation", func(t *testing.T) {
		pods := []corev1.Pod{podFailedAt(now.Add(-11 * time.Minute))} // already eligible
		_, requeueAfter := evaluate(pods, ttl, now, defaultPollInterval)
		if requeueAfter != defaultPollInterval {
			t.Errorf("requeueAfter = %v, want %v", requeueAfter, defaultPollInterval)
		}
	})
}

const (
	testNamespace    = "default"
	testStalePodName = "stale"
	testFreshPodName = "fresh"
	testSomePodName  = "some-pod"
	envLabelKey      = "env"
	tierLabelKey     = "tier"
	tierValueBatch   = "batch"
)

func failedPod(name string, failedMinutesAgo time.Duration, now time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: types.UID(name + "-uid")},
		Status: corev1.PodStatus{
			Phase:     corev1.PodFailed,
			StartTime: ptrTime(now.Add(-failedMinutesAgo)),
		},
	}
}

func evalTestPolicy(ttl time.Duration, mode string) *sweeperv1alpha1.PodSweepPolicy {
	return &sweeperv1alpha1.PodSweepPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test-policy"},
		Spec: sweeperv1alpha1.PodSweepPolicySpec{
			TTL:  metav1.Duration{Duration: ttl},
			Mode: mode,
		},
	}
}

func TestEvaluationSubReconcilerReconcile(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Minute
	noProtected := sets.New[string]()

	t.Run("report mode never deletes, even past TTL", func(t *testing.T) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		pod := failedPod(testStalePodName, 11*time.Minute, now)
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		c := newFakeClientForEval(t, ns, pod, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		got := &corev1.Pod{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: testStalePodName, Namespace: testNamespace}, got); err != nil {
			t.Fatalf("pod should still exist in report mode, got error: %v", err)
		}

		if gauge := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name)); gauge != 1 {
			t.Errorf("CurrentGauge = %v, want 1 (gauge updates even in report mode)", gauge)
		}
		if counter := testutil.ToFloat64(metrics.DeletedTotal.WithLabelValues(testNamespace, policy.Name)); counter != 0 {
			t.Errorf("DeletedTotal = %v, want 0 (report mode never deletes)", counter)
		}
	})

	t.Run("enforce mode deletes only pods past TTL", func(t *testing.T) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		stale := failedPod(testStalePodName, 11*time.Minute, now)
		fresh := failedPod(testFreshPodName, 1*time.Minute, now)
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeEnforce)
		c := newFakeClientForEval(t, ns, stale, fresh, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		if err := c.Get(context.Background(), types.NamespacedName{Name: testStalePodName, Namespace: testNamespace}, &corev1.Pod{}); !errors.IsNotFound(err) {
			t.Errorf("expected stale pod to be deleted, got error: %v", err)
		}
		if err := c.Get(context.Background(), types.NamespacedName{Name: testFreshPodName, Namespace: testNamespace}, &corev1.Pod{}); err != nil {
			t.Errorf("expected fresh pod to survive, got error: %v", err)
		}

		if gauge := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name)); gauge != 1 {
			t.Errorf("CurrentGauge = %v, want 1 (only the stale pod was eligible)", gauge)
		}
		if counter := testutil.ToFloat64(metrics.DeletedTotal.WithLabelValues(testNamespace, policy.Name)); counter != 1 {
			t.Errorf("DeletedTotal = %v, want 1 (only the stale pod was actually deleted)", counter)
		}
	})

	t.Run("disable mode does nothing", func(t *testing.T) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		pod := failedPod(testStalePodName, 11*time.Minute, now)
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeDisable)
		c := newFakeClientForEval(t, ns, pod, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		result, err := r.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if !result.IsZero() {
			t.Errorf("result = %+v, want zero result", result)
		}
		if err := c.Get(context.Background(), types.NamespacedName{Name: testStalePodName, Namespace: testNamespace}, &corev1.Pod{}); err != nil {
			t.Errorf("expected pod to survive disabled rule, got error: %v", err)
		}
		if gauge := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name)); gauge != 0 {
			t.Errorf("CurrentGauge = %v, want 0 (disabled rule reports no metrics)", gauge)
		}
	})

	t.Run("transitioning to disable purges previously set metrics", func(t *testing.T) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		pod := failedPod(testStalePodName, 11*time.Minute, now)
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		c := newFakeClientForEval(t, ns, pod, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if gauge := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name)); gauge != 1 {
			t.Fatalf("CurrentGauge before disabling = %v, want 1", gauge)
		}

		policy.Spec.Mode = sweeperv1alpha1.ModeDisable
		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		if gauge := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name)); gauge != 0 {
			t.Errorf("CurrentGauge after disabling = %v, want 0 (must be purged on report/enforce -> disable transition)", gauge)
		}
	})

	t.Run("running pods are never candidates", func(t *testing.T) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		running := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: testNamespace},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeEnforce)
		c := newFakeClientForEval(t, ns, running, policy)

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if err := c.Get(context.Background(), types.NamespacedName{Name: "running", Namespace: testNamespace}, &corev1.Pod{}); err != nil {
			t.Errorf("expected running pod to survive, got error: %v", err)
		}
	})

	t.Run("RequeueAfter is jittered around the un-jittered evaluate() value", func(t *testing.T) {
		// requeueJitterPercent must match cmd/main.go's requeueJitterPercent:
		// jitter.SetGlobalIntervalJitter is sync.Once-guarded, so this is the one place in the
		// suite allowed to set it, and it must use the same percentage production does.
		const requeueJitterPercent = 0.10

		// A fixed seed makes jitter.Percent's output deterministic, so the expected RequeueAfter
		// can be computed exactly instead of only checked against a range that would also accept
		// jitter being silently disabled (see MR !7 review discussion).
		var seed1, seed2 uint64 = 1, 2
		jitter.SetGlobalIntervalJitter(requeueJitterPercent, mathrand.New(mathrand.NewPCG(seed1, seed2)))

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		pod := failedPod(testFreshPodName, 8*time.Minute, now) // 2m until eligible
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		c := newFakeClientForEval(t, ns, pod, policy)

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		result, err := r.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}

		// Mirrors jitter.Percent's formula against an independently, identically-seeded rand so
		// this test doesn't need to reach into the jitter package's unexported closure state.
		wantUnjittered := 2 * time.Minute
		independentRand := mathrand.New(mathrand.NewPCG(seed1, seed2))
		randomP := requeueJitterPercent * (2*independentRand.Float64() - 1)
		wantExact := time.Duration(float64(wantUnjittered) * (1 + randomP))
		if wantExact == wantUnjittered {
			t.Fatal("test seed produced zero jitter; pick a different seed so this test can distinguish jitter being applied from a no-op")
		}
		if result.RequeueAfter != wantExact {
			t.Errorf("RequeueAfter = %v, want exactly %v", result.RequeueAfter, wantExact)
		}
	})

	t.Run("protected, non-allowed namespace is excluded from evaluation", func(t *testing.T) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
		pod := failedPod(testStalePodName, 11*time.Minute, now)
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeEnforce)
		c := newFakeClientForEval(t, ns, pod, policy)

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, sets.New(testNamespace), noProtected)
		r.Now = func() time.Time { return now }

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if err := c.Get(context.Background(), types.NamespacedName{Name: testStalePodName, Namespace: testNamespace}, &corev1.Pod{}); err != nil {
			t.Errorf("expected pod in protected namespace to survive, got error: %v", err)
		}
	})
}

func TestEvaluationSubReconcilerDelete(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: testSomePodName, Namespace: testNamespace},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}
	policy := evalTestPolicy(10*time.Minute, sweeperv1alpha1.ModeEnforce)
	c := newFakeClientForEval(t, ns, pod, policy)

	r := &EvaluationSubReconciler{Client: c, Policy: policy}
	metrics.SetCurrent(testNamespace, policy.Name, 1)

	result, err := r.Delete(context.Background())
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !result.IsZero() {
		t.Errorf("result = %+v, want zero result", result)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: testSomePodName, Namespace: testNamespace}, &corev1.Pod{}); err != nil {
		t.Errorf("Delete on the sub-reconciler must never cascade into deleting pods, got error: %v", err)
	}
	if got := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name)); got != 0 {
		t.Errorf("CurrentGauge after Delete() = %v, want 0 (fresh series, policy metrics purged)", got)
	}
}

func TestEvaluationSubReconcilerName(t *testing.T) {
	r := &EvaluationSubReconciler{}
	if got := r.Name(); got != "Evaluation" {
		t.Errorf("Name() = %q, want %q", got, "Evaluation")
	}
}

func TestEvaluationSubReconcilerWatches(t *testing.T) {
	c := newFakeClientForEval(t)
	r := &EvaluationSubReconciler{Client: c}

	watches := r.Watches()
	if len(watches) != 1 {
		t.Fatalf("Watches() returned %d entries, want 1", len(watches))
	}

	watch := watches[0](nil, logr.Discard(), nil)
	if _, ok := watch.Object.(*corev1.Pod); !ok {
		t.Errorf("watch.Object = %T, want *corev1.Pod", watch.Object)
	}
	if watch.EventHandler == nil {
		t.Error("watch.EventHandler is nil")
	}
}

func TestMapFailedPodToPolicies(t *testing.T) {
	failedPodIn := func(ns string, podLabels map[string]string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: testSomePodName, Namespace: ns, Labels: podLabels},
			Status:     corev1.PodStatus{Phase: corev1.PodFailed},
		}
	}

	namedPolicy := func(name string, nsSel, podSel *metav1.LabelSelector) *sweeperv1alpha1.PodSweepPolicy {
		return &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				NamespaceSelector: nsSel,
				PodSelector:       podSel,
			},
		}
	}

	matchAllPolicy := namedPolicy("matches-all", nil, nil)
	nonMatchingPolicy := namedPolicy("wants-web-tier",
		nil, &metav1.LabelSelector{MatchLabels: map[string]string{tierLabelKey: "web"}})

	tests := map[string]struct {
		pod      client.Object
		ns       *corev1.Namespace
		policies []client.Object
		want     []types.NamespacedName
	}{
		"zero PodSweepPolicy objects yields no requests": {
			pod:      failedPodIn(testNamespace, nil),
			ns:       &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
			policies: nil,
			want:     nil,
		},
		"non-Failed-phase pod yields no requests": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: testSomePodName, Namespace: testNamespace},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			},
			ns:       &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
			policies: []client.Object{matchAllPolicy},
			want:     nil,
		},
		"pod's namespace does not exist yields no requests": {
			pod:      failedPodIn(testNamespace, nil),
			ns:       nil,
			policies: []client.Object{matchAllPolicy},
			want:     nil,
		},
		"one matching policy yields one request": {
			pod:      failedPodIn(testNamespace, map[string]string{tierLabelKey: tierValueBatch}),
			ns:       &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
			policies: []client.Object{matchAllPolicy},
			want:     []types.NamespacedName{{Name: matchAllPolicy.Name}},
		},
		"one non-matching policy yields no requests": {
			pod:      failedPodIn(testNamespace, map[string]string{tierLabelKey: tierValueBatch}),
			ns:       &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
			policies: []client.Object{nonMatchingPolicy},
			want:     nil,
		},
		"mixed matching and non-matching policies yields only matching requests": {
			pod:      failedPodIn(testNamespace, map[string]string{tierLabelKey: tierValueBatch}),
			ns:       &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}},
			policies: []client.Object{matchAllPolicy, nonMatchingPolicy},
			want:     []types.NamespacedName{{Name: matchAllPolicy.Name}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{tc.pod}
			if tc.ns != nil {
				objs = append(objs, tc.ns)
			}
			objs = append(objs, tc.policies...)
			c := newFakeClientForEval(t, objs...)

			r := &EvaluationSubReconciler{Client: c}
			reqs := r.mapFailedPodToPolicies(logr.Discard())(context.Background(), tc.pod)

			got := make([]types.NamespacedName, len(reqs))
			for i, req := range reqs {
				got[i] = req.NamespacedName
			}
			if len(got) != len(tc.want) {
				t.Fatalf("mapFailedPodToPolicies() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("mapFailedPodToPolicies() = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

func TestPolicyMatchesPod(t *testing.T) {
	prodNamespace := labels.Set{envLabelKey: "prod"}

	policy := func(nsSel, podSel *metav1.LabelSelector) *sweeperv1alpha1.PodSweepPolicy {
		return &sweeperv1alpha1.PodSweepPolicy{
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				NamespaceSelector: nsSel,
				PodSelector:       podSel,
			},
		}
	}

	batchPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{tierLabelKey: tierValueBatch}}}

	tests := map[string]struct {
		policy *sweeperv1alpha1.PodSweepPolicy
		pod    *corev1.Pod
		nsSet  labels.Set
		want   bool
	}{
		"nil selectors match everything": {
			policy: policy(nil, nil),
			pod:    batchPod,
			nsSet:  prodNamespace,
			want:   true,
		},
		"namespace selector excludes non-matching namespace": {
			policy: policy(&metav1.LabelSelector{MatchLabels: map[string]string{envLabelKey: "dev"}}, nil),
			pod:    batchPod,
			nsSet:  prodNamespace,
			want:   false,
		},
		"pod selector excludes non-matching pod": {
			policy: policy(nil, &metav1.LabelSelector{MatchLabels: map[string]string{tierLabelKey: "web"}}),
			pod:    batchPod,
			nsSet:  prodNamespace,
			want:   false,
		},
		"both selectors match": {
			policy: policy(
				&metav1.LabelSelector{MatchLabels: map[string]string{envLabelKey: "prod"}},
				&metav1.LabelSelector{MatchLabels: map[string]string{tierLabelKey: tierValueBatch}},
			),
			pod:   batchPod,
			nsSet: prodNamespace,
			want:  true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := policyMatchesPod(tc.policy, tc.pod, tc.nsSet); got != tc.want {
				t.Errorf("policyMatchesPod() = %v, want %v", got, tc.want)
			}
		})
	}
}

// listErrorForClient fails List for one list type and delegates the rest to the embedded client.
type listErrorForClient struct {
	client.Client
	failFor client.ObjectList
}

func (c *listErrorForClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if reflect.TypeOf(list) == reflect.TypeOf(c.failFor) {
		return fmt.Errorf("injected List failure for %T", list)
	}
	return c.Client.List(ctx, list, opts...)
}

// deleteErrorClient fails Delete for every object, or only for the pod named failOnly when it
// is set.
type deleteErrorClient struct {
	client.Client
	failOnly string
}

func (c *deleteErrorClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if c.failOnly == "" || obj.GetName() == c.failOnly {
		return fmt.Errorf("injected Delete failure for %s", obj.GetName())
	}
	return c.Client.Delete(ctx, obj, opts...)
}

// evaluatedCond returns the Evaluated condition or fails the test when it is missing.
func evaluatedCond(t *testing.T, policy *sweeperv1alpha1.PodSweepPolicy) *metav1.Condition {
	t.Helper()
	cond := meta.FindStatusCondition(policy.Status.Conditions, sweeperv1alpha1.ConditionTypeEvaluated)
	if cond == nil {
		t.Fatalf("Evaluated condition is missing; conditions = %+v", policy.Status.Conditions)
	}
	if cond.ObservedGeneration != policy.Generation {
		t.Errorf("Evaluated.ObservedGeneration = %d, want %d", cond.ObservedGeneration, policy.Generation)
	}
	return cond
}

func expectEvaluated(t *testing.T, policy *sweeperv1alpha1.PodSweepPolicy, status metav1.ConditionStatus, reason string) {
	t.Helper()
	cond := evaluatedCond(t, policy)
	if cond.Status != status || cond.Reason != reason {
		t.Errorf("Evaluated = %s/%s (%q), want %s/%s", cond.Status, cond.Reason, cond.Message, status, reason)
	}
}

func TestEvaluationSubReconcilerConditions(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Minute
	noProtected := sets.New[string]()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}

	newReconciler := func(c client.Client, policy *sweeperv1alpha1.PodSweepPolicy) (*EvaluationSubReconciler, *record.FakeRecorder) {
		recorder := record.NewFakeRecorder(10)
		r := NewEvaluationSubReconciler(c, recorder, policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }
		return r, recorder
	}

	t.Run("sets Evaluated=True after a clean pass", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		c := newFakeClientForEval(t, ns, failedPod(testStalePodName, 11*time.Minute, now), policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		r, recorder := newReconciler(c, policy)

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		expectEvaluated(t, policy, metav1.ConditionTrue, reasonEvaluated)
		if got := len(recorder.Events); got != 0 {
			t.Errorf("events = %d, want 0", got)
		}
	})

	t.Run("sets Evaluated=False/Disabled in disable mode", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeDisable)
		c := newFakeClientForEval(t, ns, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		r, recorder := newReconciler(c, policy)

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		expectEvaluated(t, policy, metav1.ConditionFalse, reasonDisabled)
		if got := len(recorder.Events); got != 0 {
			t.Errorf("events = %d, want 0", got)
		}
	})

	t.Run("sets Evaluated=False/NamespaceListFailed when listing namespaces fails", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		c := &listErrorForClient{Client: newFakeClientForEval(t, ns, policy), failFor: &corev1.NamespaceList{}}
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		r, recorder := newReconciler(c, policy)

		if _, err := r.Reconcile(context.Background()); err == nil {
			t.Fatal("Reconcile() error = nil, want an error")
		}
		expectEvaluated(t, policy, metav1.ConditionFalse, reasonNamespaceListFailed)
		if got := len(recorder.Events); got != 1 {
			t.Errorf("events = %d, want 1", got)
		}
	})

	t.Run("sets Evaluated=False/PodListFailed when listing pods fails", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		c := &listErrorForClient{Client: newFakeClientForEval(t, ns, policy), failFor: &corev1.PodList{}}
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		r, _ := newReconciler(c, policy)

		if _, err := r.Reconcile(context.Background()); err == nil {
			t.Fatal("Reconcile() error = nil, want an error")
		}
		expectEvaluated(t, policy, metav1.ConditionFalse, reasonPodListFailed)
		if msg := evaluatedCond(t, policy).Message; !strings.HasPrefix(msg, "listing pods in namespace default:") {
			t.Errorf("Evaluated.Message = %q, want prefix %q", msg, "listing pods in namespace default:")
		}
	})

	t.Run("sets Evaluated=False/DeleteFailed and returns the delete error on a delete failure", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeEnforce)
		stale := failedPod(testStalePodName, 11*time.Minute, now)
		other := failedPod(testSomePodName, 12*time.Minute, now)
		c := &deleteErrorClient{Client: newFakeClientForEval(t, ns, stale, other, policy)}
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		r, recorder := newReconciler(c, policy)

		if _, err := r.Reconcile(context.Background()); err == nil {
			t.Fatal("Reconcile() error = nil, want an error")
		}
		expectEvaluated(t, policy, metav1.ConditionFalse, reasonDeleteFailed)
		if counter := testutil.ToFloat64(metrics.DeletedTotal.WithLabelValues(testNamespace, policy.Name)); counter != 0 {
			t.Errorf("DeletedTotal = %v, want 0", counter)
		}
		if got := len(recorder.Events); got != 1 {
			t.Errorf("events = %d, want 1", got)
		}
	})

	t.Run("counts successful deletes when one delete fails", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeEnforce)
		stale := failedPod(testStalePodName, 11*time.Minute, now)
		other := failedPod(testSomePodName, 12*time.Minute, now)
		c := &deleteErrorClient{Client: newFakeClientForEval(t, ns, stale, other, policy), failOnly: testStalePodName}
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		r, _ := newReconciler(c, policy)

		if _, err := r.Reconcile(context.Background()); err == nil {
			t.Fatal("Reconcile() error = nil, want an error")
		}
		expectEvaluated(t, policy, metav1.ConditionFalse, reasonDeleteFailed)
		if counter := testutil.ToFloat64(metrics.DeletedTotal.WithLabelValues(testNamespace, policy.Name)); counter != 1 {
			t.Errorf("DeletedTotal = %v, want 1", counter)
		}
	})

	t.Run("records one Warning for a repeated failure", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		c := &listErrorForClient{Client: newFakeClientForEval(t, ns, policy), failFor: &corev1.PodList{}}
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		r, recorder := newReconciler(c, policy)

		for i := 0; i < 2; i++ {
			if _, err := r.Reconcile(context.Background()); err == nil {
				t.Fatalf("Reconcile() #%d error = nil, want an error", i+1)
			}
		}
		if got := len(recorder.Events); got != 1 {
			t.Errorf("events = %d, want 1", got)
		}
	})

	t.Run("records one Warning when the generation changes but the failure does not", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		policy.Generation = 1
		c := &listErrorForClient{Client: newFakeClientForEval(t, ns, policy), failFor: &corev1.PodList{}}
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		r, recorder := newReconciler(c, policy)

		if _, err := r.Reconcile(context.Background()); err == nil {
			t.Fatal("Reconcile() #1 error = nil, want an error")
		}
		policy.Generation = 2
		if _, err := r.Reconcile(context.Background()); err == nil {
			t.Fatal("Reconcile() #2 error = nil, want an error")
		}
		if got := len(recorder.Events); got != 1 {
			t.Errorf("events = %d, want 1", got)
		}
		// The fake client's status update copies the stored metadata back, which resets the
		// in-memory Generation to 1, so compare the condition against the literal generation.
		cond := meta.FindStatusCondition(policy.Status.Conditions, sweeperv1alpha1.ConditionTypeEvaluated)
		if cond == nil || cond.ObservedGeneration != 2 {
			t.Errorf("Evaluated = %+v, want ObservedGeneration 2", cond)
		}
	})

	t.Run("restores Evaluated=True after a failure clears", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		base := newFakeClientForEval(t, ns, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })
		failing, _ := newReconciler(&listErrorForClient{Client: base, failFor: &corev1.PodList{}}, policy)
		if _, err := failing.Reconcile(context.Background()); err == nil {
			t.Fatal("Reconcile() error = nil, want an error")
		}

		healthy, _ := newReconciler(base, policy)
		if _, err := healthy.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		expectEvaluated(t, policy, metav1.ConditionTrue, reasonEvaluated)
	})
}

func TestEvaluationSubReconcilerDeletePreconditions(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Minute
	noProtected := sets.New[string]()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}

	t.Run("delete carries the pod UID", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeEnforce)
		pod := failedPod(testStalePodName, 11*time.Minute, now)
		var got []client.DeleteOptions
		c := newInterceptedClientForEval(t, interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				got = append(got, *(&client.DeleteOptions{}).ApplyOptions(opts))
				return cl.Delete(ctx, obj, opts...)
			},
		}, ns, pod, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Delete calls = %d, want 1", len(got))
		}
		pre := got[0].Preconditions
		if pre == nil || pre.UID == nil {
			t.Fatalf("Preconditions = %+v, want a UID precondition", pre)
		}
		if *pre.UID != pod.UID {
			t.Errorf("Preconditions.UID = %q, want %q", *pre.UID, pod.UID)
		}
		if pre.ResourceVersion != nil {
			t.Errorf("Preconditions.ResourceVersion = %q, want nil", *pre.ResourceVersion)
		}
		if err := c.Get(context.Background(), types.NamespacedName{Name: testStalePodName, Namespace: testNamespace}, &corev1.Pod{}); !errors.IsNotFound(err) {
			t.Errorf("expected pod to be deleted, got error: %v", err)
		}
	})

	t.Run("conflict on a recreated pod is skipped", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeEnforce)
		pod := failedPod(testStalePodName, 11*time.Minute, now)
		c := newInterceptedClientForEval(t, interceptor.Funcs{
			Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteOption) error {
				return errors.NewConflict(corev1.Resource("pods"), obj.GetName(),
					fmt.Errorf("precondition failed: UID in precondition"))
			},
		}, ns, pod, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })

		recorder := record.NewFakeRecorder(10)
		r := NewEvaluationSubReconciler(c, recorder, policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		expectEvaluated(t, policy, metav1.ConditionTrue, reasonEvaluated)
		if counter := testutil.ToFloat64(metrics.DeletedTotal.WithLabelValues(testNamespace, policy.Name)); counter != 0 {
			t.Errorf("DeletedTotal = %v, want 0", counter)
		}
		if got := len(recorder.Events); got != 0 {
			t.Errorf("events = %d, want 0", got)
		}
	})
}

func terminatingFailedPod(name string, failedMinutesAgo time.Duration, now time.Time) *corev1.Pod {
	pod := failedPod(name, failedMinutesAgo, now)
	pod.Finalizers = []string{"example.com/hold"}
	pod.DeletionTimestamp = ptrTime(now)
	return pod
}

func TestEvaluationSubReconcilerTerminatingPods(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ttl := 10 * time.Minute
	noProtected := sets.New[string]()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNamespace}}
	const terminatingPodName = "terminating"

	t.Run("enforce", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeEnforce)
		terminating := terminatingFailedPod(terminatingPodName, 11*time.Minute, now)
		stale := failedPod(testStalePodName, 11*time.Minute, now)
		deletes := map[string]int{}
		c := newInterceptedClientForEval(t, interceptor.Funcs{
			Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				deletes[obj.GetName()]++
				return cl.Delete(ctx, obj, opts...)
			},
		}, ns, terminating, stale, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		for i := 0; i < 2; i++ {
			if _, err := r.Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile() #%d error = %v", i+1, err)
			}
		}
		if got := deletes[terminatingPodName]; got != 0 {
			t.Errorf("Delete calls for the terminating pod = %d, want 0", got)
		}
		if counter := testutil.ToFloat64(metrics.DeletedTotal.WithLabelValues(testNamespace, policy.Name)); counter != 1 {
			t.Errorf("DeletedTotal = %v, want 1 (only the non-terminating pod)", counter)
		}
		if gauge := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name)); gauge != 0 {
			t.Errorf("CurrentGauge = %v, want 0", gauge)
		}
	})

	t.Run("report", func(t *testing.T) {
		policy := evalTestPolicy(ttl, sweeperv1alpha1.ModeReport)
		terminating := terminatingFailedPod(terminatingPodName, 11*time.Minute, now)
		stale := failedPod(testStalePodName, 11*time.Minute, now)
		c := newFakeClientForEval(t, ns, terminating, stale, policy)
		t.Cleanup(func() { metrics.DeletePolicy(policy.Name) })

		r := NewEvaluationSubReconciler(c, record.NewFakeRecorder(10), policy, noProtected, noProtected)
		r.Now = func() time.Time { return now }

		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if gauge := testutil.ToFloat64(metrics.CurrentGauge.WithLabelValues(testNamespace, policy.Name)); gauge != 1 {
			t.Errorf("CurrentGauge = %v, want 1 (the terminating pod is not a candidate)", gauge)
		}
	})
}
