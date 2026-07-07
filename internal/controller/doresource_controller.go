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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

const (
	doResourceFinalizer = "do.pulumi.com/finalizer"
	// resyncInterval is how often a settled resource is re-read for drift.
	resyncInterval = 10 * time.Minute
)

// DoResourceReconciler reconciles a DoResource object against the cloud via
// `pulumi do`.
type DoResourceReconciler struct {
	client.Client
	// Live reads straight from the API server, bypassing the informer
	// cache. Job-backed reconciles take tens of seconds, easily longer than
	// cache propagation, and acting on a stale object here means duplicate
	// cloud creates. Cloud calls are expensive enough that the extra live
	// GET per reconcile is a bargain.
	Live     client.Reader
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Runner   pulumido.Runner
	Schemas  *pulumido.SchemaCache
}

// +kubebuilder:rbac:groups=do.pulumi.com,resources=doresources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=do.pulumi.com,resources=doresources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=do.pulumi.com,resources=doresources/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update

// Reconcile drives the external resource toward spec:
// create when no id is recorded, patch when the spec generation changed,
// read (refresh) otherwise, and delete on object deletion.
func (r *DoResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Tag the context with this object's identity: the JobRunner derives
	// deterministic Job names from it, so interrupted operations are adopted
	// on retry instead of re-run. The namespace tag lets a
	// per-resource-namespace JobRunner execute in the tenant's namespace.
	ctx = pulumido.WithOwner(ctx, req.String())
	ctx = pulumido.WithNamespace(ctx, req.Namespace)

	res := &dov1alpha1.DoResource{}
	if err := r.reader().Get(ctx, req.NamespacedName, res); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	pkg := res.Spec.Package
	token := res.Spec.Type

	// Resolve the provider profile first: teardown needs the provider's
	// credentials Secret just as much as creation does.
	ctx, pkg, halt, err := r.resolveProvider(ctx, res, pkg, token)
	if halt != nil {
		return *halt, err
	}

	// Deletion: tear down the external resource, then release the finalizer.
	// A vanished provider must not wedge the finalizer — the operation runs
	// with the deployment-default credentials and surfaces any auth failure.
	if !res.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, res)
	}

	if !controllerutil.ContainsFinalizer(res, doResourceFinalizer) {
		controllerutil.AddFinalizer(res, doResourceFinalizer)
		if err := r.Update(ctx, res); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Unpinned providers still work through registry resolution, but their
	// operations are not reproducible and bypass the shared plugin cache.
	if !pulumido.PackagePinned(pkg) {
		r.Recorder.Eventf(res, "Warning", "ProviderNotPinned",
			"provider package is not pinned; set spec.package to name@version for reproducible operations")
	}

	props, err := decodeProperties(res.Spec.Properties)
	if err != nil {
		return r.markSyncFailed(ctx, res, "InvalidProperties", fmt.Errorf("spec.properties is not a JSON object: %w", err), false)
	}

	// Resolve cross-resource references into the properties. Unresolvable
	// references gate readiness; the watch on source objects re-enqueues
	// this resource when they progress.
	if halt, err := r.applyReferences(ctx, res, props); halt != nil {
		return *halt, err
	}

	// Secret input values never pass through the controller: a placeholder
	// satisfies schema validation (required inputs may come from Secrets),
	// and the runner substitutes the real value from an env var injected in
	// the Job's namespace.
	ctx, halt, err = r.stageSecretInputs(ctx, res, props)
	if halt != nil {
		return *halt, err
	}

	// Validate the fully resolved inputs against the provider's JSON schema
	// from the Pulumi registry before touching the cloud.
	schema, err := r.Schemas.Get(ctx, schemaPackage(pkg, token), token)
	if err != nil {
		return r.markSyncFailed(ctx, res, "SchemaUnavailable", err, true)
	}
	violations, err := schema.Validate(token, props)
	if err != nil {
		return r.markSyncFailed(ctx, res, "UnknownResourceType", err, false)
	}
	if len(violations) > 0 {
		return r.markSyncFailed(ctx, res, "ValidationFailed",
			fmt.Errorf("schema validation (%s@%s): %s", schema.Name, schema.Version, strings.Join(violations, "; ")), false)
	}

	// The applied hash covers spec edits AND propagated reference values:
	// either kind of change triggers a patch. Secret input values are not
	// part of the hash (the controller never sees them) — their Secrets'
	// resourceVersions stand in, so rotation re-patches the resource.
	hash, err := hashProps(props)
	if err != nil {
		return ctrl.Result{}, err
	}
	hash = r.mixSecretVersions(ctx, res, hash)

	// Component resources are orchestrated through an ephemeral engine
	// (stateless `pulumi do` cannot construct them); the exported
	// checkpoint is persisted in status.engineState.
	if schema.Resources[token].IsComponent {
		if len(res.Spec.ValuesFrom) > 0 {
			return r.markSyncFailed(ctx, res, "ValuesFromUnsupported",
				fmt.Errorf("valuesFrom is not supported for component resources: the engine checkpoint persisted in status.engineState would contain the secret values"), false)
		}
		return r.reconcileComponent(ctx, res, token, pkg, props, hash)
	}

	switch {
	case res.Status.ID == "":
		log.Info("creating external resource", "type", token)
		id, state, err := r.Runner.Create(ctx, token, pkg, props)
		if err != nil {
			return r.markSyncFailed(ctx, res, "CreateFailed", err, true)
		}
		r.Recorder.Eventf(res, "Normal", "Created", "Created external resource %s %q", token, id)
		return r.markSynced(ctx, res, id, state, hash)

	case res.Status.AppliedHash != hash:
		log.Info("updating external resource", "type", token, "id", res.Status.ID)
		state, err := r.Runner.Patch(ctx, token, pkg, res.Status.ID, props)
		if errors.Is(err, pulumido.ErrReadNotSupported) {
			// pulumi do patch reads before patching; providers without read
			// support cannot be updated in place. Terminal until spec changes.
			return r.markSyncFailed(ctx, res, "UpdateNotSupported", err, false)
		}
		if errors.Is(err, pulumido.ErrNotFound) {
			// The external resource vanished under us; clear the recorded id
			// so the next reconcile recreates it instead of retrying a patch
			// against nothing forever.
			log.Info("external resource gone during update, will recreate", "id", res.Status.ID)
			r.Recorder.Eventf(res, "Warning", "Drifted", "External resource %s %q no longer exists; recreating", token, res.Status.ID)
			res.Status.ID = ""
			res.Status.Outputs = nil
			res.Status.AppliedHash = ""
			setCondition(res, dov1alpha1.ConditionReady, metav1.ConditionFalse, "Missing", "external resource no longer exists")
			if err := r.persistStatus(ctx, res); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		if err != nil {
			return r.markSyncFailed(ctx, res, "UpdateFailed", err, true)
		}
		r.Recorder.Eventf(res, "Normal", "Updated", "Updated external resource %s %q", token, res.Status.ID)
		return r.markSynced(ctx, res, res.Status.ID, state, hash)

	default:
		// Settled: refresh observed state for drift visibility, when the
		// provider supports read.
		state, err := r.Runner.Read(ctx, token, pkg, res.Status.ID)
		switch {
		case errors.Is(err, pulumido.ErrReadNotSupported):
			return r.markSynced(ctx, res, res.Status.ID, nil, hash)
		case errors.Is(err, pulumido.ErrNotFound):
			// The external resource vanished; clear the id so the next
			// reconcile recreates it.
			log.Info("external resource disappeared, will recreate", "id", res.Status.ID)
			r.Recorder.Eventf(res, "Warning", "Drifted", "External resource %s %q no longer exists; recreating", token, res.Status.ID)
			res.Status.ID = ""
			res.Status.Outputs = nil
			res.Status.AppliedHash = ""
			setCondition(res, dov1alpha1.ConditionReady, metav1.ConditionFalse, "Missing", "external resource no longer exists")
			if err := r.persistStatus(ctx, res); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		case err != nil:
			return r.markSyncFailed(ctx, res, "ReadFailed", err, true)
		}
		return r.markSynced(ctx, res, res.Status.ID, state, hash)
	}
}

// reconcileComponent drives a component resource through the ephemeral
// engine: create when no state exists, update on hash changes, and no drift
// reads (the engine has no cheap read).
func (r *DoResourceReconciler) reconcileComponent(ctx context.Context, res *dov1alpha1.DoResource, token, pkg string, props map[string]any, hash string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	switch {
	case res.Status.ID == "" || res.Status.EngineState == nil:
		log.Info("constructing component resource", "type", token)
		id, outputs, state, err := r.Runner.CreateComponent(ctx, token, pkg, props)
		if err != nil {
			return r.markSyncFailed(ctx, res, "CreateFailed", err, true)
		}
		r.Recorder.Eventf(res, "Normal", "Created", "Constructed component %s %q", token, id)
		res.Status.EngineState = &apiextensionsv1.JSON{Raw: state}
		return r.markSynced(ctx, res, id, outputs, hash)

	case res.Status.AppliedHash != hash:
		log.Info("updating component resource", "type", token, "id", res.Status.ID)
		outputs, state, err := r.Runner.UpdateComponent(ctx, token, pkg, props, res.Status.EngineState.Raw)
		if err != nil {
			return r.markSyncFailed(ctx, res, "UpdateFailed", err, true)
		}
		r.Recorder.Eventf(res, "Normal", "Updated", "Updated component %s %q", token, res.Status.ID)
		res.Status.EngineState = &apiextensionsv1.JSON{Raw: state}
		return r.markSynced(ctx, res, res.Status.ID, outputs, hash)

	default:
		// Settled: no engine-side read; status already reflects the last
		// applied state.
		return r.markSynced(ctx, res, res.Status.ID, nil, hash)
	}
}

// reconcileDelete tears down the external resource once no dependents
// remain, then releases the finalizer.
func (r *DoResourceReconciler) reconcileDelete(ctx context.Context, res *dov1alpha1.DoResource) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if !controllerutil.ContainsFinalizer(res, doResourceFinalizer) {
		return ctrl.Result{}, nil
	}
	// Blocking teardown: while other DoResources reference this one, its
	// external resource must outlive theirs (reverse-topological deletion).
	// Dependent deletion events re-enqueue this object.
	if deps, err := r.blockingDependents(ctx, res); err != nil {
		return ctrl.Result{}, err
	} else if len(deps) > 0 {
		msg := fmt.Sprintf("deletion blocked by dependent resources: %s", strings.Join(deps, ", "))
		log.Info("deletion blocked", "dependents", deps)
		setCondition(res, dov1alpha1.ConditionReady, metav1.ConditionFalse, "BlockedByDependents", msg)
		r.Recorder.Eventf(res, "Normal", "BlockedByDependents", "%s", msg)
		if err := r.persistStatus(ctx, res); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	token, pkg := res.Spec.Type, res.Spec.Package
	if res.Spec.DeletionPolicy != dov1alpha1.DeletionOrphan && res.Status.ID != "" {
		log.Info("deleting external resource", "type", token, "id", res.Status.ID)
		var err error
		if res.Status.EngineState != nil {
			// Component resource: destroy through the engine from the
			// persisted checkpoint.
			err = r.Runner.DeleteComponent(ctx, token, pkg, res.Status.EngineState.Raw)
		} else {
			err = r.Runner.Delete(ctx, token, pkg, res.Status.ID)
		}
		switch {
		case err == nil:
			r.Recorder.Eventf(res, "Normal", "Deleted", "Deleted external resource %s %q", token, res.Status.ID)
		case errors.Is(err, pulumido.ErrNotFound):
			log.Info("external resource already gone", "id", res.Status.ID)
		default:
			r.Recorder.Eventf(res, "Warning", "DeleteFailed", "Failed to delete %s %q: %s", token, res.Status.ID, compact(err.Error()))
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(res, doResourceFinalizer)
	if err := r.Update(ctx, res); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

// markSynced records the freshly observed state in status (persisted in
// etcd) and sets Ready/Synced to True. A nil state leaves outputs untouched.
func (r *DoResourceReconciler) markSynced(ctx context.Context, res *dov1alpha1.DoResource, id string, state map[string]any, appliedHash string) (ctrl.Result, error) {
	res.Status.ID = id
	if state != nil {
		raw, err := json.Marshal(state)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("marshaling outputs: %w", err)
		}
		res.Status.Outputs = &apiextensionsv1.JSON{Raw: raw}
	}
	res.Status.AppliedHash = appliedHash
	res.Status.ObservedGeneration = res.Generation
	setCondition(res, dov1alpha1.ConditionSynced, metav1.ConditionTrue, "Synced", "last operation against the provider succeeded")
	setCondition(res, dov1alpha1.ConditionReady, metav1.ConditionTrue, "Available", "external resource exists")
	if err := r.persistStatus(ctx, res); err != nil {
		return ctrl.Result{}, err
	}
	// Status is durable — connection details derive from it, so a failed
	// publish retries without re-running the cloud operation.
	if err := r.publishConnectionSecret(ctx, res); err != nil {
		r.Recorder.Eventf(res, "Warning", "ConnectionSecretFailed", "%s", compact(err.Error()))
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// markSyncFailed records a failure condition. When retryOp is true the error
// is returned so controller-runtime backs off and retries; otherwise the
// failure is terminal until the spec changes. When the runner supplied a
// typed failure code it becomes the condition reason, so `kubectl get`
// shows the actual failure class (e.g. RegistryAuthMissing) instead of a
// generic phase name.
func (r *DoResourceReconciler) markSyncFailed(ctx context.Context, res *dov1alpha1.DoResource, reason string, opErr error, retryOp bool) (ctrl.Result, error) {
	var coded *pulumido.CodedError
	if errors.As(opErr, &coded) && coded.Code != "" {
		reason = coded.Code
	}
	logf.FromContext(ctx).Error(opErr, "reconcile failed", "reason", reason)
	r.Recorder.Eventf(res, "Warning", reason, "%s", compact(opErr.Error()))
	setCondition(res, dov1alpha1.ConditionSynced, metav1.ConditionFalse, reason, compact(opErr.Error()))
	if res.Status.ID == "" {
		setCondition(res, dov1alpha1.ConditionReady, metav1.ConditionFalse, reason, "external resource not provisioned")
	}
	if err := r.persistStatus(ctx, res); err != nil {
		return ctrl.Result{}, err
	}
	if retryOp {
		return ctrl.Result{}, opErr
	}
	return ctrl.Result{}, nil
}

// persistStatus writes res.Status durably, retrying on conflicts against a
// live read. Losing a status write after a cloud mutation would orphan the
// external resource (the next reconcile would create it again) — so the
// write is detached from reconcile cancellation: during manager shutdown the
// reconcile ctx is already canceled while in-flight work is drained, and
// this is precisely the write that must still land.
func (r *DoResourceReconciler) persistStatus(ctx context.Context, res *dov1alpha1.DoResource) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	status := res.Status
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dov1alpha1.DoResource{}
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(res), latest); err != nil {
			// Object deleted mid-reconcile: nothing left to record.
			return client.IgnoreNotFound(err)
		}
		latest.Status = status
		return client.IgnoreNotFound(r.Status().Update(ctx, latest))
	})
}

// reader returns the live API reader, falling back to the cached client.
func (r *DoResourceReconciler) reader() client.Reader {
	if r.Live != nil {
		return r.Live
	}
	return r.Client
}

func setCondition(res *dov1alpha1.DoResource, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&res.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: res.Generation,
	})
}

// decodeProperties turns spec.properties into a map, preserving number
// precision.
func decodeProperties(j *apiextensionsv1.JSON) (map[string]any, error) {
	if j == nil || len(j.Raw) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(j.Raw)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// schemaPackage picks the package to fetch the schema for: an explicit
// spec.package wins, otherwise it is inferred from the type token.
func schemaPackage(pkg, token string) string {
	if pkg != "" {
		return pkg
	}
	return pulumido.PackageForToken(token)
}

// compact collapses CLI error output into a single condition-friendly line.
func compact(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 700 {
		s = s[:700] + "…"
	}
	return s
}

// SetupWithManager sets up the controller with the Manager.
func (r *DoResourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &dov1alpha1.DoResource{}, providerRefIndexKey,
		func(o client.Object) []string {
			res, ok := o.(*dov1alpha1.DoResource)
			if !ok || res.Spec.ProviderRef == nil || refKind(res.Spec.ProviderRef) != dov1alpha1.ProviderKindCluster {
				return nil
			}
			return []string{res.Spec.ProviderRef.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &dov1alpha1.DoResource{}, providerConfigIndexKey,
		func(o client.Object) []string {
			res, ok := o.(*dov1alpha1.DoResource)
			if !ok || res.Spec.ProviderRef == nil || refKind(res.Spec.ProviderRef) != dov1alpha1.ProviderKindConfig {
				return nil
			}
			return []string{res.Spec.ProviderRef.Name}
		}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		// Generation-gated: our own status writes must not re-trigger the
		// object's reconcile, or a provider with volatile outputs turns
		// every drift read into a self-sustaining loop of read Jobs.
		// Deletion bumps the generation; periodic drift reads come from
		// RequeueAfter in markSynced.
		For(&dov1alpha1.DoResource{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("doresource").
		// Any DoResource event (including status-only changes) also wakes
		// its graph neighbors: dependents (value propagation, readiness
		// gating) and sources (deletion unblocking).
		Watches(&dov1alpha1.DoResource{}, handler.EnqueueRequestsFromMapFunc(r.mapGraphNeighbors)).
		// Provider profile edits change resolved packages and credentials
		// for every resource referencing them.
		Watches(&dov1alpha1.DoProvider{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderResources)).
		Watches(&dov1alpha1.DoProviderConfig{}, handler.EnqueueRequestsFromMapFunc(r.mapProviderConfigResources)).
		// Reconciles block on runner Jobs for tens of seconds; allow a few
		// objects in flight. The same object is never reconciled concurrently.
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}

// mapGraphNeighbors maps a DoResource event to its dependents and sources.
func (r *DoResourceReconciler) mapGraphNeighbors(ctx context.Context, obj client.Object) []reconcile.Request {
	res, ok := obj.(*dov1alpha1.DoResource)
	if !ok {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(res.Spec.References))
	if deps, err := r.dependentsOf(ctx, res); err == nil {
		for _, name := range deps {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: res.Namespace, Name: name}})
		}
	}
	for _, ref := range res.Spec.References {
		if ref.From.Name == res.Name {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: res.Namespace, Name: ref.From.Name}})
	}
	return reqs
}
