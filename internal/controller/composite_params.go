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

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsvalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// validateParameters checks a composite's parameters against the
// definition's parameters schema — the same contract the generated CRD
// enforces at admission for typed objects, applied here so raw DoComposite
// users get identical validation at render time.
func validateParameters(schema *apiextensionsv1.JSONSchemaProps, params map[string]any) error {
	if schema == nil {
		return nil
	}
	internal := &apiextensions.JSONSchemaProps{}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(schema.DeepCopy(), internal, nil); err != nil {
		return fmt.Errorf("converting parameters schema: %w", err)
	}
	validator, _, err := apiextensionsvalidation.NewSchemaValidator(internal)
	if err != nil {
		return fmt.Errorf("compiling parameters schema: %w", err)
	}
	if errs := apiextensionsvalidation.ValidateCustomResource(field.NewPath("spec", "parameters"), params, validator); len(errs) > 0 {
		return errs.ToAggregate()
	}
	return nil
}
