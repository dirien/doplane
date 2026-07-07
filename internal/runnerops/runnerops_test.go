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
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarshalPCL(t *testing.T) {
	props := map[string]any{
		"bucket": "my-bucket",
		"length": json.Number("3"),
		"force":  true,
		"tags": map[string]any{
			"managed-by": "operator",
			"quote":      `say "hi" ${not-interpolated}`,
		},
		"rules": []any{json.Number("1"), "two"},
		"nada":  nil,
	}
	out, err := MarshalPCL(props)
	if err != nil {
		t.Fatalf("MarshalPCL: %v", err)
	}
	for _, want := range []string{
		`bucket = "my-bucket"`,
		"length = 3",
		"force = true",
		`"managed-by" = "operator"`,
		`"quote" = "say \"hi\" $${not-interpolated}"`,
		`rules = [1, "two"]`,
		"nada = null",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if _, err := MarshalPCL(map[string]any{"not valid": 1}); err == nil {
		t.Fatal("expected error for invalid identifier")
	}
}

func TestLastJSONObject(t *testing.T) {
	out := "preamble\n{\n  \"length\": 3\n}\n{\n  \"id\": \"quick-fox\"\n}\n"
	obj, err := LastJSONObject(out)
	if err != nil {
		t.Fatalf("LastJSONObject: %v", err)
	}
	if obj["id"] != "quick-fox" {
		t.Errorf("wanted last object, got %v", obj)
	}
	if _, err := LastJSONObject("nothing"); err == nil {
		t.Fatal("expected error when no JSON present")
	}
}

func TestClassifyPackageRef(t *testing.T) {
	cases := []struct {
		pkg      string
		wantKind string
		wantPath string
	}{
		{"aws@7.34.0", "plain", ""},
		{"random", "plain", ""},
		{"", "plain", ""},
		{"ediri/ai-model", "registry", "private/ediri/ai-model/versions/latest"},
		{"ediri/ai-model@0.4.0", "registry", "private/ediri/ai-model/versions/0.4.0"},
		{"private/ediri/ai-model@0.4.0", "registry", "private/ediri/ai-model/versions/0.4.0"},
		{"github.com/pulumi/workshops/components/ai-model", "git", ""},
		{"./local/component", "git", ""},
	}
	for _, c := range cases {
		kind, path := ClassifyPackageRef(c.pkg)
		if kind != c.wantKind || path != c.wantPath {
			t.Errorf("ClassifyPackageRef(%q) = (%q, %q), want (%q, %q)", c.pkg, kind, path, c.wantKind, c.wantPath)
		}
	}
}

func TestEngineProgram(t *testing.T) {
	prog, err := engineProgram("ai-model:index:AIModelComponent", map[string]any{"modelName": "bert"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(prog), &doc); err != nil {
		t.Fatalf("program must be valid JSON (and therefore YAML): %v", err)
	}
	res := doc["resources"].(map[string]any)["res"].(map[string]any)
	if res["type"] != "ai-model:index:AIModelComponent" {
		t.Errorf("token: %v", res["type"])
	}
}

func TestTrimSchema(t *testing.T) {
	full := `{"name":"aws","version":"7.34.0","resources":{
		"aws:s3/bucketV2:BucketV2": {"inputProperties":{"bucket":{"type":"string"}}},
		"aws:ec2/instance:Instance": {"inputProperties":{}}}}`
	trimmed, err := trimSchema([]byte(full), "aws:s3/bucketV2:BucketV2")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name      string                     `json:"name"`
		Resources map[string]json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "aws" || len(doc.Resources) != 1 {
		t.Errorf("trimmed = %s", trimmed)
	}
	if _, ok := doc.Resources["aws:s3/bucketV2:BucketV2"]; !ok {
		t.Error("requested resource missing from trimmed schema")
	}
}

func TestRegistryClient(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`{"name":"ai-model","version":"0.4.0","resources":{}}`))
	_ = zw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/preview/registry/packages/"):
			if r.Header.Get("Authorization") != "token test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			resp := map[string]string{
				"name": "ai-model", "version": "0.4.0",
				"schemaURL":         "http://" + r.Host + "/schema.json",
				"pluginDownloadURL": "git://github.com/pulumi/workshops/components/ai-model",
			}
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/schema.json":
			// Raw gzip body without Content-Encoding, like S3.
			_, _ = w.Write(gz.Bytes())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	t.Setenv("PULUMI_ACCESS_TOKEN", "test-token")
	t.Setenv("PULUMI_API", srv.URL)

	client, err := newRegistryClient()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := client.resolve(context.Background(), "private/ediri/ai-model/versions/0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	src, err := pkg.gitSource()
	if err != nil || src != "github.com/pulumi/workshops/components/ai-model" {
		t.Errorf("gitSource = %q, %v", src, err)
	}
	schema, err := client.fetchSchema(context.Background(), pkg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), `"name":"ai-model"`) {
		t.Errorf("schema not decompressed: %q", Truncate(string(schema), 100))
	}

	t.Setenv("PULUMI_ACCESS_TOKEN", "")
	if _, err := newRegistryClient(); err == nil {
		t.Error("missing token must error")
	}
}

func TestSeedPlugins(t *testing.T) {
	baked := t.TempDir()
	plugDir := filepath.Join(baked, "resource-kubernetes-v4.23.0")
	if err := os.MkdirAll(plugDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugDir, "pulumi-resource-kubernetes"), []byte("#!bin"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baked, "resource-kubernetes-v4.23.0.lock"), nil, 0o400); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := seedPlugins(baked, dest); err != nil {
		t.Fatal(err)
	}
	// Plugin dirs must be REAL directories (pulumi's discovery skips
	// symlinked dirs) with symlinked contents.
	info, err := os.Lstat(filepath.Join(dest, "resource-kubernetes-v4.23.0"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("plugin dir must be a real directory: %v %v", info, err)
	}
	inner, err := os.Lstat(filepath.Join(dest, "resource-kubernetes-v4.23.0", "pulumi-resource-kubernetes"))
	if err != nil || inner.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("plugin binary must be a symlink: %v %v", inner, err)
	}
	// Lock files must be real writable files, not symlinks to the read-only
	// image layer.
	lock := filepath.Join(dest, "resource-kubernetes-v4.23.0.lock")
	if err := os.WriteFile(lock, []byte("x"), 0o600); err != nil {
		t.Fatalf("lock file must be writable: %v", err)
	}
}

func TestExecuteRejectsOversizedState(t *testing.T) {
	r := &Runner{}
	res := r.Execute(context.Background(), Op{
		Verb:        VerbEngineUp,
		Token:       "t:m:C",
		EngineState: bytes.Repeat([]byte("x"), MaxEngineStateBytes+1),
	})
	// The oversized state is written before pulumi runs; the guard lives in
	// the caller (engineStateJSON) — here we only ensure Execute survives
	// bad specs without panicking and reports failure.
	if res.OK {
		t.Error("expected failure result")
	}
}

func TestClassifyDoFailure(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"not found", "error: resource \"x\" was not found", CodeNotFound},
		{"import", "error: Resource Import Not Implemented", CodeReadNotSupported},
		{"generic", "error: AccessDenied", CodeOperationFailed},
		{"id in argv ignored", "This will delete \"my-not-found-bucket\"", CodeOperationFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := classifyDoFailure(errContaining(c.text), "")
			if res.Code != c.want {
				t.Errorf("code = %s, want %s", res.Code, c.want)
			}
		})
	}
}

type textError string

func (e textError) Error() string { return string(e) }

func errContaining(s string) error { return textError(s) }
