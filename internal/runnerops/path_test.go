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

package runnerops

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSetPath(t *testing.T) {
	props := map[string]any{
		"tags":  map[string]any{"a": "1"},
		"rules": []any{map[string]any{"id": "old"}},
	}
	cases := []struct {
		path  string
		value any
	}{
		{"bucket", "b1"},
		{"tags.owner", "me"},
		{"nested.deep.key", "v"},
		{"rules[0].id", "new"},
	}
	for _, c := range cases {
		if err := SetPath(props, c.path, c.value); err != nil {
			t.Fatalf("SetPath(%q): %v", c.path, err)
		}
		got, ok := GetPath(props, c.path)
		if !ok || !reflect.DeepEqual(got, c.value) {
			t.Errorf("GetPath(%q) = %v (ok=%t), want %v", c.path, got, ok, c.value)
		}
	}
	for _, bad := range []string{"", "rules[5].id", "bucket.sub", "tags[0]", "a..b", `a["unterminated`, "a[x]"} {
		if err := SetPath(props, bad, "x"); err == nil {
			t.Errorf("SetPath(%q): expected error", bad)
		}
	}
}

func TestPathQuotedSegments(t *testing.T) {
	props := map[string]any{}
	path := AppendKeySegment("tags", "kubernetes.io/cluster/prod")
	if path != `tags["kubernetes.io/cluster/prod"]` {
		t.Fatalf("AppendKeySegment: %q", path)
	}
	if err := SetPath(props, path, "owned"); err != nil {
		t.Fatal(err)
	}
	got, ok := GetPath(props, path)
	if !ok || got != "owned" {
		t.Errorf("roundtrip via quoted segment failed: %v %t", got, ok)
	}
	tags := props["tags"].(map[string]any)
	if tags["kubernetes.io/cluster/prod"] != "owned" {
		t.Errorf("dotted key not stored literally: %v", tags)
	}
	// Quotes and backslashes inside keys survive the escaping.
	tricky := AppendKeySegment("", `he said "hi"\`)
	if err := SetPath(props, tricky, 1); err != nil {
		t.Fatalf("SetPath(%q): %v", tricky, err)
	}
	if _, ok := props[`he said "hi"\`]; !ok {
		t.Errorf("tricky key missing: %v", props)
	}
	if v := AppendKeySegment("a", "plain"); v != "a.plain" {
		t.Errorf("plain keys must stay dotted: %q", v)
	}
}

func TestGetPathMissing(t *testing.T) {
	v := map[string]any{"a": map[string]any{"b": "c"}}
	if _, ok := GetPath(v, "a.x"); ok {
		t.Error("expected missing path to report !ok")
	}
	if got, ok := GetPath(v, "a.b"); !ok || got != "c" {
		t.Errorf("GetPath(a.b) = %v, %t", got, ok)
	}
}

func TestRenderScalar(t *testing.T) {
	cases := map[string]any{
		"str":       "str",
		"42":        json.Number("42"),
		"true":      true,
		`{"k":"v"}`: map[string]any{"k": "v"},
		`["a"]`:     []any{"a"},
		"":          nil,
	}
	for want, in := range cases {
		if got := RenderScalar(in); got != want {
			t.Errorf("RenderScalar(%v) = %q, want %q", in, got, want)
		}
	}
}
