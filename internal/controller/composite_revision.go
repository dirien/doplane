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
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

// +kubebuilder:rbac:groups=do.pulumi.com,resources=docompositedefinitionrevisions,verbs=get;list;watch;create

// resolveRevision guarantees an immutable snapshot of the definition's
// current spec exists and picks the revision this composite renders from:
// an explicit revisionRef always wins; Manual instances stay on the
// revision recorded in status (adopting the current latest on first
// render); Automatic instances track the latest. This is what keeps a
// definition edit from silently rewriting every instance.
func (r *DoCompositeReconciler) resolveRevision(ctx context.Context, comp *dov1alpha1.DoComposite,
	def *dov1alpha1.DoCompositeDefinition,
) (*dov1alpha1.DoCompositeDefinitionRevision, error) {
	latest, err := r.ensureLatestRevision(ctx, def)
	if err != nil {
		return nil, err
	}
	var pinned string
	switch {
	case comp.Spec.RevisionRef != nil:
		pinned = comp.Spec.RevisionRef.Name
	case comp.Spec.UpdatePolicy == dov1alpha1.UpdateManual && comp.Status.Revision != "":
		pinned = comp.Status.Revision
	default:
		return latest, nil
	}
	if pinned == latest.Name {
		return latest, nil
	}
	rev := &dov1alpha1.DoCompositeDefinitionRevision{}
	if err := r.reader().Get(ctx, types.NamespacedName{Name: pinned}, rev); err != nil {
		return nil, err
	}
	if rev.Spec.DefinitionName != def.Name {
		return nil, fmt.Errorf("revision %q snapshots definition %q, not %q", pinned, rev.Spec.DefinitionName, def.Name)
	}
	return rev, nil
}

// ensureLatestRevision returns the newest revision of the definition,
// creating the next one when the definition spec has changed since. The
// deterministic "<definition>-v<N>" name makes concurrent creators
// converge: the loser of the race re-reads the winner's object.
func (r *DoCompositeReconciler) ensureLatestRevision(ctx context.Context, def *dov1alpha1.DoCompositeDefinition) (
	*dov1alpha1.DoCompositeDefinitionRevision, error,
) {
	var list dov1alpha1.DoCompositeDefinitionRevisionList
	if err := r.List(ctx, &list); err != nil {
		return nil, err
	}
	var latest *dov1alpha1.DoCompositeDefinitionRevision
	for i := range list.Items {
		rev := &list.Items[i]
		if rev.Spec.DefinitionName != def.Name {
			continue
		}
		if latest == nil || rev.Spec.Revision > latest.Spec.Revision {
			latest = rev
		}
	}
	if latest != nil && equality.Semantic.DeepEqual(latest.Spec.Definition, def.Spec) {
		return latest, nil
	}

	next := int64(1)
	if latest != nil {
		next = latest.Spec.Revision + 1
	}
	rev := &dov1alpha1.DoCompositeDefinitionRevision{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-v%d", def.Name, next)},
		Spec: dov1alpha1.DoCompositeDefinitionRevisionSpec{
			DefinitionName: def.Name,
			Revision:       next,
			Definition:     *def.Spec.DeepCopy(),
		},
	}
	// Revisions die with their definition.
	if err := controllerutil.SetControllerReference(def, rev, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, rev); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		if err := r.reader().Get(ctx, types.NamespacedName{Name: rev.Name}, rev); err != nil {
			return nil, err
		}
	}
	return rev, nil
}
