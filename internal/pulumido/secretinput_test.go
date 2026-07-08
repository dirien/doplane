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

package pulumido

import "testing"

func TestPackagePinned(t *testing.T) {
	tests := []struct {
		pkg  string
		want bool
	}{
		{"aws@7.34.0", true},
		{"aws", false},
		{"", false},                           // inferred from the type token
		{"private/org/pkg", false},            // registry: resolves versions/latest
		{"private/org/pkg@1.2.3", true},       // registry with explicit version
		{"https://github.com/org/repo", true}, // git pins via URL/ref
	}
	for _, tt := range tests {
		if got := PackagePinned(tt.pkg); got != tt.want {
			t.Errorf("PackagePinned(%q) = %t, want %t", tt.pkg, got, tt.want)
		}
	}
}

func TestSecretInputsPlan(t *testing.T) {
	// Deterministic ordering matters: Job names hash the op document, so
	// the same inputs must always produce the same mapping for adoption.
	inputs := []SecretInput{
		{ToPath: "zeta", SecretName: "b", SecretKey: "k2"},
		{ToPath: "alpha", SecretName: "a", SecretKey: "k1"},
	}
	mapping, ordered := secretInputsPlan(inputs)
	if mapping["alpha"] != "DOPLANE_SECRET_0" || mapping["zeta"] != "DOPLANE_SECRET_1" {
		t.Errorf("mapping must follow ToPath order: %v", mapping)
	}
	if ordered[0].SecretName != "a" || ordered[1].SecretName != "b" {
		t.Errorf("ordered inputs must match env numbering: %+v", ordered)
	}
	// The caller's slice stays untouched.
	if inputs[0].ToPath != "zeta" {
		t.Error("plan must not mutate its input")
	}
}
