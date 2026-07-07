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
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MaxEngineStateBytes bounds checkpoints handed around via Job env and the
// CR status; larger states would strain etcd object limits.
const MaxEngineStateBytes = 900 * 1024

// PackageForToken derives the package name from a type token like
// "aws:s3/bucketV2:BucketV2".
func PackageForToken(token string) string {
	if i := strings.Index(token, ":"); i > 0 {
		return token[:i]
	}
	return token
}

// LastJSONObject extracts the final top-level JSON object from mixed CLI
// output. `pulumi do` prints a human preamble and an echo of the inputs
// before the resulting resource state; the state is always the last object.
func LastJSONObject(out string) (map[string]any, error) {
	var last map[string]any
	found := false
	for i := 0; i < len(out); i++ {
		if out[i] != '{' || (i > 0 && out[i-1] != '\n') {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(out[i:]))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err == nil {
			last = m
			found = true
			i += int(dec.InputOffset()) - 1
		}
	}
	if !found {
		return nil, errors.New("no JSON object found in output")
	}
	return last, nil
}

// Truncate shortens s to at most n bytes for error messages.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}

// ProviderErrorText extracts the provider's own error lines from CLI/pod
// output (lines starting with "error"), appending any extra failure message.
// Error classification runs against this text only — never against command
// lines or echoed inputs, whose contents (e.g. a resource named
// "not-found-test") would otherwise cause false classification.
func ProviderErrorText(output, extra string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.ToLower(line))
		if strings.HasPrefix(trimmed, "error:") || strings.HasPrefix(trimmed, "error ") ||
			strings.HasPrefix(trimmed, "[error]") {
			lines = append(lines, trimmed)
		}
	}
	if extra != "" {
		lines = append(lines, strings.ToLower(extra))
	}
	return strings.Join(lines, "\n")
}

// engineProgram renders the single-resource Pulumi YAML program. JSON is
// valid YAML, so the program is emitted as JSON for robust quoting; `pulumi
// package add` appends the packages section itself.
func engineProgram(token string, props map[string]any) (string, error) {
	if props == nil {
		props = map[string]any{}
	}
	program := map[string]any{
		"name":    "doplane",
		"runtime": "yaml",
		"resources": map[string]any{
			"res": map[string]any{
				"type":       token,
				"properties": props,
			},
		},
	}
	raw, err := json.Marshal(program)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// MarshalPCL renders a set of resource input properties as a PCL attribute
// list, the format `pulumi do --input-file` expects. Top-level keys must be
// valid identifiers (Pulumi schema property names always are).
func MarshalPCL(props map[string]any) (string, error) {
	var b strings.Builder
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !identRe.MatchString(k) {
			return "", fmt.Errorf("property name %q is not a valid PCL identifier", k)
		}
		v, err := pclValue(props[k], 0)
		if err != nil {
			return "", fmt.Errorf("property %q: %w", k, err)
		}
		fmt.Fprintf(&b, "%s = %s\n", k, v)
	}
	return b.String(), nil
}

func pclValue(v any, depth int) (string, error) {
	if depth > 32 {
		return "", fmt.Errorf("value nesting too deep")
	}
	switch t := v.(type) {
	case nil:
		return "null", nil
	case bool:
		return fmt.Sprintf("%t", t), nil
	case json.Number:
		return t.String(), nil
	case float64:
		raw, err := json.Marshal(t)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	case string:
		return pclString(t), nil
	case []any:
		items := make([]string, 0, len(t))
		for i, e := range t {
			s, err := pclValue(e, depth+1)
			if err != nil {
				return "", fmt.Errorf("index %d: %w", i, err)
			}
			items = append(items, s)
		}
		return "[" + strings.Join(items, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("{\n")
		for _, k := range keys {
			s, err := pclValue(t[k], depth+1)
			if err != nil {
				return "", fmt.Errorf("key %q: %w", k, err)
			}
			fmt.Fprintf(&b, "%s%s = %s\n", strings.Repeat("  ", depth+1), pclString(k), s)
		}
		b.WriteString(strings.Repeat("  ", depth) + "}")
		return b.String(), nil
	default:
		return "", fmt.Errorf("unsupported value type %T", v)
	}
}

// pclString quotes a string for PCL, escaping interpolation and template
// directive sequences so values are always treated literally.
func pclString(s string) string {
	raw, _ := json.Marshal(s) // JSON string escaping is valid in PCL
	q := string(raw)
	q = strings.ReplaceAll(q, "${", "$${")
	q = strings.ReplaceAll(q, "%{", "%%{")
	return q
}
