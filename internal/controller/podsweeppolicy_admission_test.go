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

// ttlPolicy builds an unstructured PodSweepPolicy so spec.ttl can hold any string,
// including values a typed metav1.Duration could never serialize.
func ttlPolicy(name, ttl string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": sweeperv1alpha1.GroupVersion.String(),
			"kind":       "PodSweepPolicy",
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"ttl": ttl,
			},
		},
	}
}

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

	DescribeTable("rejects a spec.ttl that is not a positive Go duration",
		func(name, ttl, wantMessage string) {
			err := k8sClient.Create(context.Background(), ttlPolicy(name, ttl))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.ttl"))
			Expect(err.Error()).To(ContainSubstring(wantMessage))
		},
		Entry("day unit", "ttl-day-unit", "1d", "a day unit (1d) is not supported"),
		Entry("not a duration", "ttl-not-a-duration", "abc", "ttl must be a Go duration"),
		Entry("space before unit", "ttl-space-unit", "1 hour", "ttl must be a Go duration"),
		Entry("uppercase unit", "ttl-upper-unit", "1H", "ttl must be a Go duration"),
		Entry("negative", "ttl-negative", "-5m", "ttl must be a Go duration"),
		Entry("zero", "ttl-zero", "0s", "ttl must be greater than 0s"),
		Entry("overflow", "ttl-overflow", "9999999999999h", "spec.ttl"),
	)

	DescribeTable("accepts a spec.ttl that is a positive Go duration",
		func(name, ttl string) {
			Expect(k8sClient.Create(context.Background(), ttlPolicy(name, ttl))).To(Succeed())
		},
		Entry("hours", "ttl-hours", "72h"),
		Entry("multi-unit", "ttl-multi-unit", "1h30m"),
		Entry("milliseconds", "ttl-millis", "500ms"),
		Entry("fraction", "ttl-fraction", "1.5h"),
	)

	It("rejects an update that sets spec.ttl to a day unit", func() {
		policy := ttlPolicy("ttl-update", "1h")
		Expect(k8sClient.Create(context.Background(), policy)).To(Succeed())

		Expect(unstructured.SetNestedField(policy.Object, "1d", "spec", "ttl")).To(Succeed())
		err := k8sClient.Update(context.Background(), policy)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("a day unit (1d) is not supported"))
	})
})
