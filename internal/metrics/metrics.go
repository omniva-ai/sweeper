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

// Package metrics defines sweeper's custom Prometheus metrics and registers them on
// controller-runtime's standard metrics registry, so they ship on the existing /metrics
// endpoint without any manager wiring changes.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	currentGaugeName = "sweeper_evicted_failed_pods_current"
	deletedTotalName = "sweeper_pods_deleted_total"
	policyLabel      = "policy"
	namespaceLabel   = "namespace"
)

var (
	// CurrentGauge tracks, per namespace and policy, how many pods are currently eligible for
	// deletion (past their policy's TTL) at last evaluation. Updated regardless of mode,
	// including report, so operators can see enforce's blast radius before enabling it.
	CurrentGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: currentGaugeName,
		Help: "Number of Failed-phase pods currently past their PodSweepPolicy's TTL, by namespace and policy.",
	}, []string{namespaceLabel, policyLabel})

	// DeletedTotal counts, per namespace and policy, pods actually deleted. Only incremented in
	// enforce mode.
	DeletedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: deletedTotalName,
		Help: "Cumulative count of pods deleted by a PodSweepPolicy in enforce mode, by namespace and policy.",
	}, []string{namespaceLabel, policyLabel})
)

// SetCurrent sets the current-eligible-pods gauge for a (namespace, policy) pair.
func SetCurrent(namespace, policy string, count float64) {
	CurrentGauge.WithLabelValues(namespace, policy).Set(count)
}

// ResetCurrent replaces every current-gauge series for policy with exactly the given
// per-namespace counts, dropping any namespace no longer present. Callers should use this
// instead of reaching into CurrentGauge directly, so this package's label keys stay private.
func ResetCurrent(policy string, countsByNamespace map[string]float64) {
	CurrentGauge.DeletePartialMatch(prometheus.Labels{policyLabel: policy})
	for ns, count := range countsByNamespace {
		SetCurrent(ns, policy, count)
	}
}

// IncDeleted increments the deleted-pods counter for a (namespace, policy) pair.
func IncDeleted(namespace, policy string) {
	DeletedTotal.WithLabelValues(namespace, policy).Inc()
}

// DeletePolicy removes every series labeled with policy from both metrics, so a deleted
// PodSweepPolicy doesn't leave stale label combinations behind indefinitely.
func DeletePolicy(policy string) {
	labels := prometheus.Labels{policyLabel: policy}
	CurrentGauge.DeletePartialMatch(labels)
	DeletedTotal.DeletePartialMatch(labels)
}

func init() {
	crmetrics.Registry.MustRegister(CurrentGauge)
	crmetrics.Registry.MustRegister(DeletedTotal)
}
