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
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteFallbackApplies(t *testing.T) {
	cases := []struct {
		name string
		res  Result
		want bool
	}{
		{"read not supported", failure(CodeReadNotSupported, "error: Resource Import Not Implemented"), true},
		{"importer rejects id", failure(CodeOperationFailed,
			"exit status 1:\nerror: [ERROR] Invalid import usage: expecting {result},{min},{max}: Import Random Integer Error"), true},
		{"delete-phase provider error", failure(CodeOperationFailed,
			"exit status 1:\nerror: DeleteConflict: Cannot delete entity, must detach all policies first"), false},
		{"import only outside provider error lines", failure(CodeOperationFailed,
			"importing resource first\nerror: AccessDenied"), false},
		{"not found", failure(CodeNotFound, "error: resource \"x\" was not found"), false},
		{"already exists", failure(CodeAlreadyExists, "error: already exists"), false},
		{"success", Result{OK: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deleteFallbackApplies(c.res); got != c.want {
				t.Errorf("deleteFallbackApplies = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDeletePluginRef(t *testing.T) {
	cases := []struct {
		name        string
		op          Op
		wantName    string
		wantVersion string
		ok          bool
	}{
		{"pinned plain", Op{Token: "random:index/randomPet:RandomPet", Package: "random@4.21.0"}, "random", "4.21.0", true},
		{"unpinned plain", Op{Token: "random:index/randomPet:RandomPet", Package: "random"}, "random", "", true},
		{"inferred from token", Op{Token: "aws:s3/bucket:Bucket"}, "aws", "", true},
		{"git source", Op{Token: "t:m:R", Package: "https://github.com/org/repo.git"}, "", "", false},
		{"registry ref", Op{Token: "t:m:R", Package: "private/org/name@1.0.0"}, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name, version, ok := deletePluginRef(c.op)
			if ok != c.ok || name != c.wantName || version != c.wantVersion {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)", name, version, ok, c.wantName, c.wantVersion, c.ok)
			}
		})
	}
}

func TestDeleteCheckpoint(t *testing.T) {
	state := map[string]any{"id": "pet-1", "length": json.Number("2"), "prefix": "p"}
	raw, err := deleteCheckpoint("random", "4.21.0", "random:index/randomPet:RandomPet", "pet-1", state)
	if err != nil {
		t.Fatal(err)
	}
	var cp struct {
		Version    int `json:"version"`
		Deployment struct {
			Resources []checkpointResource `json:"resources"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	if cp.Version != 3 || len(cp.Deployment.Resources) != 2 {
		t.Fatalf("unexpected checkpoint shape: %s", raw)
	}
	provider, res := cp.Deployment.Resources[0], cp.Deployment.Resources[1]
	if provider.Type != "pulumi:providers:random" || provider.ID == "" || !provider.Custom {
		t.Errorf("bad provider resource: %+v", provider)
	}
	if provider.Inputs["version"] != "4.21.0" || provider.Outputs["version"] != "4.21.0" {
		t.Errorf("provider must pin the plugin version: %+v", provider)
	}
	if !strings.HasPrefix(provider.URN, "urn:pulumi:dev::doplane::pulumi:providers:random::") {
		t.Errorf("bad provider urn %q", provider.URN)
	}
	if res.Provider != provider.URN+"::"+provider.ID {
		t.Errorf("resource must reference the provider by urn::id, got %q", res.Provider)
	}
	if res.Type != "random:index/randomPet:RandomPet" || res.ID != "pet-1" || !res.Custom {
		t.Errorf("bad resource: %+v", res)
	}
	if res.URN != "urn:pulumi:dev::doplane::random:index/randomPet:RandomPet::target" {
		t.Errorf("bad resource urn %q", res.URN)
	}
	if _, ok := res.Inputs["id"]; ok {
		t.Error("id must not be an input")
	}
	if res.Inputs["prefix"] != "p" || res.Outputs["prefix"] != "p" || res.Outputs["id"] != "pet-1" {
		t.Errorf("state must be carried as inputs and outputs: %+v", res)
	}
	if _, ok := state["id"]; !ok {
		t.Error("caller's state must not be mutated")
	}

	// Unpinned packages carry no version so the engine uses what is installed.
	raw, err = deleteCheckpoint("random", "", "random:index/randomPet:RandomPet", "pet-1", state)
	if err != nil {
		t.Fatal(err)
	}
	var unpinned struct {
		Deployment struct {
			Resources []checkpointResource `json:"resources"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(raw, &unpinned); err != nil {
		t.Fatal(err)
	}
	if _, ok := unpinned.Deployment.Resources[0].Inputs["version"]; ok {
		t.Error("unpinned provider must not pin a version")
	}
}

// fakePulumi installs a stand-in pulumi binary that records every argv line
// to a log, fails `do … delete` with the given stderr text, captures the file
// passed to `stack import` and lets `destroy` succeed or fail as configured.
func fakePulumi(t *testing.T, doStderr, destroyStderr string) (bin, log, capture string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "pulumi")
	log = filepath.Join(dir, "argv.log")
	capture = filepath.Join(dir, "imported.json")
	script := `#!/bin/sh
echo "$@" >> "$FAKE_PULUMI_LOG"
case "$1" in
  do) printf '%s\n' "$FAKE_PULUMI_DO_STDERR" >&2; exit 1 ;;
  stack) [ "$2" = import ] && cp "$4" "$FAKE_PULUMI_CAPTURE"; exit 0 ;;
  destroy) if [ -n "$FAKE_PULUMI_DESTROY_STDERR" ]; then printf '%s\n' "$FAKE_PULUMI_DESTROY_STDERR" >&2; exit 1; fi; exit 0 ;;
esac
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // the fake binary must be executable
		t.Fatal(err)
	}
	t.Setenv("FAKE_PULUMI_LOG", log)
	t.Setenv("FAKE_PULUMI_CAPTURE", capture)
	t.Setenv("FAKE_PULUMI_DO_STDERR", doStderr)
	t.Setenv("FAKE_PULUMI_DESTROY_STDERR", destroyStderr)
	return bin, log, capture
}

func TestExecuteDeleteFallsBackToEngineDestroy(t *testing.T) {
	bin, log, capture := fakePulumi(t,
		"error: [ERROR] This resource does not support import. Please contact the provider developer for additional information.: Resource Import Not Implemented", "")
	r := &Runner{PulumiBin: bin, Progress: io.Discard}
	state := map[string]any{"id": "pet-1", "length": float64(2), "prefix": "p"}
	res := r.Execute(context.Background(), Op{
		Verb: VerbDelete, Token: "random:index/randomPet:RandomPet", Package: "random@4.21.0", ID: "pet-1", State: state,
	})
	if !res.OK || res.ID != "pet-1" {
		t.Fatalf("expected success, got %+v", res)
	}
	argv, err := os.ReadFile(log) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	want := []string{
		"do random:index/randomPet:RandomPet --package random@4.21.0 delete pet-1 --yes --stateless --non-interactive --color never --output json",
		"stack init dev --non-interactive",
		"stack import --file ",
		"destroy --yes --non-interactive",
	}
	if len(lines) != len(want) {
		t.Fatalf("argv lines = %q, want %d steps", lines, len(want))
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("step %d = %q, want prefix %q", i, lines[i], w)
		}
	}
	imported, err := os.ReadFile(capture) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("stack import did not receive a checkpoint file: %v", err)
	}
	var cp struct {
		Deployment struct {
			Resources []checkpointResource `json:"resources"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(imported, &cp); err != nil {
		t.Fatal(err)
	}
	if len(cp.Deployment.Resources) != 2 || cp.Deployment.Resources[1].ID != "pet-1" ||
		cp.Deployment.Resources[1].Outputs["prefix"] != "p" {
		t.Errorf("imported checkpoint does not carry the recorded state: %s", imported)
	}
}

func TestExecuteDeleteWithoutStateKeepsReadNotSupported(t *testing.T) {
	bin, log, _ := fakePulumi(t, "error: Resource Import Not Implemented", "")
	r := &Runner{PulumiBin: bin, Progress: io.Discard}
	res := r.Execute(context.Background(), Op{
		Verb: VerbDelete, Token: "random:index/randomPet:RandomPet", Package: "random@4.21.0", ID: "pet-1",
	})
	if res.OK || res.Code != CodeReadNotSupported {
		t.Fatalf("expected ReadNotSupported without recorded state, got %+v", res)
	}
	argv, _ := os.ReadFile(log) // #nosec G304 -- test temp dir
	if strings.Contains(string(argv), "destroy") {
		t.Error("fallback must not run without recorded state")
	}
}

func TestExecuteDeleteDoesNotFallBackOnDeletePhaseFailure(t *testing.T) {
	bin, log, _ := fakePulumi(t, "error: DeleteConflict: Cannot delete entity, must detach all policies first", "")
	r := &Runner{PulumiBin: bin, Progress: io.Discard}
	res := r.Execute(context.Background(), Op{
		Verb: VerbDelete, Token: "aws:iam/role:Role", Package: "aws@7.34.0", ID: "r", State: map[string]any{"id": "r"},
	})
	if res.OK || res.Code != CodeOperationFailed {
		t.Fatalf("expected the provider's delete failure to surface, got %+v", res)
	}
	argv, _ := os.ReadFile(log) // #nosec G304 -- test temp dir
	if strings.Contains(string(argv), "destroy") {
		t.Error("a delete-phase failure must not be retried through the engine")
	}
}

func TestExecuteDeleteFallbackReportsBothFailures(t *testing.T) {
	bin, _, _ := fakePulumi(t, "error: Resource Import Not Implemented",
		"error: [ERROR] deleting: InternalError: something broke")
	r := &Runner{PulumiBin: bin, Progress: io.Discard}
	res := r.Execute(context.Background(), Op{
		Verb: VerbDelete, Token: "random:index/randomPet:RandomPet", Package: "random@4.21.0", ID: "pet-1",
		State: map[string]any{"id": "pet-1"},
	})
	if res.OK || res.Code != CodeOperationFailed {
		t.Fatalf("expected OperationFailed, got %+v", res)
	}
	for _, s := range []string{"ReadNotSupported", "Import Not Implemented", "pulumi destroy", "something broke"} {
		if !strings.Contains(res.Message, s) {
			t.Errorf("message %q lacks %q", res.Message, s)
		}
	}
}

func TestExecuteDeleteFallbackNotFoundFinalizes(t *testing.T) {
	bin, _, _ := fakePulumi(t, "error: Resource Import Not Implemented",
		"error: [ERROR] deleting: NoSuchEntity: the role was not found")
	r := &Runner{PulumiBin: bin, Progress: io.Discard}
	res := r.Execute(context.Background(), Op{
		Verb: VerbDelete, Token: "aws:iam/role:Role", Package: "aws@7.34.0", ID: "r", State: map[string]any{"id": "r"},
	})
	if res.OK || res.Code != CodeNotFound {
		t.Fatalf("a resource gone by destroy time must report NotFound, got %+v", res)
	}
}

func TestExecuteDeleteFallbackSkipsUnsupportedPackages(t *testing.T) {
	bin, log, _ := fakePulumi(t, "error: Resource Import Not Implemented", "")
	r := &Runner{PulumiBin: bin, Progress: io.Discard}
	res := r.Execute(context.Background(), Op{
		Verb: VerbDelete, Token: "t:m:R", Package: "https://github.com/org/repo.git", ID: "x", State: map[string]any{"id": "x"},
	})
	if res.OK || res.Code != CodeReadNotSupported {
		t.Fatalf("expected the stateless failure for a git package, got %+v", res)
	}
	argv, _ := os.ReadFile(log) // #nosec G304 -- test temp dir
	if strings.Contains(string(argv), "stack") {
		t.Error("no checkpoint path for git packages")
	}
}
