/*
Copyright 2026.

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

// DoUsageSpec declares that a DoResource is in use by something outside
// the reference graph.
type DoUsageSpec struct {
	// Of names the DoResource (same namespace) whose deletion is blocked
	// while this usage exists.
	Of UsageTarget `json:"of"`

	// Reason documents who or what depends on the resource, e.g.
	// "database is used by the payments namespace". Surfaced in the
	// blocked resource's conditions.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// UsageTarget identifies the used DoResource.
type UsageTarget struct {
	// Name of the DoResource in this namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.of.name is immutable"
	Name string `json:"name"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=douse
// +kubebuilder:printcolumn:name="OF",type=string,JSONPath=`.spec.of.name`
// +kubebuilder:printcolumn:name="REASON",type=string,JSONPath=`.spec.reason`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DoUsage blocks deletion of a DoResource that is in use even when no
// spec.references edge exists — the platform-team equivalent of
// Crossplane's Usage. While any DoUsage targets a resource, its external
// teardown is refused (Ready=False / BlockedByDependents); deleting the
// usage unblocks it.
type DoUsage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec DoUsageSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// DoUsageList contains a list of DoUsage.
type DoUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoUsage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DoUsage{}, &DoUsageList{})
}
