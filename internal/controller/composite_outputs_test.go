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

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

func TestRenderOutputs(t *testing.T) {
	def := &dov1alpha1.DoCompositeDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: dov1alpha1.DoCompositeDefinitionSpec{
			Resources: []dov1alpha1.CompositeResourceTemplate{
				{Name: "droplet", Type: "digitalocean:index/droplet:Droplet"},
				{Name: "vpc", Type: "digitalocean:index/vpc:Vpc"},
			},
			Outputs: jsonRaw(t, map[string]any{
				"url":       "http://${resources.droplet.outputs.ipv4Address}",
				"ip":        "${resources.droplet.outputs.ipv4Address}",
				"port":      "${resources.droplet.outputs.port}",
				"id":        "${resources.droplet.id}",
				"who":       "${self.namespace}/${self.name} (${params.env})",
				"literal":   "$${not-an-expr}",
				"twoSource": "${resources.droplet.outputs.ipv4Address} in ${resources.vpc.outputs.ipRange}",
				"nested":    map[string]any{"vpc": "${resources.vpc.id}", "tags": []any{"${params.env}", "static"}},
			}),
		},
	}
	comp := testComposite(t, map[string]any{"env": "dev"})
	dropletRaw, _ := json.Marshal(map[string]any{"ipv4Address": "203.0.113.7", "port": 80})
	droplet := &dov1alpha1.DoResource{Status: dov1alpha1.DoResourceStatus{
		ID: "12345", Outputs: &apiextensionsv1.JSON{Raw: dropletRaw},
	}}

	t.Run("pending until the sources exist", func(t *testing.T) {
		got, pending, err := renderOutputs(comp, def, map[string]*dov1alpha1.DoResource{"droplet": droplet})
		if err != nil {
			t.Fatal(err)
		}
		if got["url"] != "http://203.0.113.7" || got["ip"] != "203.0.113.7" || got["id"] != "12345" {
			t.Errorf("resolved outputs: %v", got)
		}
		if n, ok := got["port"].(json.Number); !ok || n.String() != "80" {
			t.Errorf("a whole-string expression keeps the source's native type, got %T %v", got["port"], got["port"])
		}
		if got["who"] != "default/site (dev)" || got["literal"] != "${not-an-expr}" {
			t.Errorf("params/self/escape rendering: %v", got)
		}
		for _, absent := range []string{"twoSource", "nested"} {
			if _, ok := got[absent]; ok {
				t.Errorf("%s depends on the vpc child and must be absent until it exists", absent)
			}
		}
		if len(pending) != 2 || !strings.Contains(pending[0], "nested (vpc not yet applied)") ||
			!strings.Contains(pending[1], "twoSource (vpc not yet applied)") {
			t.Errorf("pending outputs: %v", pending)
		}
	})

	t.Run("resolved once every source is available", func(t *testing.T) {
		vpcRaw, _ := json.Marshal(map[string]any{"ipRange": "10.42.0.0/24"})
		vpc := &dov1alpha1.DoResource{Status: dov1alpha1.DoResourceStatus{ID: "vpc-1", Outputs: &apiextensionsv1.JSON{Raw: vpcRaw}}}
		got, pending, err := renderOutputs(comp, def, map[string]*dov1alpha1.DoResource{"droplet": droplet, "vpc": vpc})
		if err != nil {
			t.Fatal(err)
		}
		if len(pending) != 0 {
			t.Errorf("nothing should be pending: %v", pending)
		}
		if got["twoSource"] != "203.0.113.7 in 10.42.0.0/24" {
			t.Errorf("several sources in one string: %v", got["twoSource"])
		}
		nested, _ := got["nested"].(map[string]any)
		tags, _ := nested["tags"].([]any)
		if nested["vpc"] != "vpc-1" || len(tags) != 2 || tags[0] != "dev" || tags[1] != "static" {
			t.Errorf("nested output: %v", nested)
		}
	})

	t.Run("a child without an id yet is pending, not an error", func(t *testing.T) {
		got, pending, err := renderOutputs(comp, def, map[string]*dov1alpha1.DoResource{
			"droplet": {}, "vpc": {},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got["who"] == nil || got["literal"] == nil {
			t.Errorf("only the source-free outputs resolve against empty children: %v", got)
		}
		if len(pending) != 6 {
			t.Errorf("pending: %v", pending)
		}
	})
}

func TestRenderOutputsErrors(t *testing.T) {
	def := &dov1alpha1.DoCompositeDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: dov1alpha1.DoCompositeDefinitionSpec{
			Resources: []dov1alpha1.CompositeResourceTemplate{{Name: "droplet", Type: "digitalocean:index/droplet:Droplet"}},
		},
	}
	comp := testComposite(t, map[string]any{"env": "dev"})
	droplet := &dov1alpha1.DoResource{Status: dov1alpha1.DoResourceStatus{ID: "12345"}}

	t.Run("definition bugs are errors", func(t *testing.T) {
		for name, outputs := range map[string]map[string]any{
			"unknown resource":  {"x": "${resources.nope.id}"},
			"missing field":     {"x": "${resources.droplet}"},
			"bad field":         {"x": "${resources.droplet.spec.size}"},
			"unknown parameter": {"x": "${params.missing}"},
			"unsupported expr":  {"x": "${env.HOME}"},
			"unterminated":      {"x": "${resources.droplet.id"},
			"empty key":         {"": "v"},
		} {
			bad := def.DeepCopy()
			bad.Spec.Outputs = jsonRaw(t, outputs)
			if _, _, err := renderOutputs(comp, bad, map[string]*dov1alpha1.DoResource{"droplet": droplet}); err == nil {
				t.Errorf("%s: expected an error", name)
			}
		}
	})

	t.Run("no outputs declared", func(t *testing.T) {
		plain := def.DeepCopy()
		plain.Spec.Outputs = nil
		got, pending, err := renderOutputs(comp, plain, nil)
		if err != nil || got != nil || pending != nil {
			t.Errorf("got %v %v %v", got, pending, err)
		}
	})
}
