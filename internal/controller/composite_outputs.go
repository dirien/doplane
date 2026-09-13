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
	"fmt"
	"sort"
	"strings"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// renderOutputs resolves a definition's spec.outputs for comp against the
// parameters and the children's observed state. observed maps template
// resource names to the child objects as last read; a missing entry means
// the child does not exist yet (or is being replaced).
//
// Unlike template properties, outputs are resolved by the composite
// controller itself rather than compiled into DoResource references, so one
// string may combine any number of sibling sources. The result is the
// resolved outputs (nil when none resolved), one entry per still-pending
// output naming what it waits for, and an error only for a definition bug
// (unknown resource, unknown parameter, malformed expression) — the
// composite surfaces that as RenderFailed.
func renderOutputs(comp *dov1alpha1.DoComposite, def *dov1alpha1.DoCompositeDefinition,
	observed map[string]*dov1alpha1.DoResource,
) (map[string]any, []string, error) {
	if def.Spec.Outputs == nil || len(def.Spec.Outputs.Raw) == 0 {
		return nil, nil, nil
	}
	outputs, err := decodeProperties(def.Spec.Outputs)
	if err != nil {
		return nil, nil, fmt.Errorf("spec.outputs is not a JSON object: %w", err)
	}
	params, err := decodeProperties(comp.Spec.Parameters)
	if err != nil {
		return nil, nil, fmt.Errorf("spec.parameters is not a JSON object: %w", err)
	}
	rc := &renderContext{composite: comp, params: params, childName: map[string]string{}}
	for _, tpl := range def.Spec.Resources {
		rc.childName[tpl.Name] = childResourceName(comp.Name, tpl.Name)
	}

	keys := make([]string, 0, len(outputs))
	for k := range outputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	resolved := map[string]any{}
	var pending []string
	for _, key := range keys {
		if key == "" {
			return nil, nil, fmt.Errorf("spec.outputs contains an empty key")
		}
		value, wait, err := renderOutputValue(rc, observed, outputs[key], "outputs."+key)
		if err != nil {
			return nil, nil, err
		}
		if wait != "" {
			// One output is published whole or not at all: a nested object
			// with half its sources resolved would be misleading.
			pending = append(pending, fmt.Sprintf("%s (%s)", key, wait))
			continue
		}
		resolved[key] = value
	}
	if len(resolved) == 0 {
		resolved = nil
	}
	return resolved, pending, nil
}

// renderOutputValue walks one output value. The returned wait names the
// first source that is not yet available ("" when the value resolved).
func renderOutputValue(rc *renderContext, observed map[string]*dov1alpha1.DoResource, v any, path string) (any, string, error) {
	switch t := v.(type) {
	case string:
		return renderOutputString(rc, observed, t, path)
	case map[string]any:
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			rv, wait, err := renderOutputValue(rc, observed, t[k], pulumido.AppendKeySegment(path, k))
			if err != nil || wait != "" {
				return nil, wait, err
			}
			out[k] = rv
		}
		return out, "", nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			rv, wait, err := renderOutputValue(rc, observed, e, fmt.Sprintf("%s[%d]", path, i))
			if err != nil || wait != "" {
				return nil, wait, err
			}
			out[i] = rv
		}
		return out, "", nil
	default:
		return v, "", nil
	}
}

// renderOutputString resolves every expression inside one output string.
// A string that is exactly one expression yields the source value with its
// native type; otherwise sources are interpolated as text.
func renderOutputString(rc *renderContext, observed map[string]*dov1alpha1.DoResource, s, path string) (any, string, error) {
	escaped := strings.ReplaceAll(s, "$${", escMarker)
	if err := checkUnterminated(escaped, path); err != nil {
		return nil, "", err
	}
	matches := exprRe.FindAllStringSubmatchIndex(escaped, -1)
	if len(matches) == 0 {
		return strings.ReplaceAll(escaped, escMarker, "${"), "", nil
	}
	wholeString := len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(escaped)

	var b strings.Builder
	prev := 0
	for _, m := range matches {
		b.WriteString(escaped[prev:m[0]])
		prev = m[1]
		expr := strings.TrimSpace(escaped[m[2]:m[3]])
		val, wait, err := resolveOutputExpr(rc, observed, expr, path)
		if err != nil {
			return nil, "", err
		}
		if wait != "" {
			return nil, wait, nil
		}
		if wholeString {
			return val, "", nil
		}
		b.WriteString(pulumido.RenderScalar(val))
	}
	b.WriteString(escaped[prev:])
	return strings.ReplaceAll(b.String(), escMarker, "${"), "", nil
}

// resolveOutputExpr resolves one expression against the parameters, the
// composite's identity or a child's observed status.
func resolveOutputExpr(rc *renderContext, observed map[string]*dov1alpha1.DoResource, expr, path string) (any, string, error) {
	switch {
	case strings.HasPrefix(expr, "params.") || expr == "params":
		val, ok := pulumido.GetPath(map[string]any{"params": rc.params}, expr)
		if !ok {
			return nil, "", fmt.Errorf("value at %q: expression ${%s}: parameter not found", path, expr)
		}
		return val, "", nil
	case expr == "self.name":
		return rc.composite.Name, "", nil
	case expr == "self.namespace":
		return rc.composite.Namespace, "", nil
	case strings.HasPrefix(expr, "resources."):
		source, fieldPath, err := parseResourceExpr(rc, expr)
		if err != nil {
			return nil, "", fmt.Errorf("value at %q: %w", path, err)
		}
		child := observed[source]
		if child == nil {
			return nil, fmt.Sprintf("%s not yet applied", source), nil
		}
		val, ok, err := resolveFieldPath(child, fieldPath)
		if err != nil {
			return nil, "", fmt.Errorf("value at %q: expression ${%s}: %w", path, expr, err)
		}
		if !ok {
			return nil, fmt.Sprintf("%s: %s not yet available", source, fieldPath), nil
		}
		return val, "", nil
	default:
		return nil, "", fmt.Errorf("value at %q: unsupported expression ${%s} (want params.*, self.name, self.namespace or resources.*)", path, expr)
	}
}
