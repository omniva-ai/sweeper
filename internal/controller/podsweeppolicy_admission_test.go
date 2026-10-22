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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	sweeperv1alpha1 "github.com/omniva-ai/sweeper/api/v1alpha1"
)

var _ = Describe("PodSweepPolicy admission", func() {
	It("rejects a PodSweepPolicy with no spec", func() {
		// A typed PodSweepPolicy always serializes a (possibly zero-value) spec object,
		// since encoding/json's omitempty has no effect on non-pointer struct fields; only
		// an unstructured object can actually omit the spec key to exercise CRD-schema
		// required-ness the way a raw `kubectl apply` manifest without spec: {} would.
		policy := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": sweeperv1alpha1.GroupVersion.String(),
				"kind":       "PodSweepPolicy",
				"metadata": map[string]interface{}{
					"name": "no-spec",
				},
			},
		}

		err := k8sClient.Create(context.Background(), policy)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("spec"))
	})

	It("accepts a PodSweepPolicy with a valid spec.ttl", func() {
		policy := &sweeperv1alpha1.PodSweepPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: "valid-policy",
			},
			Spec: sweeperv1alpha1.PodSweepPolicySpec{
				TTL: metav1.Duration{Duration: time.Hour},
			},
		}

		Expect(k8sClient.Create(context.Background(), policy)).To(Succeed())
	})
})
