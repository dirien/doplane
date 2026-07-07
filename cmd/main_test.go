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

package main

import "testing"

func TestParseWatchNamespaces(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string // nil means cluster-wide (nil map)
	}{
		{"empty means cluster-wide", "", nil},
		{"only separators means cluster-wide", " , ,", nil},
		{"single namespace", "team-a", []string{"team-a"}},
		{"multiple with whitespace", " team-a, team-b ,team-c", []string{"team-a", "team-b", "team-c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseWatchNamespaces(tt.in)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("want nil (cluster-wide), got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want namespaces %v", got, tt.want)
			}
			for _, ns := range tt.want {
				if _, ok := got[ns]; !ok {
					t.Errorf("namespace %q missing from %v", ns, got)
				}
			}
		})
	}
}
