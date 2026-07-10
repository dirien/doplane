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
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.api) || has(self.api)",message="spec.api cannot be removed once set; delete the definition instead"
type DoCompositeDefinitionSpec struct {
	// Resources are the templates rendered into DoResources.
	// +kubebuilder:validation:MinItems=1
	Resources []CompositeResourceTemplate `json:"resources"`

	// API exposes this definition as its own typed, namespaced CRD (e.g.
	// `kind: Website` in `platform.acme.com`): users apply the platform
	// kind with their parameters as spec, instead of a generic DoComposite.
	// Each typed object is translated into an owned DoComposite.
	// +optional
	API *CompositeAPI `json:"api,omitempty"`
}

// CompositeAPI describes the typed CRD generated for a definition.
// Group, kind and plural are immutable once set: a rename would strand the
// typed objects behind the old API, so a breaking change ships as a new
// definition and manifests migrate at the leaf.
//
// +kubebuilder:validation:XValidation:rule="self.kind == oldSelf.kind",message="spec.api.kind is immutable"
// +kubebuilder:validation:XValidation:rule="has(self.plural) == has(oldSelf.plural) && (!has(self.plural) || self.plural == oldSelf.plural)",message="spec.api.plural is immutable"
// +kubebuilder:validation:XValidation:rule="has(self.group) == has(oldSelf.group) && (!has(self.group) || self.group == oldSelf.group)",message="spec.api.group is immutable"
type CompositeAPI struct {
	// Group of the generated API (e.g. platform.acme.com). Must be on the
	// operator's install-time allowlist (Helm value compositeApiGroups),
	// which also renders the matching manager RBAC. Empty uses the fixed
	// typed.do.pulumi.com group.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)+$`
	// +optional
	Group string `json:"group,omitempty"`

	// Kind of the generated API (e.g. Website).
	// +kubebuilder:validation:Pattern=`^[A-Z][A-Za-z0-9]*$`
	Kind string `json:"kind"`

	// Plural resource name; defaults to lowercase(kind) + "s".
	// +optional
	Plural string `json:"plural,omitempty"`

	// Version served and referenced by the templates (storage version).
	// Defaults to v1alpha1. Bumping it keeps prior versions only if they
	// are listed in deprecatedVersions; generated CRDs use conversion
	// strategy None, so every served version must stay round-trippable —
	// a new required parameter is a new API, not a new version.
	// +kubebuilder:validation:Pattern=`^v[0-9]+((alpha|beta)[0-9]+)?$`
	// +optional
	Version string `json:"version,omitempty"`

	// DeprecatedVersions are previously served versions kept served (and
	// marked deprecated) while their objects migrate. Remove a version
	// only when the definition's status reports zero objects for it.
	// +optional
	// +listType=set
	DeprecatedVersions []string `json:"deprecatedVersions,omitempty"`

	// ParametersSchema is the OpenAPI v3 schema validating the typed
	// object's spec — the single source of the parameter contract. It also
	// validates DoComposite parameters at render time and template
	// ${params.*} usage at apply time. Empty accepts any object. The
	// property name "doplane" is reserved for doplane's lifecycle knobs.
	// +optional
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:pruning:PreserveUnknownFields
	ParametersSchema *apiextensionsv1.JSONSchemaProps `json:"parametersSchema,omitempty"`
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

	// ExternalName adopts an existing external resource instead of creating
	// one: rendered (params/self expressions allowed; sibling resources.*
	// are not) into the child's crossplane.io/external-name annotation.
	// +optional
	ExternalName string `json:"externalName,omitempty"`
}

// TypedAPIVersionStatus reports one served version of a definition's API.
type TypedAPIVersionStatus struct {
	// Name of the version (e.g. v1alpha1).
	Name string `json:"name"`
	// Deprecated marks a version served only for migration.
	// +optional
	Deprecated bool `json:"deprecated,omitempty"`
	// Objects counts typed objects whose manifests last wrote this
	// version. A deprecated version may be removed once this reaches zero.
	Objects int32 `json:"objects"`
}

// DoCompositeDefinitionStatus reports observed state of a definition.
type DoCompositeDefinitionStatus struct {
	// Composites is the number of DoComposites currently using this
	// definition.
	// +optional
	Composites int32 `json:"composites,omitempty"`

	// APIVersions reports each served version of the typed API and how
	// many objects still use it (the migration signal for version bumps).
	// +optional
	APIVersions []TypedAPIVersionStatus `json:"apiVersions,omitempty"`

	// Conditions (APIServed).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=docd
// +kubebuilder:printcolumn:name="COMPOSITES",type=integer,JSONPath=`.status.composites`
// +kubebuilder:printcolumn:name="SERVED",type=string,JSONPath=`.status.conditions[?(@.type=='APIServed')].status`
// +kubebuilder:printcolumn:name="REASON",type=string,JSONPath=`.status.conditions[?(@.type=='APIServed')].reason`
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
