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

// Condition types reported by the DoProvider controller, in addition to
// ConditionReady.
const (
	// ConditionSchemaFetched indicates the provider schema was retrieved
	// from the Pulumi registry.
	ConditionSchemaFetched = "SchemaFetched"
	// ConditionPluginReady indicates the provider plugin is available to
	// runner Jobs (shared cache, baked into the image, or on-demand).
	ConditionPluginReady = "PluginReady"
	// ConditionCredentialsReady indicates the configured credentials Secret
	// exists and holds every required key.
	ConditionCredentialsReady = "CredentialsReady"
)

// LocalSecretReference names a Secret in the runner namespace.
type LocalSecretReference struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// DoProviderSpec declares platform-level support for one provider package:
// the pinned version, where its credentials live, and which resources app
// teams may use. DoResources opt in via spec.providerRef.
type DoProviderSpec struct {
	// Package pins the provider package, in the form "name@version"
	// (e.g. "aws@7.34.0"). An unpinned "name" works but makes operations
	// non-reproducible and is surfaced with a warning.
	// +kubebuilder:validation:MinLength=1
	Package string `json:"package"`

	// CredentialsSecretRef names the Secret (in the runner namespace) whose
	// keys become environment variables of runner pods executing operations
	// for this provider. Empty keeps the deployment-wide default Secret.
	// +optional
	CredentialsSecretRef *LocalSecretReference `json:"credentialsSecretRef,omitempty"`

	// CredentialKeys lists keys that must exist in the credentials Secret
	// (e.g. DIGITALOCEAN_TOKEN). Missing keys surface as
	// CredentialsReady=False.
	// +optional
	CredentialKeys []string `json:"credentialKeys,omitempty"`

	// AllowedResources restricts which resource type tokens may use this
	// provider. Entries are full tokens ("aws:s3/bucketV2:BucketV2"),
	// module paths ("s3/bucketV2"), module globs ("s3/*") or "*". Empty
	// allows every resource of the package.
	// +optional
	AllowedResources []string `json:"allowedResources,omitempty"`
}

// ProviderPackageStatus reports the resolved package coordinates.
type ProviderPackageStatus struct {
	// Name of the provider package.
	// +optional
	Name string `json:"name,omitempty"`
	// Version of the provider package as reported by its schema.
	// +optional
	Version string `json:"version,omitempty"`
}

// ProviderPluginStatus reports plugin availability for runner Jobs.
type ProviderPluginStatus struct {
	// Ready is true when runner Jobs can execute operations for this
	// provider without manual plugin installation.
	// +optional
	Ready bool `json:"ready,omitempty"`
	// CachePath is the shared plugin cache mount inside runner pods, when
	// the cache is enabled.
	// +optional
	CachePath string `json:"cachePath,omitempty"`
}

// DoProviderStatus reports the outcome of provider validation.
type DoProviderStatus struct {
	// Package is the resolved package name and version.
	// +optional
	Package *ProviderPackageStatus `json:"package,omitempty"`

	// Plugin reports plugin availability.
	// +optional
	Plugin *ProviderPluginStatus `json:"plugin,omitempty"`

	// LastSchemaFetchTime is when the provider schema last validated
	// successfully.
	// +optional
	LastSchemaFetchTime *metav1.Time `json:"lastSchemaFetchTime,omitempty"`

	// Dependents counts the DoResources currently referencing this
	// profile.
	// +optional
	Dependents int32 `json:"dependents,omitempty"`

	// ObservedGeneration is the spec generation last reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest observations (Ready, SchemaFetched,
	// PluginReady, CredentialsReady).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=doprov
// +kubebuilder:printcolumn:name="PACKAGE",type=string,JSONPath=`.spec.package`
// +kubebuilder:printcolumn:name="READY",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].status`
// +kubebuilder:printcolumn:name="REASON",type=string,JSONPath=`.status.conditions[?(@.type=='Ready')].reason`
// +kubebuilder:printcolumn:name="DEPENDENTS",type=integer,JSONPath=`.status.dependents`
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=`.metadata.creationTimestamp`

// DoProvider is a cluster-scoped, platform-team-owned provider profile:
// add a provider once, and app teams consume it through
// DoResource.spec.providerRef or curated composites without learning
// plugin versions or credential names.
type DoProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DoProviderSpec   `json:"spec,omitempty"`
	Status DoProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DoProviderList contains a list of DoProvider.
type DoProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DoProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DoProvider{}, &DoProviderList{})
}
