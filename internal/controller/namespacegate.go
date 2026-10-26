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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// resolveNamespaceGate lists the namespaces matched by selector (nil/empty selector matches all
// namespaces) and partitions them into matched (usable) and skipped (protected and not
// explicitly allowed). It is shared by ValidationSubReconciler and EvaluationSubReconciler so the
// protected-namespace gate is computed identically by both.
func resolveNamespaceGate(ctx context.Context, cl client.Client, selector *metav1.LabelSelector,
	protected, allowed sets.Set[string]) (matched, skipped []string, err error) {
	// asSelector treats an absent selector as "all namespaces" per the CRD's documented semantics.
	labelSelector, err := asSelector(selector)
	if err != nil {
		return nil, nil, err
	}

	var namespaces corev1.NamespaceList
	if err := cl.List(ctx, &namespaces, client.MatchingLabelsSelector{Selector: labelSelector}); err != nil {
		return nil, nil, err
	}

	for _, ns := range namespaces.Items {
		if protected.Has(ns.Name) && !allowed.Has(ns.Name) {
			skipped = append(skipped, ns.Name)
			continue
		}
		matched = append(matched, ns.Name)
	}

	return matched, skipped, nil
}
