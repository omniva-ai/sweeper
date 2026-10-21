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

package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSetCurrent(t *testing.T) {
	SetCurrent("ns-a", "policy-a", 3)
	t.Cleanup(func() { DeletePolicy("policy-a") })

	got := testutil.ToFloat64(CurrentGauge.WithLabelValues("ns-a", "policy-a"))
	if got != 3 {
		t.Errorf("CurrentGauge = %v, want 3", got)
	}

	SetCurrent("ns-a", "policy-a", 0)
	got = testutil.ToFloat64(CurrentGauge.WithLabelValues("ns-a", "policy-a"))
	if got != 0 {
		t.Errorf("CurrentGauge after reset = %v, want 0", got)
	}
}

func TestIncDeleted(t *testing.T) {
	IncDeleted("ns-b", "policy-b")
	IncDeleted("ns-b", "policy-b")
	t.Cleanup(func() { DeletePolicy("policy-b") })

	got := testutil.ToFloat64(DeletedTotal.WithLabelValues("ns-b", "policy-b"))
	if got != 2 {
		t.Errorf("DeletedTotal = %v, want 2", got)
	}
}

func TestResetCurrentDropsNamespacesNoLongerPresent(t *testing.T) {
	SetCurrent("ns-stale", "policy-reset", 4)
	SetCurrent("ns-fresh", "policy-reset", 1)
	t.Cleanup(func() { DeletePolicy("policy-reset") })

	ResetCurrent("policy-reset", map[string]float64{"ns-fresh": 2})

	if got := testutil.ToFloat64(CurrentGauge.WithLabelValues("ns-fresh", "policy-reset")); got != 2 {
		t.Errorf("ns-fresh CurrentGauge = %v, want 2", got)
	}
	if got := testutil.ToFloat64(CurrentGauge.WithLabelValues("ns-stale", "policy-reset")); got != 0 {
		t.Errorf("ns-stale CurrentGauge = %v, want 0 (namespace no longer present must be dropped)", got)
	}
}

func TestResetCurrentLeavesOtherPoliciesUntouched(t *testing.T) {
	SetCurrent("ns-d", "policy-other", 7)
	t.Cleanup(func() { DeletePolicy("policy-other"); DeletePolicy("policy-reset-2") })

	ResetCurrent("policy-reset-2", map[string]float64{"ns-d": 1})

	if got := testutil.ToFloat64(CurrentGauge.WithLabelValues("ns-d", "policy-other")); got != 7 {
		t.Errorf("policy-other CurrentGauge = %v, want 7 (unrelated policy must survive)", got)
	}
}

func TestDeletePolicyRemovesOnlyThatPolicy(t *testing.T) {
	SetCurrent("ns-c", "policy-keep", 1)
	SetCurrent("ns-c", "policy-remove", 5)
	IncDeleted("ns-c", "policy-remove")
	t.Cleanup(func() { DeletePolicy("policy-keep") })

	DeletePolicy("policy-remove")

	if got := testutil.ToFloat64(CurrentGauge.WithLabelValues("ns-c", "policy-keep")); got != 1 {
		t.Errorf("policy-keep CurrentGauge = %v, want 1 (must survive DeletePolicy(policy-remove))", got)
	}

	removedGauge := CurrentGauge.WithLabelValues("ns-c", "policy-remove")
	if got := testutil.ToFloat64(removedGauge); got != 0 {
		t.Errorf("policy-remove CurrentGauge after delete = %v, want 0 (fresh series)", got)
	}

	removedCounter := DeletedTotal.WithLabelValues("ns-c", "policy-remove")
	if got := testutil.ToFloat64(removedCounter); got != 0 {
		t.Errorf("policy-remove DeletedTotal after delete = %v, want 0 (fresh series)", got)
	}
}
