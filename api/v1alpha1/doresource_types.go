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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeletionPolicy determines what happens to the external cloud resource when
// the DoResource is deleted.
type DeletionPolicy string

const (
	// DeletionDelete deletes the external resource via `pulumi do delete`.
	DeletionDelete DeletionPolicy = "Delete"
	// DeletionOrphan leaves the external resource in place.
	DeletionOrphan DeletionPolicy = "Orphan"
)

// Condition types and reasons used by the controller.
const (
	// ConditionReady indicates the external resource exists and its state is
	// recorded in status.outputs.
	ConditionReady = "Ready"
	// ConditionSynced indicates the last reconcile against the cloud provider
	// succeeded.
	ConditionSynced = "Synced"
)

// DoResourceSpec defines the desired state of DoResource.
type DoResourceSpec struct {
	// Type is the Pulumi resource type token to operate on,
	// e.g. "aws:s3/bucketV2:BucketV2" or "random:index/randomPet:RandomPet".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.type is immutable"
	Type string `json:"type"`

	// Package pins the provider package used to serve the resource, in the
	// form "name" or "name@version" (e.g. "aws@7.34.0"). When empty the
	// package is inferred from the type token and resolved by the Pulumi
	// registry.
	// +optional
	Package string `json:"package,omitempty"`

	// ProviderRef resolves package and credentials from a cluster-scoped
	// DoProvider profile, so platform teams pin provider versions in one
	// place. When both providerRef and package are set they must agree,
	// otherwise the resource is rejected.
	// +optional
	ProviderRef *ProviderReference `json:"providerRef,omitempty"`

	// Properties holds the resource input properties. They are validated
	// against the provider's JSON schema from the Pulumi registry before any
	// operation is attempted.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Properties *apiextensionsv1.JSON `json:"properties,omitempty"`

	// DeletionPolicy controls whether the external resource is deleted when
	// this object is deleted. Defaults to Delete.
	// +kubebuilder:validation:Enum=Delete;Orphan
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// References wire values from other DoResources in the same namespace
	// into this resource's properties before validation and any provider
	// operation. While a referenced value cannot be resolved yet the
	// resource waits (Ready=False, reason WaitingForDependency); when a
	// resolved value changes later the new value is propagated with a
	// patch. A resource that is referenced by others refuses to delete its
	// external resource until all dependents are gone.
	// +optional
	References []Reference `json:"references,omitempty"`
}

// ProviderReference points at a cluster-scoped DoProvider profile.
type ProviderReference struct {
	// Name of the DoProvider.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// Reference wires a single value from another DoResource into a property.
type Reference struct {
	// ToPath is a dot-separated path within spec.properties to set, e.g.
	// "bucket" or "tags.owner". Existing array elements are addressed with
	// [i], e.g. "rules[0].id". The path is created if it does not exist.
	// +kubebuilder:validation:MinLength=1
	ToPath string `json:"toPath"`

	// From identifies the source object and field.
	From ReferenceSource `json:"from"`

	// Template optionally transforms the resolved value: every "${value}"
	// occurrence in the template is replaced with the value rendered as a
	// string (objects and arrays render as compact JSON). "$${value}"
	// escapes a literal "${value}".
	// +optional
	Template string `json:"template,omitempty"`
}

// ReferenceSource points at a field of another DoResource.
type ReferenceSource struct {
	// Name of the source DoResource in the same namespace.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// FieldPath into the source object: "status.id" or a path below
	// "status.outputs.", e.g. "status.outputs.arn".
	// +kubebuilder:validation:MinLength=1
	FieldPath string `json:"fieldPath"`
}

// DoResourceStatus defines the observed state of DoResource.
type DoResourceStatus struct {
	// ID is the provider-assigned identifier of the external resource.
	// +optional
	ID string `json:"id,omitempty"`

	// Outputs is the full state of the external resource as returned by the
	// provider on the last successful create/patch/read. Persisted in etcd
	// with the rest of this object.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Outputs *apiextensionsv1.JSON `json:"outputs,omitempty"`

	// ObservedGeneration is the spec generation last applied to the cloud.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// AppliedHash is a hash of the last successfully applied, fully
	// reference-resolved properties. A differing hash (spec edit or a
	// changed upstream value) triggers a patch even when the generation is
	// unchanged.
	// +optional
	AppliedHash string `json:"appliedHash,omitempty"`

	// EngineState holds the exported Pulumi engine checkpoint for component
	// resources (which are orchestrated by an ephemeral engine instead of
	// stateless CRUD). Persisted in etcd with the rest of this object and
	// re-imported on update/delete.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	EngineState *apiextensionsv1.JSON `json:"engineState,omitempty"`

	// Conditions represent the latest available observations of the
	// resource's state (Ready, Synced).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=dores
// +kubebuilder:printcolumn:name="TYPE",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="ID",type=string,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="SYNCED",type=string,JSONPath=`.status.conditions[?(@.type=='Synced')].status`
// +kubebuilder:printcolumn:name="REASON",type=string,JSONPath=`.status.conditions[?(@.type=='Synced')].reason`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DoResource is the Schema for the doresources API. It represents a single
// cloud resource managed via `pulumi do`: spec.properties is the desired
// input state, status.outputs is the observed cloud state.
type DoResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DoResourceSpec   `json:"spec,omitempty"`
	Status DoResourceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DoResourceList contains a list of DoResource.
type DoResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoResource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DoResource{}, &DoResourceList{})
}
