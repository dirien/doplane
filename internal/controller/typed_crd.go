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

package controller

import (
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// typedGroup is the default API group of generated typed CRDs. Provider
// resource mirrors always live here; composite APIs may choose their own
// platform group, gated by the install-time allowlist that keeps manager
// RBAC enumerable.
const typedGroup = "typed.do.pulumi.com"

// typedVersion is the default served/storage version of generated CRDs.
const typedVersion = "v1alpha1"

// annTypedOwner persists which source a generated CRD belongs to
// ("resource:<token>" / "composite:<definition>"). Checked before any
// apply, so ownership survives manager restarts and a foreign or
// differently-owned CRD is never overwritten.
const annTypedOwner = "do.pulumi.com/owner"

// doplaneReservedProperty is the reserved spec block on typed composite
// objects carrying doplane's lifecycle knobs.
const doplaneReservedProperty = "doplane"

// kindFromToken derives the CRD kind from a resource token:
// "aws:s3/bucketV2:BucketV2" → "BucketV2".
func kindFromToken(token string) string {
	if i := strings.LastIndex(token, ":"); i >= 0 {
		return token[i+1:]
	}
	return token
}

// pluralize lowercases a kind into a naive plural resource name.
func pluralize(kind string) string {
	lower := strings.ToLower(kind)
	switch {
	case strings.HasSuffix(lower, "s"), strings.HasSuffix(lower, "x"), strings.HasSuffix(lower, "ch"):
		return lower + "es"
	case strings.HasSuffix(lower, "y") && len(lower) > 1 && !strings.ContainsRune("aeiou", rune(lower[len(lower)-2])):
		return lower[:len(lower)-1] + "ies"
	default:
		return lower + "s"
	}
}

// schemaTypeObject is the OpenAPI type name of object nodes.
const schemaTypeObject = "object"

// propsToJSONSchema converts a Pulumi property schema into the structural
// OpenAPI subset CRDs accept. Unresolvable shapes ($ref, oneOf, unknown
// types) degrade to a value with unknown fields preserved — validation is
// still enforced end to end by the controller's registry-schema check.
func propsToJSONSchema(p pulumido.PropertySchema) apiextensionsv1.JSONSchemaProps {
	switch p.Type {
	case "string", "boolean", "integer", "number":
		return apiextensionsv1.JSONSchemaProps{Type: p.Type}
	case "array":
		items := apiextensionsv1.JSONSchemaProps{XPreserveUnknownFields: ptr.To(true)}
		if p.Items != nil {
			items = propsToJSONSchema(*p.Items)
		}
		return apiextensionsv1.JSONSchemaProps{
			Type:  "array",
			Items: &apiextensionsv1.JSONSchemaPropsOrArray{Schema: &items},
		}
	case schemaTypeObject:
		obj := apiextensionsv1.JSONSchemaProps{Type: schemaTypeObject}
		if p.AdditionalProperties != nil {
			ap := propsToJSONSchema(*p.AdditionalProperties)
			obj.AdditionalProperties = &apiextensionsv1.JSONSchemaPropsOrBool{Schema: &ap}
		} else {
			obj.XPreserveUnknownFields = ptr.To(true)
		}
		return obj
	default:
		// $ref, oneOf and friends: accept anything at this node.
		return apiextensionsv1.JSONSchemaProps{XPreserveUnknownFields: ptr.To(true)}
	}
}

// typedResourceCRD builds the CRD for one provider resource token: spec
// carries providerRef/forProvider/deletionPolicy, forProvider validated
// from the provider's input schema.
func typedResourceCRD(token string, schema *pulumido.PackageSchema) (*apiextensionsv1.CustomResourceDefinition, error) {
	res, ok := schema.Resources[token]
	if !ok {
		return nil, fmt.Errorf("resource type %q not found in schema of %s@%s", token, schema.Name, schema.Version)
	}
	kind := kindFromToken(token)
	plural := pluralize(kind)

	forProvider := apiextensionsv1.JSONSchemaProps{
		Type:       schemaTypeObject,
		Properties: map[string]apiextensionsv1.JSONSchemaProps{},
		Required:   append([]string(nil), res.RequiredInputs...),
	}
	for name, prop := range res.InputProperties {
		forProvider.Properties[name] = propsToJSONSchema(prop)
	}

	spec := apiextensionsv1.JSONSchemaProps{
		Type:     schemaTypeObject,
		Required: []string{"forProvider"},
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"providerRef": {
				Type:     schemaTypeObject,
				Required: []string{"name"},
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"name": {Type: "string", MinLength: ptr.To(int64(1))},
					"kind": {Type: "string", Enum: []apiextensionsv1.JSON{
						{Raw: []byte(`"DoProvider"`)}, {Raw: []byte(`"DoProviderConfig"`)},
					}},
				},
			},
			"forProvider":    forProvider,
			"deletionPolicy": {Type: "string", Enum: []apiextensionsv1.JSON{{Raw: []byte(`"Delete"`)}, {Raw: []byte(`"Orphan"`)}}},
		},
	}
	source := "resource"
	if res.IsComponent {
		source = "component"
	}
	crd := typedCRD(typedGroup, kind, plural, []crdVersion{{name: typedVersion, storage: true}}, spec,
		fmt.Sprintf("Generated by doplane from Pulumi %s %s (%s@%s).", source, token, schema.Name, schema.Version))
	crd.Annotations = map[string]string{annTypedOwner: "resource:" + token}
	return crd, nil
}

// compositeAPIGroup returns the effective group of a definition's API.
func compositeAPIGroup(api *dov1alpha1.CompositeAPI) string {
	if api.Group != "" {
		return api.Group
	}
	return typedGroup
}

// compositeAPIVersion returns the effective storage version of a
// definition's API — the one its templates reference.
func compositeAPIVersion(api *dov1alpha1.CompositeAPI) string {
	if api.Version != "" {
		return api.Version
	}
	return typedVersion
}

// compositeAPIPlural returns the effective plural of a definition's API.
func compositeAPIPlural(api *dov1alpha1.CompositeAPI) string {
	if api.Plural != "" {
		return api.Plural
	}
	return pluralize(api.Kind)
}

// doplaneBlockSchema is the reserved spec.doplane block injected into every
// generated composite schema: typed users get the same lifecycle knobs as
// raw DoComposites (typed parity), visible to kubectl explain.
func doplaneBlockSchema() apiextensionsv1.JSONSchemaProps {
	return apiextensionsv1.JSONSchemaProps{
		Type:        schemaTypeObject,
		Description: "Reserved doplane lifecycle knobs (not platform parameters).",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"updatePolicy": {
				Type:        "string",
				Description: "Automatic follows the definition's latest revision; Manual stays pinned.",
				Enum:        []apiextensionsv1.JSON{{Raw: []byte(`"Automatic"`)}, {Raw: []byte(`"Manual"`)}},
			},
			"revisionRef": {
				Type:        schemaTypeObject,
				Description: "Pins rendering to one DoCompositeDefinitionRevision.",
				Required:    []string{"name"},
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"name": {Type: "string", MinLength: ptr.To(int64(1))},
				},
			},
		},
	}
}

// typedCompositeCRD builds the CRD for a definition's platform API: the
// typed object's spec is the composite's parameters plus the reserved
// doplane block. All versions (current + deprecated) serve the same
// schema — conversion strategy is None, so schemas must be round-trippable
// anyway and the current schema accepts every stored object.
func typedCompositeCRD(def string, api *dov1alpha1.CompositeAPI) (*apiextensionsv1.CustomResourceDefinition, error) {
	spec := apiextensionsv1.JSONSchemaProps{Type: schemaTypeObject, XPreserveUnknownFields: ptr.To(true)}
	if api.ParametersSchema != nil {
		spec = *api.ParametersSchema.DeepCopy()
		if spec.Type == "" {
			spec.Type = schemaTypeObject
		}
		if spec.Type != schemaTypeObject {
			return nil, fmt.Errorf("spec.api.parametersSchema must describe an object, not %q", spec.Type)
		}
	}
	if _, taken := spec.Properties[doplaneReservedProperty]; taken {
		return nil, fmt.Errorf("spec.api.parametersSchema declares a parameter named %q; that name is reserved for doplane's lifecycle knobs", doplaneReservedProperty)
	}
	if spec.Properties == nil {
		spec.Properties = map[string]apiextensionsv1.JSONSchemaProps{}
	}
	spec.Properties[doplaneReservedProperty] = doplaneBlockSchema()

	current := compositeAPIVersion(api)
	versions := []crdVersion{{name: current, storage: true}}
	for _, v := range api.DeprecatedVersions {
		if v == current {
			return nil, fmt.Errorf("spec.api.version %q cannot also be listed in deprecatedVersions", v)
		}
		versions = append(versions, crdVersion{name: v, deprecated: true})
	}

	crd := typedCRD(compositeAPIGroup(api), api.Kind, compositeAPIPlural(api), versions, spec,
		fmt.Sprintf("Generated by doplane from DoCompositeDefinition %q.", def))
	crd.Annotations = map[string]string{annTypedOwner: "composite:" + def}
	return crd, nil
}

// crdVersion describes one served version of a generated CRD.
type crdVersion struct {
	name       string
	storage    bool
	deprecated bool
}

// typedCRD assembles the shared CRD shell: namespaced, status subresource,
// printer columns mirroring the underlying object's conditions. Conversion
// strategy is always None (the Kubernetes alternative is a conversion
// webhook, whose TLS plumbing and availability coupling were rejected), so
// every served version shares the one schema.
//
// Deliberately NO OwnerReference to the generating DoProvider/
// DoCompositeDefinition. Both are cluster-scoped, so a cross-object owner ref
// is technically valid and would let the garbage collector clean up the CRD on
// source deletion — but deleting a served CRD deletes every CR of that kind,
// each of which owns a mirror DoResource whose finalizer tears down a real
// cloud resource. Cascade GC would therefore turn an accidental
// `kubectl delete doprovider …` into mass destruction of managed
// infrastructure. Fail-safe wins here. Lifecycle cleanup for composite APIs
// is the definition finalizer: it BLOCKS deletion while typed CRs exist and
// only then deletes the CRD — never a plain owner-ref cascade.
func typedCRD(group, kind, plural string, versions []crdVersion,
	spec apiextensionsv1.JSONSchemaProps, description string,
) *apiextensionsv1.CustomResourceDefinition {
	crdVersions := make([]apiextensionsv1.CustomResourceDefinitionVersion, 0, len(versions))
	for _, v := range versions {
		version := apiextensionsv1.CustomResourceDefinitionVersion{
			Name:       v.name,
			Served:     true,
			Storage:    v.storage,
			Deprecated: v.deprecated,
			Subresources: &apiextensionsv1.CustomResourceSubresources{
				Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
			},
			AdditionalPrinterColumns: []apiextensionsv1.CustomResourceColumnDefinition{
				{Name: "READY", Type: "string", JSONPath: `.status.conditions[?(@.type=='Ready')].status`},
				{Name: "SYNCED", Type: "string", JSONPath: `.status.conditions[?(@.type=='Synced')].status`},
				{Name: "REASON", Type: "string", JSONPath: `.status.conditions[?(@.type=='Synced')].reason`},
				{Name: "AGE", Type: "date", JSONPath: `.metadata.creationTimestamp`},
			},
			Schema: &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type:        schemaTypeObject,
					Description: description,
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"spec":   spec,
						"status": {Type: schemaTypeObject, XPreserveUnknownFields: ptr.To(true)},
					},
				},
			},
		}
		crdVersions = append(crdVersions, version)
	}
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:   plural + "." + group,
			Labels: map[string]string{labelManagedByKey: "doplane"},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     kind,
				ListKind: kind + "List",
				Plural:   plural,
				Singular: strings.ToLower(kind),
			},
			Versions: crdVersions,
		},
	}
}
