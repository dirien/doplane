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

// DoCompositeDefinitionRevisionSpec is the immutable snapshot of one
// definition generation.
type DoCompositeDefinitionRevisionSpec struct {
	// DefinitionName is the DoCompositeDefinition this revision snapshots.
	// +kubebuilder:validation:MinLength=1
	DefinitionName string `json:"definitionName"`

	// Revision numbers the snapshot, monotonically increasing per
	// definition.
	// +kubebuilder:validation:Minimum=1
	Revision int64 `json:"revision"`

	// Definition is the snapshotted definition spec.
	Definition DoCompositeDefinitionSpec `json:"definition"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=docdrev
// +kubebuilder:printcolumn:name="DEFINITION",type=string,JSONPath=`.spec.definitionName`
// +kubebuilder:printcolumn:name="REVISION",type=integer,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DoCompositeDefinitionRevision is an immutable snapshot of a
// DoCompositeDefinition. Editing a definition creates the next revision;
// composites render from the revision their update policy selects, so a
// definition edit never silently rewrites pinned instances.
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="revisions are immutable"
type DoCompositeDefinitionRevision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec DoCompositeDefinitionRevisionSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// DoCompositeDefinitionRevisionList contains a list of revisions.
type DoCompositeDefinitionRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoCompositeDefinitionRevision `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DoCompositeDefinitionRevision{}, &DoCompositeDefinitionRevisionList{})
}
