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
	"strconv"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// annApproveReplacement authorizes one replacement: its value must equal
// the resource's current generation, so an old approval never authorizes a
// later, different change.
const annApproveReplacement = "do.pulumi.com/approve-replacement"

// replacementApproved reports whether this exact spec generation's
// replacement was approved.
func replacementApproved(res *dov1alpha1.DoResource) bool {
	return res.Annotations[annApproveReplacement] == strconv.FormatInt(res.Generation, 10)
}

// protected reports whether accidental replacement is guarded: only an
// explicit protect=false opts a resource out (unset behaves protected —
// stateful resources must never be replaced by accident).
func protected(res *dov1alpha1.DoResource) bool {
	return res.Spec.Protect == nil || *res.Spec.Protect
}

// reconcileReplacement handles an update that cannot be applied in place.
// Protected resources stop with a terminal ReplacementRequired condition
// until approved for this generation; approved (or explicitly unprotected)
// resources replace create-before-delete where the provider identity
// allows it, falling back to delete-before-create on an identity conflict.
// Dependents cascade afterwards through normal hash propagation.
func (r *DoResourceReconciler) reconcileReplacement(ctx context.Context, res *dov1alpha1.DoResource,
	token, pkg string, props map[string]any, hash string, patchErr error,
) (ctrl.Result, error) {
	if protected(res) && !replacementApproved(res) {
		return r.markSyncFailed(ctx, res, "ReplacementRequired",
			fmt.Errorf("the change cannot be applied in place and replacing external resource %q needs approval: "+
				"annotate with %s=%d (or set spec.protect=false): %w",
				res.Status.ID, annApproveReplacement, res.Generation, patchErr), false)
	}

	log := logf.FromContext(ctx)
	oldID := res.Status.ID

	// Create-before-delete keeps dependents working through the swap (no
	// window without e.g. a bucket policy). Fixed-identity resources
	// collide on create; only then delete first.
	log.Info("replacing external resource (create-before-delete)", "type", token, "old-id", oldID)
	id, state, err := r.Runner.Create(ctx, token, pkg, props)
	if pulumido.IsAlreadyExists(err) {
		log.Info("identity conflict; replacing delete-before-create", "type", token, "id", oldID)
		if derr := r.Runner.Delete(ctx, token, pkg, oldID); derr != nil && !errors.Is(derr, pulumido.ErrNotFound) {
			return r.markSyncFailed(ctx, res, "ReplaceFailed", derr, true)
		}
		// A crash from here recovers through the existing drift path: the
		// recorded id no longer resolves and the resource is recreated.
		id, state, err = r.Runner.Create(ctx, token, pkg, props)
	}
	if err != nil {
		return r.markSyncFailed(ctx, res, "ReplaceFailed", err, true)
	}
	if err := r.persistExternalName(ctx, res, id); err != nil {
		return ctrl.Result{}, err
	}
	if id != oldID {
		if derr := r.Runner.Delete(ctx, token, pkg, oldID); derr != nil && !errors.Is(derr, pulumido.ErrNotFound) {
			// The new resource is live; leaking the old one is recoverable
			// by hand and better than failing the swap.
			r.Recorder.Eventf(res, "Warning", "OrphanedOldResource",
				"replaced %s but could not delete the previous external resource %q: %s", token, oldID, compact(derr.Error()))
		}
	}
	r.Recorder.Eventf(res, "Normal", "Replaced", "Replaced external resource %s %q with %q", token, oldID, id)
	return r.markSynced(ctx, res, id, state, hash)
}
