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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// applyReferences runs the reference phase of a reconcile: cycle
// detection, resolution into props, and readiness gating. A non-nil halt
// means the reconcile must return (*halt, err) immediately.
func (r *DoResourceReconciler) applyReferences(ctx context.Context, res *dov1alpha1.DoResource, props map[string]any) (*ctrl.Result, error) {
	if len(res.Spec.References) == 0 {
		return nil, nil
	}
	if cycle, cyclic := r.detectCycle(ctx, res); cyclic {
		result, err := r.markSyncFailed(ctx, res, "CyclicReference", fmt.Errorf("reference cycle detected: %s", cycle), false)
		return &result, err
	}
	waiting, refErr := r.resolveReferences(ctx, res, props)
	if refErr != nil {
		result, err := r.markSyncFailed(ctx, res, "InvalidReferences", refErr, false)
		return &result, err
	}
	if len(waiting) > 0 {
		msg := "waiting for: " + strings.Join(waiting, "; ")
		logf.FromContext(ctx).Info("references not yet resolvable", "waiting", waiting)
		setCondition(res, dov1alpha1.ConditionReady, metav1.ConditionFalse, "WaitingForDependency", msg)
		if err := r.persistStatus(ctx, res); err != nil {
			return &ctrl.Result{}, err
		}
		// The dependency watch is the primary wake-up; requeue is a backstop.
		return &ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	return nil, nil
}

// resolveReferences resolves spec.references into props (mutating it).
// It returns the list of not-yet-resolvable references (dependency missing
// or field not populated) and a terminal error for structurally invalid
// references.
func (r *DoResourceReconciler) resolveReferences(ctx context.Context, res *dov1alpha1.DoResource, props map[string]any) ([]string, error) {
	var waiting []string
	for i, ref := range res.Spec.References {
		src := &dov1alpha1.DoResource{}
		err := r.reader().Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: ref.From.Name}, src)
		switch {
		case apierrors.IsNotFound(err):
			waiting = append(waiting, fmt.Sprintf("%s (object not found)", ref.From.Name))
			continue
		case err != nil:
			return nil, fmt.Errorf("reading reference source %q: %w", ref.From.Name, err)
		}
		value, ok, err := resolveFieldPath(src, ref.From.FieldPath)
		if err != nil {
			return nil, fmt.Errorf("references[%d]: %w", i, err)
		}
		if !ok {
			waiting = append(waiting, fmt.Sprintf("%s (%s not yet available)", ref.From.Name, ref.From.FieldPath))
			continue
		}
		if ref.Template != "" {
			value = expandTemplate(ref.Template, pulumido.RenderScalar(value))
		}
		if err := pulumido.SetPath(props, ref.ToPath, value); err != nil {
			return nil, fmt.Errorf("references[%d]: setting %q: %w", i, ref.ToPath, err)
		}
	}
	return waiting, nil
}

// expandTemplate substitutes rendered into every "${value}" placeholder.
// "$${value}" is an escape producing a literal "${value}".
func expandTemplate(tpl, rendered string) string {
	const literalMark = "\x00DOPLANE_LITERAL\x00"
	out := strings.ReplaceAll(tpl, "$${value}", literalMark)
	out = strings.ReplaceAll(out, "${value}", rendered)
	return strings.ReplaceAll(out, literalMark, "${value}")
}

// resolveFieldPath reads "status.id" or a "status.outputs.*" path from a
// source DoResource. The boolean reports whether the value is populated.
func resolveFieldPath(src *dov1alpha1.DoResource, fieldPath string) (any, bool, error) {
	switch {
	case fieldPath == "status.id":
		return src.Status.ID, src.Status.ID != "", nil
	case fieldPath == "status.outputs" || strings.HasPrefix(fieldPath, "status.outputs."):
		if src.Status.Outputs == nil || len(src.Status.Outputs.Raw) == 0 {
			return nil, false, nil
		}
		dec := json.NewDecoder(strings.NewReader(string(src.Status.Outputs.Raw)))
		dec.UseNumber()
		var outputs map[string]any
		if err := dec.Decode(&outputs); err != nil {
			return nil, false, fmt.Errorf("source %q has unparseable outputs: %w", src.Name, err)
		}
		if fieldPath == "status.outputs" {
			return outputs, true, nil
		}
		v, ok := pulumido.GetPath(outputs, strings.TrimPrefix(fieldPath, "status.outputs."))
		return v, ok, nil
	default:
		return nil, false, fmt.Errorf("fieldPath %q must be \"status.id\" or start with \"status.outputs.\"", fieldPath)
	}
}

// dependentsOf lists names of same-namespace DoResources that reference res
// (excluding res itself). The namespace-scoped list comes from the informer
// cache in production; filtering in memory keeps this index-free.
func (r *DoResourceReconciler) dependentsOf(ctx context.Context, res *dov1alpha1.DoResource) ([]string, error) {
	var list dov1alpha1.DoResourceList
	if err := r.List(ctx, &list, client.InNamespace(res.Namespace)); err != nil {
		return nil, err
	}
	var names []string
	for i := range list.Items {
		item := &list.Items[i]
		if item.Name == res.Name {
			continue
		}
		for _, ref := range item.Spec.References {
			if ref.From.Name == res.Name {
				names = append(names, item.Name)
				break
			}
		}
	}
	return names, nil
}

// blockingDependents returns the dependents that must be torn down before
// res may delete its external resource. A dependent that is itself
// terminating does not block when it has nothing external left to tear
// down, or when it forms a reference cycle with res and loses the
// deterministic name tie-break — without that, mutually-referencing
// resources would wedge in Terminating forever, each waiting for the other.
func (r *DoResourceReconciler) blockingDependents(ctx context.Context, res *dov1alpha1.DoResource) ([]string, error) {
	deps, err := r.dependentsOf(ctx, res)
	if err != nil {
		return nil, err
	}
	blocking := make([]string, 0, len(deps))
	for _, name := range deps {
		dep := &dov1alpha1.DoResource{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: name}, dep); err != nil {
			continue // dependent already gone
		}
		if !dep.DeletionTimestamp.IsZero() {
			if dep.Status.ID == "" {
				continue // nothing external left; it will release shortly
			}
			if r.reaches(ctx, res, dep.Name) && res.Name > dep.Name {
				// Mutual (cyclic) teardown: the lexicographically greater
				// name proceeds first so exactly one side yields.
				continue
			}
		}
		blocking = append(blocking, name)
	}
	return blocking, nil
}

// reaches reports whether res transitively references target.
func (r *DoResourceReconciler) reaches(ctx context.Context, res *dov1alpha1.DoResource, target string) bool {
	visited := map[string]bool{res.Name: true}
	queue := make([]string, 0, len(res.Spec.References))
	for _, ref := range res.Spec.References {
		queue = append(queue, ref.From.Name)
	}
	for len(queue) > 0 && len(visited) < 64 {
		name := queue[0]
		queue = queue[1:]
		if name == target {
			return true
		}
		if visited[name] {
			continue
		}
		visited[name] = true
		cur := &dov1alpha1.DoResource{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: name}, cur); err != nil {
			continue
		}
		for _, ref := range cur.Spec.References {
			queue = append(queue, ref.From.Name)
		}
	}
	return false
}

// detectCycle walks the reference graph from res; it reports a
// human-readable cycle path when res is reachable from itself.
func (r *DoResourceReconciler) detectCycle(ctx context.Context, res *dov1alpha1.DoResource) (string, bool) {
	visited := map[string]bool{}
	var walk func(name string, path []string) (string, bool)
	walk = func(name string, path []string) (string, bool) {
		if len(path) > 32 {
			return "", false
		}
		cur := &dov1alpha1.DoResource{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: name}, cur); err != nil {
			return "", false
		}
		for _, ref := range cur.Spec.References {
			next := ref.From.Name
			if next == res.Name {
				return strings.Join(append(path, name, next), " -> "), true
			}
			if visited[next] {
				continue
			}
			visited[next] = true
			if cycle, found := walk(next, append(path, name)); found {
				return cycle, true
			}
		}
		return "", false
	}
	return walk(res.Name, nil)
}

// hashProps produces a stable short hash of fully resolved properties.
// encoding/json marshals map keys in sorted order, making this canonical.
func hashProps(props map[string]any) (string, error) {
	raw, err := json.Marshal(props)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:16], nil
}
