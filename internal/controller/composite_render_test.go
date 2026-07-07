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
	"encoding/json"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	dov1alpha1 "github.com/dirien/pulumi-do-operator/api/v1alpha1"
	"github.com/dirien/pulumi-do-operator/internal/pulumido"
)

func jsonRaw(t *testing.T, v any) *apiextensionsv1.JSON {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return &apiextensionsv1.JSON{Raw: raw}
}

func testComposite(t *testing.T, params any) *dov1alpha1.DoComposite {
	t.Helper()
	return &dov1alpha1.DoComposite{
		ObjectMeta: metav1.ObjectMeta{Name: "site", Namespace: "default"},
		Spec:       dov1alpha1.DoCompositeSpec{Definition: "website", Parameters: jsonRaw(t, params)},
	}
}

func TestRenderComposite(t *testing.T) {
	def := &dov1alpha1.DoCompositeDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "website"},
		Spec: dov1alpha1.DoCompositeDefinitionSpec{
			RequiredParameters: []string{"env"},
			Resources: []dov1alpha1.CompositeResourceTemplate{
				{
					Name: "bucket",
					Type: "aws:s3/bucketV2:BucketV2",
					Properties: jsonRaw(t, map[string]any{
						"bucket": "${self.name}-${params.env}",
						"tags":   map[string]any{"env": "${params.env}", "literal": "$${not-an-expr}"},
					}),
				},
				{
					Name: "policy",
					Type: "aws:s3/bucketPolicy:BucketPolicy",
					Properties: jsonRaw(t, map[string]any{
						"bucket": "${resources.bucket.outputs.bucket}",
						// The same expression may repeat; it collapses into
						// one reference with a multi-${value} template.
						"policy": `{"Resource":["${resources.bucket.outputs.arn}","${resources.bucket.outputs.arn}/*"]}`,
					}),
				},
			},
		},
	}
	comp := testComposite(t, map[string]any{"env": "prod"})

	children, err := renderComposite(comp, def)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("want 2 children, got %d", len(children))
	}

	bucket := children[0]
	if bucket.Name != "site-bucket" || bucket.Namespace != "default" {
		t.Errorf("bucket identity: %s/%s", bucket.Namespace, bucket.Name)
	}
	var bucketProps map[string]any
	if err := json.Unmarshal(bucket.Spec.Properties.Raw, &bucketProps); err != nil {
		t.Fatal(err)
	}
	if bucketProps["bucket"] != "site-prod" {
		t.Errorf("params/self substitution: %v", bucketProps["bucket"])
	}
	tags := bucketProps["tags"].(map[string]any)
	if tags["env"] != "prod" || tags["literal"] != "${not-an-expr}" {
		t.Errorf("tags rendering: %v", tags)
	}
	if len(bucket.Spec.References) != 0 {
		t.Errorf("bucket should have no references, got %v", bucket.Spec.References)
	}

	policy := children[1]
	if len(policy.Spec.References) != 2 {
		t.Fatalf("policy should have 2 references, got %v", policy.Spec.References)
	}
	byPath := map[string]dov1alpha1.Reference{}
	for _, ref := range policy.Spec.References {
		byPath[ref.ToPath] = ref
	}
	if ref := byPath["bucket"]; ref.From.Name != "site-bucket" || ref.From.FieldPath != "status.outputs.bucket" || ref.Template != "" {
		t.Errorf("bucket ref: %+v", ref)
	}
	if ref := byPath["policy"]; ref.From.FieldPath != "status.outputs.arn" ||
		ref.Template != `{"Resource":["${value}","${value}/*"]}` {
		t.Errorf("policy ref: %+v", ref)
	}
	var policyProps map[string]any
	if err := json.Unmarshal(policy.Spec.Properties.Raw, &policyProps); err != nil {
		t.Fatal(err)
	}
	if _, exists := policyProps["bucket"]; exists {
		t.Errorf("referenced property must be removed from literals: %v", policyProps)
	}
}

func TestRenderCompositeTypedParam(t *testing.T) {
	def := &dov1alpha1.DoCompositeDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "website"},
		Spec: dov1alpha1.DoCompositeDefinitionSpec{
			Resources: []dov1alpha1.CompositeResourceTemplate{{
				Name: "pet",
				Type: "random:index/randomPet:RandomPet",
				Properties: jsonRaw(t, map[string]any{
					"length": "${params.length}",
				}),
			}},
		},
	}
	comp := testComposite(t, map[string]any{"length": 4})
	children, err := renderComposite(comp, def)
	if err != nil {
		t.Fatal(err)
	}
	var props map[string]any
	if err := json.Unmarshal(children[0].Spec.Properties.Raw, &props); err != nil {
		t.Fatal(err)
	}
	if props["length"] != float64(4) {
		t.Errorf("whole-string param must keep its type, got %T %v", props["length"], props["length"])
	}
}

func TestChildResourceName(t *testing.T) {
	if got := childResourceName("site", "bucket"); got != "site-bucket" {
		t.Errorf("short name: %q", got)
	}
	long := strings.Repeat("a", 250) + "-x." // truncation point lands on punctuation
	nameA := childResourceName(long, "bucket")
	nameB := childResourceName(long, "policy")
	for _, n := range []string{nameA, nameB} {
		if errs := validation.IsDNS1123Subdomain(n); len(errs) > 0 || len(n) > 253 {
			t.Errorf("invalid child name %q: %v", n, errs)
		}
	}
	if nameA == nameB {
		t.Errorf("long composite names must keep distinct suffixes: %q", nameA)
	}
	if !strings.HasSuffix(nameA, "-bucket") || !strings.HasSuffix(nameB, "-policy") {
		t.Errorf("resource suffix must survive truncation: %q, %q", nameA, nameB)
	}
	// Different composites truncating to the same prefix must not collide.
	other := childResourceName(strings.Repeat("a", 250)+"-y.", "bucket")
	if other == nameA {
		t.Errorf("hash must disambiguate truncated composite names: %q", other)
	}
}

func TestCompositeLabelValue(t *testing.T) {
	if got := compositeLabelValue("site"); got != "site" {
		t.Errorf("short value: %q", got)
	}
	long := strings.Repeat("b", 100)
	v := compositeLabelValue(long)
	if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
		t.Errorf("invalid label value %q: %v", v, errs)
	}
	if compositeLabelValue(strings.Repeat("b", 101)) == v {
		t.Error("different names must map to different label values")
	}
}

func TestRenderCompositeDottedKeysAndEscapes(t *testing.T) {
	def := &dov1alpha1.DoCompositeDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "website"},
		Spec: dov1alpha1.DoCompositeDefinitionSpec{
			Resources: []dov1alpha1.CompositeResourceTemplate{
				{Name: "vpc", Type: "aws:ec2/vpc:Vpc"},
				{
					Name: "bucket",
					Type: "aws:s3/bucketV2:BucketV2",
					Properties: jsonRaw(t, map[string]any{
						"tags": map[string]any{
							"kubernetes.io/cluster/prod": "${resources.vpc.id}",
						},
						// A literal ${value} via escape, mixed into a template.
						"note": "$${value} is literal, id is ${resources.vpc.id}",
					}),
				},
			},
		},
	}
	children, err := renderComposite(testComposite(t, map[string]any{}), def)
	if err != nil {
		t.Fatal(err)
	}
	bucket := children[1]
	byPath := map[string]dov1alpha1.Reference{}
	for _, ref := range bucket.Spec.References {
		byPath[ref.ToPath] = ref
	}
	if _, ok := byPath[`tags["kubernetes.io/cluster/prod"]`]; !ok {
		t.Fatalf("dotted key must use quoted ToPath, got %v", bucket.Spec.References)
	}
	noteRef, ok := byPath["note"]
	if !ok || noteRef.Template != "$${value} is literal, id is ${value}" {
		t.Fatalf("escaped literal must survive templating: %+v", noteRef)
	}
	// The full chain: resolving the template yields the literal ${value}.
	if got := expandTemplate(noteRef.Template, "vpc-123"); got != "${value} is literal, id is vpc-123" {
		t.Errorf("expandTemplate: %q", got)
	}

	// Round-trip: the quoted ToPath must address the original key.
	var props map[string]any
	if err := json.Unmarshal(bucket.Spec.Properties.Raw, &props); err != nil {
		t.Fatal(err)
	}
	if err := pulumido.SetPath(props, `tags["kubernetes.io/cluster/prod"]`, "vpc-1"); err != nil {
		t.Fatal(err)
	}
	if props["tags"].(map[string]any)["kubernetes.io/cluster/prod"] != "vpc-1" {
		t.Errorf("resolved value landed wrong: %v", props)
	}
}

func TestRenderCompositeUnterminatedExpression(t *testing.T) {
	def := &dov1alpha1.DoCompositeDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "website"},
		Spec: dov1alpha1.DoCompositeDefinitionSpec{
			Resources: []dov1alpha1.CompositeResourceTemplate{{
				Name: "a", Type: "t:m:R",
				Properties: jsonRaw(t, map[string]any{"x": "${resources.a.id"}),
			}},
		},
	}
	if _, err := renderComposite(testComposite(t, map[string]any{}), def); err == nil ||
		!strings.Contains(err.Error(), "unterminated") {
		t.Errorf("want unterminated-expression error, got %v", err)
	}
}

func TestRenderCompositeRejectsBadResourceName(t *testing.T) {
	def := &dov1alpha1.DoCompositeDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "website"},
		Spec: dov1alpha1.DoCompositeDefinitionSpec{
			Resources: []dov1alpha1.CompositeResourceTemplate{{
				Name: strings.Repeat("x", 64), Type: "t:m:R",
			}},
		},
	}
	if _, err := renderComposite(testComposite(t, map[string]any{}), def); err == nil ||
		!strings.Contains(err.Error(), "DNS label") {
		t.Errorf("want DNS label error, got %v", err)
	}
}

func TestRenderCompositeErrors(t *testing.T) {
	base := func(props map[string]any) *dov1alpha1.DoCompositeDefinition {
		return &dov1alpha1.DoCompositeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "website"},
			Spec: dov1alpha1.DoCompositeDefinitionSpec{
				Resources: []dov1alpha1.CompositeResourceTemplate{{
					Name: "a", Type: "t:m:R", Properties: jsonRaw(t, props),
				}},
			},
		}
	}
	cases := []struct {
		name    string
		def     *dov1alpha1.DoCompositeDefinition
		params  any
		wantErr string
	}{
		{"unknown resource", base(map[string]any{"x": "${resources.nope.id}"}), map[string]any{}, "unknown resource"},
		{"two resource exprs", base(map[string]any{"x": "${resources.a.id}-${resources.a.outputs.b}"}), map[string]any{}, "two different"},
		{"missing param", base(map[string]any{"x": "${params.gone}"}), map[string]any{}, "parameter not found"},
		{"bad expr", base(map[string]any{"x": "${bogus.thing}"}), map[string]any{}, "unsupported expression"},
		{"missing required", &dov1alpha1.DoCompositeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "website"},
			Spec: dov1alpha1.DoCompositeDefinitionSpec{
				RequiredParameters: []string{"env"},
				Resources:          []dov1alpha1.CompositeResourceTemplate{{Name: "a", Type: "t:m:R"}},
			},
		}, map[string]any{}, "required parameter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := renderComposite(testComposite(t, c.params), c.def)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}
