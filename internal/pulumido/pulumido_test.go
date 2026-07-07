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

import (
	"encoding/json"
	"errors"
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
}

func TestMarshalPCLRejectsBadIdentifier(t *testing.T) {
	if _, err := MarshalPCL(map[string]any{"not valid": 1}); err == nil {
		t.Fatal("expected error for invalid identifier")
	}
}

func TestLastJSONObject(t *testing.T) {
	out := `This will create random:index/randomPet:RandomPet with the following inputs:
{
  "length": 3
}
{
  "id": "quick-fox",
  "length": 3,
  "separator": "-"
}
`
	obj, err := lastJSONObject(out)
	if err != nil {
		t.Fatalf("lastJSONObject: %v", err)
	}
	if obj["id"] != "quick-fox" {
		t.Errorf("wanted last object with id, got %v", obj)
	}
}

func TestLastJSONObjectNoJSON(t *testing.T) {
	if _, err := lastJSONObject("nothing to see here"); err == nil {
		t.Fatal("expected error when no JSON present")
	}
}

func TestClassifyProviderErrors(t *testing.T) {
	cases := []struct {
		name   string
		output string
		extra  string
		want   error
	}{
		{
			name:   "read not supported",
			output: "error: Resource Import Not Implemented: contact developer",
			want:   ErrReadNotSupported,
		},
		{
			name:   "aws not found",
			output: "some preamble\nerror: api error NoSuchBucket: The specified bucket does not exist",
			want:   ErrNotFound,
		},
		{
			name:  "job fail message only",
			extra: "error: resource \"x\" was not found",
			want:  ErrNotFound,
		},
		{
			name:   "status code 404",
			output: "error: https response error StatusCode: 404, request id: abc",
			want:   ErrNotFound,
		},
		{
			name:   "access denied is not a sentinel",
			output: "error: AccessDenied: not authorized",
			want:   nil,
		},
		{
			// A resource whose ID contains "not-found" must not be
			// classified from the echoed command line.
			name:   "id in command line is ignored",
			output: "This will delete aws:s3/bucketV2:BucketV2 \"my-not-found-bucket\".\npulumi do delete my-404-bucket",
			want:   nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyText(providerErrorText(c.output, c.extra))
			matches := (got == nil && c.want == nil) || errors.Is(got, c.want)
			if !matches {
				t.Errorf("want %v, got %v", c.want, got)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	schema := &PackageSchema{
		Name:    "aws",
		Version: "7.34.0",
		Resources: map[string]ResourceSchema{
			"aws:s3/bucketV2:BucketV2": {
				RequiredInputs: []string{},
				InputProperties: map[string]PropertySchema{
					"bucket": {Type: "string"},
					"tags":   {Type: "object", AdditionalProperties: &PropertySchema{Type: "string"}},
					"count":  {Type: "integer"},
					"rules":  {Type: "array", Items: &PropertySchema{Type: "string"}},
					"refd":   {Ref: "#/types/aws:s3/thing:Thing"},
				},
			},
			"aws:ec2/instance:Instance": {
				RequiredInputs:  []string{"ami"},
				InputProperties: map[string]PropertySchema{"ami": {Type: "string"}},
			},
		},
	}

	t.Run("valid", func(t *testing.T) {
		v, err := schema.Validate("aws:s3/bucketV2:BucketV2", map[string]any{
			"bucket": "b",
			"tags":   map[string]any{"a": "b"},
			"count":  json.Number("2"),
			"rules":  []any{"r1"},
			"refd":   map[string]any{"whatever": true},
		})
		if err != nil || len(v) != 0 {
			t.Fatalf("want valid, got violations=%v err=%v", v, err)
		}
	})

	t.Run("violations", func(t *testing.T) {
		v, err := schema.Validate("aws:s3/bucketV2:BucketV2", map[string]any{
			"bucket":  json.Number("5"),
			"unknown": "x",
			"tags":    map[string]any{"a": json.Number("1")},
			"rules":   []any{json.Number("1")},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(v) != 4 {
			t.Fatalf("want 4 violations, got %d: %v", len(v), v)
		}
	})

	t.Run("required", func(t *testing.T) {
		v, err := schema.Validate("aws:ec2/instance:Instance", map[string]any{})
		if err != nil || len(v) != 1 || !strings.Contains(v[0], "ami") {
			t.Fatalf("want missing-ami violation, got %v err=%v", v, err)
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		if _, err := schema.Validate("aws:nope/nope:Nope", nil); err == nil {
			t.Fatal("expected error for unknown resource type")
		}
	})
}

func TestDoArgs(t *testing.T) {
	got := strings.Join(doArgs("aws:s3/bucketV2:BucketV2", "aws@7.34.0", "patch", "my-id", "/tmp/in.pp"), " ")
	want := "do aws:s3/bucketV2:BucketV2 --package aws@7.34.0 patch my-id --yes --input-file /tmp/in.pp --stateless --non-interactive --color never"
	if got != want {
		t.Errorf("doArgs mismatch:\n got %s\nwant %s", got, want)
	}
	read := strings.Join(doArgs("t:m:R", "", "read", "id1", ""), " ")
	if strings.Contains(read, "--yes") {
		t.Errorf("read must not include --yes: %s", read)
	}
}

func TestShellQuote(t *testing.T) {
	in := `a'b"$c`
	q := shellQuote(in)
	if q != `'a'\''b"$c'` {
		t.Errorf("shellQuote(%q) = %s", in, q)
	}
}
