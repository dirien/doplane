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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

// +kubebuilder:rbac:groups=do.pulumi.com,resources=docompositedefinitionrevisions,verbs=get;list;watch;create;delete

// revisionHistoryLimit bounds how many revisions of one definition are kept
// beyond the ones still referenced: definitions are edited indefinitely on a
// long-running cluster, and without pruning every edit leaks one revision
// object into etcd and the informer cache forever.
const revisionHistoryLimit = 10

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
		return nil, fmt.Errorf("%w: revision %q snapshots definition %q, not %q",
			errRevisionMismatch, pinned, rev.Spec.DefinitionName, def.Name)
	}
	return rev, nil
}

// errRevisionMismatch marks a revisionRef that pins a revision belonging to a
// different definition — a user misconfiguration, terminal until the spec
// changes, not a transient error to retry forever.
var errRevisionMismatch = errors.New("revisionRef snapshots a different definition")

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
	r.pruneRevisions(ctx, def, rev.Spec.Revision)
	return rev, nil
}

// pruneRevisions deletes old revisions of def beyond revisionHistoryLimit.
// Only revision creation triggers pruning (edits are rare), and a revision
// still referenced by any composite — pinned via spec.revisionRef or
// recorded as status.revision of a Manual instance — is never deleted, so
// pinned rollbacks keep working. Best effort: a failed list or delete is
// retried on the next definition edit.
func (r *DoCompositeReconciler) pruneRevisions(ctx context.Context, def *dov1alpha1.DoCompositeDefinition, newest int64) {
	var revs dov1alpha1.DoCompositeDefinitionRevisionList
	if err := r.List(ctx, &revs); err != nil {
		return
	}
	// Composites are listed live (not from the cache) and filtered in memory:
	// pruning runs only on rare definition edits, and a revision pinned by a
	// just-written spec.revisionRef the informer cache has not yet observed
	// must survive — whatever definition the pinning composite names.
	var comps dov1alpha1.DoCompositeList
	if err := r.reader().List(ctx, &comps); err != nil {
		return
	}
	referenced := map[string]struct{}{}
	for i := range comps.Items {
		comp := &comps.Items[i]
		if comp.Spec.RevisionRef != nil {
			referenced[comp.Spec.RevisionRef.Name] = struct{}{}
		}
		if comp.Status.Revision != "" {
			referenced[comp.Status.Revision] = struct{}{}
		}
	}
	log := logf.FromContext(ctx)
	for i := range revs.Items {
		rev := &revs.Items[i]
		if rev.Spec.DefinitionName != def.Name || rev.Spec.Revision > newest-int64(revisionHistoryLimit) {
			continue
		}
		if _, pinned := referenced[rev.Name]; pinned {
			continue
		}
		if err := r.Delete(ctx, rev, client.Preconditions{UID: &rev.UID}); err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "pruning definition revision failed", "revision", rev.Name)
			continue
		}
		log.Info("pruned definition revision beyond history limit", "revision", rev.Name, "definition", def.Name)
	}
}
