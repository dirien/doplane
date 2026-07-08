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
	"time"

	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// Crossplane-compatible lifecycle annotations honored on DoResource.
const (
	// annExternalName records the external resource's provider-assigned
	// name/id. It is persisted immediately after a successful create —
	// before the status write — so a crash between the cloud mutation and
	// the status update no longer orphans (or double-creates) the
	// resource: the next reconcile adopts the annotated external resource
	// instead of creating again.
	annExternalName = "crossplane.io/external-name"
	// annPaused suspends all cloud operations (including deletion) while
	// set to "true"; removing it resumes reconciliation.
	annPaused = "crossplane.io/paused"
	// annPollInterval overrides the default drift-read interval with a Go
	// duration (e.g. "1m", "2h").
	annPollInterval = "crossplane.io/poll-interval"
	// annReconcileRequestedAt wakes a settled resource: any annotation
	// change re-triggers reconciliation (see the controller's predicates),
	// and this one is the conventional place to put a timestamp.
	annReconcileRequestedAt = "crossplane.io/reconcile-requested-at"
)

func paused(res *dov1alpha1.DoResource) bool {
	return res.Annotations[annPaused] == "true"
}

func externalName(res *dov1alpha1.DoResource) string {
	return res.Annotations[annExternalName]
}

// pollInterval returns the drift-read interval: the poll-interval
// annotation when parseable, the given default otherwise (a bad value is
// reported once per sync via a Warning event by the caller).
func pollInterval(res *dov1alpha1.DoResource, fallback time.Duration) (time.Duration, bool) {
	raw, ok := res.Annotations[annPollInterval]
	if !ok || raw == "" {
		return fallback, true
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback, false
	}
	return d, true
}

// reconcileCreate provisions the external resource: an external-name
// annotation adopts an existing one instead of creating a second (it is
// also what a crashed create leaves behind — the annotation is persisted
// before status, closing the create/status crash window).
func (r *DoResourceReconciler) reconcileCreate(ctx context.Context, res *dov1alpha1.DoResource,
	token, pkg string, props map[string]any, hash string,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if at := res.Annotations[annReconcileRequestedAt]; at != "" {
		log.Info("manual reconcile requested", "requested-at", at)
	}
	if name := externalName(res); name != "" {
		if result, handled, err := r.adoptExternal(ctx, res, token, pkg, name, hash); handled {
			return result, err
		}
	}
	log.Info("creating external resource", "type", token)
	id, state, err := r.Runner.Create(ctx, token, pkg, props)
	if err != nil {
		return r.markSyncFailed(ctx, res, "CreateFailed", err, true)
	}
	// The external name is the critical bookkeeping: persist it before the
	// status write, so a crash between the two cannot orphan (or
	// re-create) the just-created resource.
	if err := r.persistExternalName(ctx, res, id); err != nil {
		return ctrl.Result{}, err
	}
	r.Recorder.Eventf(res, "Normal", "Created", "Created external resource %s %q", token, id)
	return r.markSynced(ctx, res, id, state, hash)
}

// adoptExternal takes over an existing external resource named by the
// external-name annotation. handled=false means there is nothing to adopt
// and the caller should create.
func (r *DoResourceReconciler) adoptExternal(ctx context.Context, res *dov1alpha1.DoResource,
	token, pkg, name, hash string,
) (ctrl.Result, bool, error) {
	logf.FromContext(ctx).Info("adopting external resource", "type", token, "id", name)
	state, err := r.Runner.Read(ctx, token, pkg, name)
	switch {
	case errors.Is(err, pulumido.ErrNotFound):
		// Nothing to adopt: create.
		return ctrl.Result{}, false, nil
	case errors.Is(err, pulumido.ErrReadNotSupported):
		// The provider cannot verify; trust the recorded name.
		r.Recorder.Eventf(res, "Normal", "Adopted",
			"Adopted external resource %s %q (unverified: provider does not support read)", token, name)
		result, err := r.markSynced(ctx, res, name, nil, hash)
		return result, true, err
	case err != nil:
		result, ferr := r.markSyncFailed(ctx, res, "AdoptFailed", err, true)
		return result, true, ferr
	default:
		r.Recorder.Eventf(res, "Normal", "Adopted", "Adopted external resource %s %q", token, name)
		result, err := r.markSynced(ctx, res, name, state, hash)
		return result, true, err
	}
}

// persistExternalName durably records the external name annotation with
// conflict retries, detached from reconcile cancellation: it is the
// critical write between the cloud mutation and the status update.
func (r *DoResourceReconciler) persistExternalName(ctx context.Context, res *dov1alpha1.DoResource, name string) error {
	if res.Annotations[annExternalName] == name {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dov1alpha1.DoResource{}
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(res), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		if latest.Annotations == nil {
			latest.Annotations = map[string]string{}
		}
		latest.Annotations[annExternalName] = name
		return r.Update(ctx, latest)
	})
	if err == nil {
		if res.Annotations == nil {
			res.Annotations = map[string]string{}
		}
		res.Annotations[annExternalName] = name
	}
	return err
}
