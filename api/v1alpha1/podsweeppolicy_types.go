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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ModeEnforce evaluates the rule and deletes pods that have crossed their TTL.
	ModeEnforce = "enforce"
	// ModeReport evaluates the rule and emits metrics, but never deletes pods. Default mode.
	ModeReport = "report"
	// ModeDisable skips evaluation of the rule entirely: no listing, no metrics, no deletes.
	ModeDisable = "disable"
)

const (
	// ConditionTypeReady indicates whether the PodSweepPolicy has been successfully validated
	// and is being evaluated.
	ConditionTypeReady = "Ready"
	// ConditionTypeNamespacesSkippedProtected indicates whether one or more namespaces matched
	// by spec.namespaceSelector were excluded from evaluation because they are protected at the
	// operator level and have not been explicitly unlocked.
	ConditionTypeNamespacesSkippedProtected = "NamespacesSkippedProtected"
	// ConditionTypeEvaluated indicates whether the last evaluation pass listed pods and, in
	// enforce mode, deleted eligible ones, without error.
	ConditionTypeEvaluated = "Evaluated"
)

// PodSweepPolicySpec defines the desired state of PodSweepPolicy.
type PodSweepPolicySpec struct {
	// NamespaceSelector selects the namespaces this rule applies to. An empty or absent selector
	// matches all namespaces, subject to the operator-level protected-namespace gate.
	// +optional
	// +kubebuilder:validation:XValidation:rule="!has(self.matchExpressions) || self.matchExpressions.all(e, e.operator in ['In', 'NotIn', 'Exists', 'DoesNotExist'])",message="matchExpressions operator must be one of In, NotIn, Exists, DoesNotExist"
	// +kubebuilder:validation:XValidation:rule="!has(self.matchExpressions) || self.matchExpressions.all(e, !(e.operator in ['In', 'NotIn', 'Exists', 'DoesNotExist']) || ((e.operator in ['In', 'NotIn']) ? (has(e.values) && size(e.values) > 0) : (!has(e.values) || size(e.values) == 0)))",message="matchExpressions values must be set for In and NotIn and empty for Exists and DoesNotExist"
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// PodSelector selects the pods this rule applies to within matched namespaces. An empty or
	// absent selector matches all pods.
	// +optional
	// +kubebuilder:validation:XValidation:rule="!has(self.matchExpressions) || self.matchExpressions.all(e, e.operator in ['In', 'NotIn', 'Exists', 'DoesNotExist'])",message="matchExpressions operator must be one of In, NotIn, Exists, DoesNotExist"
	// +kubebuilder:validation:XValidation:rule="!has(self.matchExpressions) || self.matchExpressions.all(e, !(e.operator in ['In', 'NotIn', 'Exists', 'DoesNotExist']) || ((e.operator in ['In', 'NotIn']) ? (has(e.values) && size(e.values) > 0) : (!has(e.values) || size(e.values) == 0)))",message="matchExpressions values must be set for In and NotIn and empty for Exists and DoesNotExist"
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`

	// TTL is how long a pod may sit in the Failed phase before it becomes eligible for deletion.
	// It is a Go duration (for example 72h, 1h30m or 500ms) and must be greater than zero.
	// Accepted units are ns, us, ms, s, m and h. A day unit such as 1d is not supported.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:XValidation:rule="self.matches('^([0-9]+([.][0-9]+)?(ns|us|ms|s|m|h))+$')",message="ttl must be a Go duration such as 72h, 1h30m or 500ms; a day unit (1d) is not supported"
	// +kubebuilder:validation:XValidation:rule="!self.matches('^([0-9]+([.][0-9]+)?(ns|us|ms|s|m|h))+$') || duration(self) > duration('0s')",message="ttl must be greater than 0s"
	TTL metav1.Duration `json:"ttl"`

	// Mode controls whether the rule is evaluated and whether it deletes pods.
	// +optional
	// +kubebuilder:validation:Enum=enforce;report;disable
	// +kubebuilder:default=report
	Mode string `json:"mode,omitempty"`
}

// PodSweepPolicyStatus defines the observed state of PodSweepPolicy.
type PodSweepPolicyStatus struct {
	// ObservedGeneration is the most recent generation observed for this PodSweepPolicy.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the PodSweepPolicy's state.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="TTL",type=string,JSONPath=`.spec.ttl`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Evaluated",type=string,JSONPath=`.status.conditions[?(@.type=="Evaluated")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// PodSweepPolicy is the Schema for the podsweeppolicies API.
type PodSweepPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodSweepPolicySpec   `json:"spec"`
	Status PodSweepPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PodSweepPolicyList contains a list of PodSweepPolicy.
type PodSweepPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodSweepPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PodSweepPolicy{}, &PodSweepPolicyList{})
}
