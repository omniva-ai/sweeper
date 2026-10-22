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
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNamespaceKubeSystem     = "kube-system"
	testNamespacePlatformSystem = "platform-system"
)

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
	}
}

func TestResolveNamespaceGate(t *testing.T) {
	batchLabels := map[string]string{"team": "batch"}

	tests := []struct {
		name        string
		namespaces  []*corev1.Namespace
		selector    *metav1.LabelSelector
		protected   sets.Set[string]
		allowed     sets.Set[string]
		wantMatched []string
		wantSkipped []string
	}{
		{
			name: "nil selector matches all namespaces, default protected list excludes it",
			namespaces: []*corev1.Namespace{
				namespace(testNamespaceKubeSystem, nil),
				namespace(testNamespacePlatformSystem, nil),
				namespace(testNamespace, nil),
				namespace("batch-jobs", batchLabels),
			},
			selector:    nil,
			protected:   sets.New(testNamespaceKubeSystem, testNamespacePlatformSystem),
			allowed:     sets.New[string](),
			wantMatched: []string{"batch-jobs", testNamespace},
			wantSkipped: []string{testNamespaceKubeSystem, testNamespacePlatformSystem},
		},
		{
			name: "label selector narrows the match before the gate applies",
			namespaces: []*corev1.Namespace{
				namespace(testNamespaceKubeSystem, nil),
				namespace("batch-jobs", batchLabels),
				namespace("other", nil),
			},
			selector:    &metav1.LabelSelector{MatchLabels: batchLabels},
			protected:   sets.New(testNamespaceKubeSystem, testNamespacePlatformSystem),
			allowed:     sets.New[string](),
			wantMatched: []string{"batch-jobs"},
			wantSkipped: nil,
		},
		{
			name: "allow-protected-namespace unlocks an explicitly allowed namespace",
			namespaces: []*corev1.Namespace{
				namespace(testNamespaceKubeSystem, nil),
				namespace(testNamespacePlatformSystem, nil),
			},
			selector:    nil,
			protected:   sets.New(testNamespaceKubeSystem, testNamespacePlatformSystem),
			allowed:     sets.New(testNamespaceKubeSystem),
			wantMatched: []string{testNamespaceKubeSystem},
			wantSkipped: []string{testNamespacePlatformSystem},
		},
		{
			name: "empty protected set skips nothing",
			namespaces: []*corev1.Namespace{
				namespace(testNamespaceKubeSystem, nil),
			},
			selector:    nil,
			protected:   sets.New[string](),
			allowed:     sets.New[string](),
			wantMatched: []string{testNamespaceKubeSystem},
			wantSkipped: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := make([]runtime.Object, 0, len(tt.namespaces))
			for _, ns := range tt.namespaces {
				objs = append(objs, ns)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithRuntimeObjects(objs...).Build()

			matched, skipped, err := resolveNamespaceGate(context.Background(), cl, tt.selector, tt.protected, tt.allowed)
			if err != nil {
				t.Fatalf("resolveNamespaceGate() error = %v", err)
			}

			sort.Strings(matched)
			sort.Strings(skipped)

			if !equalStringSlices(matched, tt.wantMatched) {
				t.Errorf("matched = %v, want %v", matched, tt.wantMatched)
			}
			if !equalStringSlices(skipped, tt.wantSkipped) {
				t.Errorf("skipped = %v, want %v", skipped, tt.wantSkipped)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
