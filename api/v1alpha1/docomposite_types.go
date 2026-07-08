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

// DoCompositeDefinitionSpec declares a reusable template that expands one
// DoComposite into a graph of DoResources. Property values may contain
// expressions:
//
//	${params.<path>}                       composite parameter
//	${self.name} / ${self.namespace}       composite identity
//	${resources.<name>.outputs.<path>}     sibling resource output
//	${resources.<name>.id}                 sibling resource id
//
// A resources.* expression compiles into a DoResource reference, so
// ordering, readiness gating, propagation and ordered teardown are handled
// by the resource graph engine. "$${" escapes a literal "${".
type DoCompositeDefinitionSpec struct {
	// RequiredParameters lists parameter keys every DoComposite using this
	// definition must provide.
	// +optional
	RequiredParameters []string `json:"requiredParameters,omitempty"`

	// Resources are the templates rendered into DoResources.
	// +kubebuilder:validation:MinItems=1
	Resources []CompositeResourceTemplate `json:"resources"`

	// API exposes this definition as its own typed, namespaced CRD (e.g.
	// `kind: StaticSite`): users apply the platform kind with their
	// parameters as spec, instead of a generic DoComposite. Each typed
	// object is translated into an owned DoComposite.
	// +optional
	API *CompositeAPI `json:"api,omitempty"`
}

// CompositeAPI describes the typed CRD generated for a definition.
type CompositeAPI struct {
	// Kind of the generated API (e.g. StaticSite).
	// +kubebuilder:validation:Pattern=`^[A-Z][A-Za-z0-9]*$`
	Kind string `json:"kind"`

	// Plural resource name; defaults to lowercase(kind) + "s".
	// +optional
	Plural string `json:"plural,omitempty"`

	// ParametersSchema is an OpenAPI v3 schema (JSONSchemaProps) validating
	// the typed object's spec — the composite parameters. Empty accepts
	// any object.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	ParametersSchema *apiextensionsv1.JSON `json:"parametersSchema,omitempty"`
}

// CompositeResourceTemplate templates one DoResource of a composite.
type CompositeResourceTemplate struct {
	// Name identifies the resource within the composite; it is referenced
	// by sibling expressions and suffixes the child object's name.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Name string `json:"name"`

	// Type is the Pulumi resource type token.
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	// Package pins the provider package (e.g. "aws@7.34.0").
	// +optional
	Package string `json:"package,omitempty"`

	// ProviderRef resolves package and credentials from a cluster-scoped
	// DoProvider profile; copied verbatim into the child DoResource.
	// +optional
	ProviderRef *ProviderReference `json:"providerRef,omitempty"`

	// DeletionPolicy for the child resource. Defaults to Delete.
	// +kubebuilder:validation:Enum=Delete;Orphan
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`

	// Properties are the resource inputs; string values may contain
	// expressions (see DoCompositeDefinitionSpec).
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Properties *apiextensionsv1.JSON `json:"properties,omitempty"`
}

// DoCompositeDefinitionStatus reports observed state of a definition.
type DoCompositeDefinitionStatus struct {
	// Composites is the number of DoComposites currently using this
	// definition.
	// +optional
	Composites int32 `json:"composites,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=docd
// +kubebuilder:printcolumn:name="COMPOSITES",type=integer,JSONPath=`.status.composites`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DoCompositeDefinition is a cluster-scoped, platform-team-owned template
// for a graph of cloud resources.
type DoCompositeDefinition struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DoCompositeDefinitionSpec   `json:"spec,omitempty"`
	Status DoCompositeDefinitionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DoCompositeDefinitionList contains a list of DoCompositeDefinition.
type DoCompositeDefinitionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoCompositeDefinition `json:"items"`
}

// UpdatePolicy controls how a composite follows definition edits.
type UpdatePolicy string

const (
	// UpdateAutomatic tracks the definition's latest revision.
	UpdateAutomatic UpdatePolicy = "Automatic"
	// UpdateManual stays on the recorded (or explicitly pinned) revision
	// until a human moves it.
	UpdateManual UpdatePolicy = "Manual"
)

// DoCompositeSpec instantiates a DoCompositeDefinition.
type DoCompositeSpec struct {
	// Definition is the name of the cluster-scoped DoCompositeDefinition
	// to expand.
	// +kubebuilder:validation:MinLength=1
	Definition string `json:"definition"`

	// Parameters feed the definition's ${params.*} expressions.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Parameters *apiextensionsv1.JSON `json:"parameters,omitempty"`

	// UpdatePolicy decides whether definition edits roll out to this
	// instance: Automatic follows the latest revision; Manual keeps the
	// revision recorded at first render until revisionRef moves it.
	// +kubebuilder:validation:Enum=Automatic;Manual
	// +kubebuilder:default=Automatic
	// +optional
	UpdatePolicy UpdatePolicy `json:"updatePolicy,omitempty"`

	// RevisionRef pins rendering to one specific
	// DoCompositeDefinitionRevision, overriding updatePolicy.
	// +optional
	RevisionRef *RevisionReference `json:"revisionRef,omitempty"`
}

// RevisionReference names a DoCompositeDefinitionRevision.
type RevisionReference struct {
	// Name of the revision (e.g. "static-site-v3").
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// CompositeResourceStatus reports one child resource of a composite.
type CompositeResourceStatus struct {
	// Name is the template resource name.
	Name string `json:"name"`
	// ResourceName is the child DoResource object name.
	ResourceName string `json:"resourceName"`
	// Ready mirrors the child's Ready condition.
	Ready bool `json:"ready"`
	// ID is the child's external resource id, once known.
	// +optional
	ID string `json:"id,omitempty"`
}

// DoCompositeStatus is the observed state of a DoComposite.
type DoCompositeStatus struct {
	// Resources reports each child DoResource. Every underlying Pulumi
	// resource is itself a Kubernetes object; this is the roll-up.
	// +optional
	Resources []CompositeResourceStatus `json:"resources,omitempty"`

	// ReadyResources counts children whose Ready condition is True,
	// rendered as "ready/total".
	// +optional
	ReadyResources string `json:"readyResources,omitempty"`

	// Revision names the DoCompositeDefinitionRevision last rendered.
	// +optional
	Revision string `json:"revision,omitempty"`

	// ObservedGeneration is the composite generation last rendered.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions (Ready, Synced).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=docomp
// +kubebuilder:printcolumn:name="DEFINITION",type=string,JSONPath=`.spec.definition`
// +kubebuilder:printcolumn:name="RESOURCES",type=string,JSONPath=`.status.readyResources`
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="SYNCED",type=string,JSONPath=`.status.conditions[?(@.type=='Synced')].status`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DoComposite instantiates a DoCompositeDefinition into a graph of child
// DoResources, each visible as its own Kubernetes object.
type DoComposite struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DoCompositeSpec   `json:"spec,omitempty"`
	Status DoCompositeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DoCompositeList contains a list of DoComposite.
type DoCompositeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoComposite `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&DoComposite{}, &DoCompositeList{},
		&DoCompositeDefinition{}, &DoCompositeDefinitionList{},
	)
}
