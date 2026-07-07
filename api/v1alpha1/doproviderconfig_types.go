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

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=doprovcfg
// +kubebuilder:printcolumn:name="PACKAGE",type=string,JSONPath=`.spec.package`
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="REASON",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].reason`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DoProviderConfig is the namespaced twin of the cluster-scoped DoProvider:
// a tenant-owned provider profile. Its credentials Secret lives in the
// config's own namespace, so teams pin provider versions and rotate their
// own credentials without platform involvement. Resources opt in with
// spec.providerRef.kind: DoProviderConfig (resolved in the resource's
// namespace); per-tenant credential isolation at runtime requires the
// per-resource runner namespace mode.
type DoProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DoProviderSpec   `json:"spec,omitempty"`
	Status DoProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DoProviderConfigList contains a list of DoProviderConfig.
type DoProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoProviderConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DoProviderConfig{}, &DoProviderConfigList{})
}
