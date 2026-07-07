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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	dov1alpha1 "github.com/dirien/pulumi-do-operator/api/v1alpha1"
	"github.com/dirien/pulumi-do-operator/internal/pulumido"
)

var exprRe = regexp.MustCompile(`\$\{([^}]*)\}`)

// escMarker temporarily replaces the "$${" escape while expressions are
// parsed. NUL cannot appear in valid Kubernetes API strings.
const escMarker = "\x00PDO_ESC\x00"

// renderContext carries everything expressions can see.
type renderContext struct {
	composite *dov1alpha1.DoComposite
	params    map[string]any
	// childName maps a template resource name to the child object name.
	childName map[string]string
}

// renderComposite expands a definition into the desired child DoResources.
// Sibling output expressions become DoResource references, so the resource
// graph engine provides ordering, propagation and ordered teardown.
func renderComposite(comp *dov1alpha1.DoComposite, def *dov1alpha1.DoCompositeDefinition) ([]*dov1alpha1.DoResource, error) {
	params, err := decodeProperties(comp.Spec.Parameters)
	if err != nil {
		return nil, fmt.Errorf("spec.parameters is not a JSON object: %w", err)
	}
	for _, required := range def.Spec.RequiredParameters {
		if _, ok := params[required]; !ok {
			return nil, fmt.Errorf("required parameter %q is missing", required)
		}
	}

	rc := &renderContext{composite: comp, params: params, childName: map[string]string{}}
	for _, tpl := range def.Spec.Resources {
		if _, dup := rc.childName[tpl.Name]; dup {
			return nil, fmt.Errorf("duplicate resource name %q in definition", tpl.Name)
		}
		// Template names become label values and child-name suffixes; both
		// require DNS-label shape.
		if errs := validation.IsDNS1123Label(tpl.Name); len(errs) > 0 {
			return nil, fmt.Errorf("resource name %q is not a valid DNS label: %s", tpl.Name, errs[0])
		}
		rc.childName[tpl.Name] = childResourceName(comp.Name, tpl.Name)
	}

	children := make([]*dov1alpha1.DoResource, 0, len(def.Spec.Resources))
	for i := range def.Spec.Resources {
		tpl := &def.Spec.Resources[i]
		child, err := renderChild(rc, tpl)
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", tpl.Name, err)
		}
		children = append(children, child)
	}
	return children, nil
}

func renderChild(rc *renderContext, tpl *dov1alpha1.CompositeResourceTemplate) (*dov1alpha1.DoResource, error) {
	props, err := decodeProperties(tpl.Properties)
	if err != nil {
		return nil, fmt.Errorf("properties are not a JSON object: %w", err)
	}
	var refs []dov1alpha1.Reference
	rendered, err := renderValue(rc, props, "", &refs)
	if err != nil {
		return nil, err
	}
	renderedProps, ok := rendered.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("rendered properties are not an object")
	}
	raw, err := json.Marshal(renderedProps)
	if err != nil {
		return nil, err
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ToPath < refs[j].ToPath })

	child := &dov1alpha1.DoResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rc.childName[tpl.Name],
			Namespace: rc.composite.Namespace,
			Labels: map[string]string{
				labelComposite:     compositeLabelValue(rc.composite.Name),
				labelCompositeItem: tpl.Name,
				labelManagedByKey:  "pulumi-do-operator",
			},
		},
		Spec: dov1alpha1.DoResourceSpec{
			Type:           tpl.Type,
			Package:        tpl.Package,
			DeletionPolicy: tpl.DeletionPolicy,
			Properties:     &apiextensionsv1.JSON{Raw: raw},
			References:     refs,
		},
	}
	return child, nil
}

// renderValue walks a JSON value, resolving expressions in strings. Strings
// containing sibling-output expressions are removed from the literal value
// and recorded as references at the value's path instead.
func renderValue(rc *renderContext, v any, path string, refs *[]dov1alpha1.Reference) (any, error) {
	switch t := v.(type) {
	case string:
		return renderString(rc, t, path, refs)
	case map[string]any:
		out := make(map[string]any, len(t))
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			// Keys containing path metacharacters ('.', '[', …) get
			// bracket-quoted so reference ToPaths address them correctly.
			rv, err := renderValue(rc, t[k], pulumido.AppendKeySegment(path, k), refs)
			if err != nil {
				return nil, err
			}
			if rv != removedValue {
				out[k] = rv
			}
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			rv, err := renderValue(rc, e, fmt.Sprintf("%s[%d]", path, i), refs)
			if err != nil {
				return nil, err
			}
			if rv == removedValue {
				// Array elements addressed by references must exist; keep a
				// placeholder the reference will overwrite.
				rv = ""
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil
	}
}

// removedValue marks a map entry fully replaced by a reference.
var removedValue = &struct{ _ byte }{}

// valueMarker temporarily stands in for the reference "${value}"
// placeholder while a template is assembled, so literal "${value}" text
// (from escapes or parameter values) can be distinguished and escaped.
const valueMarker = "\x00PDO_VALUE\x00"

// renderString resolves expressions inside one string. params/self resolve
// immediately; at most one resources.* expression may appear (repeats of
// the same expression are allowed) and it turns the string into a
// DoResource reference (with a template when the string mixes in literal
// text).
func renderString(rc *renderContext, s, path string, refs *[]dov1alpha1.Reference) (any, error) {
	escaped := strings.ReplaceAll(s, "$${", escMarker)
	if err := checkUnterminated(escaped, path); err != nil {
		return nil, err
	}
	matches := exprRe.FindAllStringSubmatchIndex(escaped, -1)
	if len(matches) == 0 {
		return strings.ReplaceAll(escaped, escMarker, "${"), nil
	}

	var resourceExpr string // the single allowed resources.* expression
	var b strings.Builder
	prev := 0
	wholeString := len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(escaped)

	for _, m := range matches {
		b.WriteString(escaped[prev:m[0]])
		prev = m[1]
		expr := strings.TrimSpace(escaped[m[2]:m[3]])
		switch {
		case strings.HasPrefix(expr, "params.") || expr == "params":
			val, ok := pulumido.GetPath(map[string]any{"params": rc.params}, expr)
			if !ok {
				return nil, fmt.Errorf("expression ${%s}: parameter not found", expr)
			}
			if wholeString {
				return val, nil // preserve non-string parameter types
			}
			b.WriteString(pulumido.RenderScalar(val))
		case expr == "self.name":
			b.WriteString(rc.composite.Name)
		case expr == "self.namespace":
			b.WriteString(rc.composite.Namespace)
		case strings.HasPrefix(expr, "resources."):
			if resourceExpr != "" && resourceExpr != expr {
				return nil, fmt.Errorf("value at %q uses two different resources.* expressions; only one source is supported per value", path)
			}
			resourceExpr = expr
			b.WriteString(valueMarker)
		default:
			return nil, fmt.Errorf("unsupported expression ${%s} (want params.*, self.name, self.namespace or resources.*)", expr)
		}
	}
	b.WriteString(escaped[prev:])
	result := strings.ReplaceAll(b.String(), escMarker, "${")

	if resourceExpr == "" {
		return strings.ReplaceAll(result, valueMarker, "${value}"), nil
	}
	// Template mode: literal "${value}" occurrences (user escapes or
	// parameter values) must not be confused with the reference
	// placeholder — escape them for resolveReferences.
	result = strings.ReplaceAll(result, "${value}", "$${value}")
	result = strings.ReplaceAll(result, valueMarker, "${value}")

	// resources.<name>.id or resources.<name>.outputs.<path>
	rest := strings.TrimPrefix(resourceExpr, "resources.")
	parts := strings.SplitN(rest, ".", 2)
	childName, ok := rc.childName[parts[0]]
	if !ok {
		return nil, fmt.Errorf("expression ${%s}: unknown resource %q", resourceExpr, parts[0])
	}
	if len(parts) < 2 {
		return nil, fmt.Errorf("expression ${%s}: missing field (use .id or .outputs.<path>)", resourceExpr)
	}
	var fieldPath string
	switch {
	case parts[1] == "id":
		fieldPath = "status.id"
	case parts[1] == "outputs" || strings.HasPrefix(parts[1], "outputs."):
		fieldPath = "status." + parts[1]
	default:
		return nil, fmt.Errorf("expression ${%s}: field must be id or outputs.<path>", resourceExpr)
	}
	ref := dov1alpha1.Reference{
		ToPath: path,
		From:   dov1alpha1.ReferenceSource{Name: childName, FieldPath: fieldPath},
	}
	if result != "${value}" {
		ref.Template = result
	}
	*refs = append(*refs, ref)
	return removedValue, nil
}

// checkUnterminated rejects strings with a "${" that never closes: silently
// treating a typoed expression as a literal would ship the raw text to the
// cloud provider.
func checkUnterminated(escaped, path string) error {
	spans := exprRe.FindAllStringIndex(escaped, -1)
	idx := 0
	for {
		pos := strings.Index(escaped[idx:], "${")
		if pos < 0 {
			return nil
		}
		pos += idx
		inSpan := false
		for _, span := range spans {
			if pos >= span[0] && pos < span[1] {
				inSpan = true
				idx = span[1]
				break
			}
		}
		if !inSpan {
			return fmt.Errorf("value at %q contains an unterminated expression starting at %q (escape a literal with \"$${\")",
				path, truncateAt(escaped[pos:], 20))
		}
	}
}

func truncateAt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// childResourceName builds a stable, DNS-safe child object name. When the
// combined name is too long it keeps the (distinct) template resource
// suffix and disambiguates the truncated composite prefix with a hash of
// the full name, so long composite names neither collide across templates
// nor produce invalid names.
func childResourceName(composite, resource string) string {
	name := composite + "-" + resource
	if len(name) <= 253 && len(validation.IsDNS1123Subdomain(name)) == 0 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	hash := hex.EncodeToString(sum[:])[:8]
	maxPrefix := 253 - len(resource) - len(hash) - 2 // two joining dashes
	prefix := composite
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], "-.")
	}
	return fmt.Sprintf("%s-%s-%s", prefix, hash, resource)
}

// compositeLabelValue renders a composite name as a valid label value
// (labels cap at 63 characters; names go up to 253), truncating with a
// disambiguating hash when needed. Pruning additionally checks owner
// references, so a truncated label only ever widens the candidate listing.
func compositeLabelValue(name string) string {
	if len(validation.IsValidLabelValue(name)) == 0 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	hash := hex.EncodeToString(sum[:])[:8]
	prefix := strings.TrimRight(name[:min(len(name), 63-len(hash)-1)], "-._")
	return prefix + "-" + hash
}
