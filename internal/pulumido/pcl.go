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
	"fmt"
	"regexp"
	"sort"
	"strings"
)

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
		// Marshal via encoding/json so integral floats render without exponent.
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
