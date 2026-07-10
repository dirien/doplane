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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

const (
	labelComposite     = "do.pulumi.com/composite"
	labelCompositeItem = "do.pulumi.com/composite-resource"
	labelManagedByKey  = "app.kubernetes.io/managed-by"

	// compositeDefIndexKey indexes DoComposites by the definition they use.
	compositeDefIndexKey = "spec.definition"
)

// DoCompositeReconciler expands DoComposites into child DoResources — one
// visible Kubernetes object per underlying Pulumi resource — and rolls up
// their readiness.
type DoCompositeReconciler struct {
	client.Client
	// Live bypasses the informer cache; see DoResourceReconciler.Live.
	Live     client.Reader
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=do.pulumi.com,resources=docomposites,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=do.pulumi.com,resources=docomposites/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=do.pulumi.com,resources=docomposites/finalizers,verbs=update
// +kubebuilder:rbac:groups=do.pulumi.com,resources=docompositedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=do.pulumi.com,resources=docompositedefinitions/status,verbs=get;update;patch

// Reconcile renders the definition with the composite's parameters, applies
// the resulting child DoResources, prunes removed ones and aggregates
// status.
func (r *DoCompositeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	comp := &dov1alpha1.DoComposite{}
	if err := r.reader().Get(ctx, req.NamespacedName, comp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// Children carry owner references; garbage collection plus the resource
	// graph's ordered teardown handle deletion.
	if !comp.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	def := &dov1alpha1.DoCompositeDefinition{}
	if err := r.reader().Get(ctx, types.NamespacedName{Name: comp.Spec.Definition}, def); err != nil {
		if apierrors.IsNotFound(err) {
			return r.markCompositeFailed(ctx, comp, "DefinitionNotFound",
				fmt.Errorf("DoCompositeDefinition %q not found", comp.Spec.Definition))
		}
		return ctrl.Result{}, err
	}

	r.updateDefinitionUsage(ctx, def)

	// Render from the revision the instance's update policy selects: a
	// definition edit only reaches Automatic instances (and pinned ones
	// once a human moves their revisionRef).
	revision, err := r.resolveRevision(ctx, comp, def)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.markCompositeFailed(ctx, comp, "RevisionNotFound", err)
		}
		// A revisionRef pinned to another definition's revision is a permanent
		// user error: surface it as a condition instead of hot-looping.
		if errors.Is(err, errRevisionMismatch) {
			return r.markCompositeFailed(ctx, comp, "RevisionInvalid", err)
		}
		return ctrl.Result{}, err
	}
	effective := def.DeepCopy()
	effective.Spec = *revision.Spec.Definition.DeepCopy()

	children, err := renderComposite(comp, effective)
	if err != nil {
		return r.markCompositeFailed(ctx, comp, "RenderFailed", err)
	}

	// Apply children and collect their observed state.
	statuses := make([]dov1alpha1.CompositeResourceStatus, 0, len(children))
	ready := 0
	replacing := false
	keep := map[string]struct{}{}
	for _, desired := range children {
		keep[desired.Name] = struct{}{}
		st, applyErr := r.applyChild(ctx, comp, desired)
		if applyErr != nil {
			// Optimistic-concurrency conflicts are transient: retry with
			// backoff instead of flagging the composite as failed.
			if apierrors.IsConflict(applyErr) {
				return ctrl.Result{}, applyErr
			}
			if errors.Is(applyErr, errApplyChild) {
				return r.markCompositeFailed(ctx, comp, "ApplyFailed", applyErr)
			}
			return ctrl.Result{}, applyErr
		}
		if st.replacing {
			replacing = true
		}
		if st.status.Ready {
			ready++
		}
		statuses = append(statuses, st.status)
	}

	// Prune children removed from the definition. The composite label is
	// user-settable, so it only narrows the listing; deletion additionally
	// requires this composite's owner reference — pruning must never reach
	// a DoResource (and its cloud resource) we do not actually own.
	var owned dov1alpha1.DoResourceList
	if err := r.List(ctx, &owned, client.InNamespace(comp.Namespace),
		client.MatchingLabels{labelComposite: compositeLabelValue(comp.Name)}); err != nil {
		return ctrl.Result{}, err
	}
	for i := range owned.Items {
		child := &owned.Items[i]
		if _, ok := keep[child.Name]; ok {
			continue
		}
		if !metav1.IsControlledBy(child, comp) {
			continue
		}
		if child.DeletionTimestamp.IsZero() {
			log.Info("pruning child resource no longer in definition", "name", child.Name)
			if err := r.Delete(ctx, child); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			r.Recorder.Eventf(comp, "Normal", "Pruned", "Deleted child DoResource %q (removed from definition)", child.Name)
		}
	}

	comp.Status.Resources = statuses
	comp.Status.ReadyResources = fmt.Sprintf("%d/%d", ready, len(children))
	comp.Status.Revision = revision.Name
	comp.Status.ObservedGeneration = comp.Generation
	setCompositeCondition(comp, dov1alpha1.ConditionSynced, metav1.ConditionTrue, "Synced", "definition rendered and children applied")
	switch {
	case replacing:
		setCompositeCondition(comp, dov1alpha1.ConditionReady, metav1.ConditionFalse, "ReplacingChildren",
			"waiting for replaced child resources to finish deleting")
	case ready == len(children):
		setCompositeCondition(comp, dov1alpha1.ConditionReady, metav1.ConditionTrue, "Available", "all child resources are ready")
	default:
		setCompositeCondition(comp, dov1alpha1.ConditionReady, metav1.ConditionFalse, "ChildrenNotReady",
			fmt.Sprintf("%d of %d child resources ready", ready, len(children)))
	}
	if err := r.persistCompositeStatus(ctx, comp); err != nil {
		return ctrl.Result{}, err
	}
	if replacing {
		// Owned-child deletion events re-trigger reconciliation; the requeue
		// is a backstop.
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	// Child status changes re-trigger via ownership; no periodic requeue needed.
	return ctrl.Result{}, nil
}

// errApplyChild wraps child apply failures so the caller can distinguish
// them (terminal ApplyFailed condition) from transient client errors.
var errApplyChild = errors.New("applying child DoResource")

// childApplyResult reports one child's reconciliation outcome.
type childApplyResult struct {
	status    dov1alpha1.CompositeResourceStatus
	replacing bool
}

// applyChild creates or updates one child DoResource. spec.type is
// immutable: when the definition changes a template's type under the same
// name, the owned child is replaced (deleted, then recreated once its
// finalizer releases the name) instead of patched in place — which would
// fail forever.
func (r *DoCompositeReconciler) applyChild(ctx context.Context, comp *dov1alpha1.DoComposite, desired *dov1alpha1.DoResource) (childApplyResult, error) {
	log := logf.FromContext(ctx)
	child := &dov1alpha1.DoResource{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}

	existing := &dov1alpha1.DoResource{}
	getErr := r.reader().Get(ctx, client.ObjectKeyFromObject(child), existing)
	if getErr != nil && !apierrors.IsNotFound(getErr) {
		return childApplyResult{}, getErr
	}
	if getErr == nil {
		// A pre-existing object we do not own must never be adopted and
		// overwritten — its cloud resource belongs to someone else.
		if !metav1.IsControlledBy(existing, comp) {
			return childApplyResult{}, fmt.Errorf("%w %q: name collides with an existing DoResource not owned by this composite",
				errApplyChild, desired.Name)
		}
		needsReplace := existing.Spec.Type != desired.Spec.Type
		if needsReplace && existing.DeletionTimestamp.IsZero() {
			log.Info("replacing child resource: immutable type changed",
				"name", desired.Name, "from", existing.Spec.Type, "to", desired.Spec.Type)
			r.Recorder.Eventf(comp, "Normal", "ReplacingChild",
				"Recreating child DoResource %q: type changed from %s to %s",
				desired.Name, existing.Spec.Type, desired.Spec.Type)
			if err := r.Delete(ctx, existing,
				client.Preconditions{UID: &existing.UID}); err != nil && !apierrors.IsNotFound(err) {
				return childApplyResult{}, err
			}
		}
		if needsReplace || !existing.DeletionTimestamp.IsZero() {
			// The old instance is on its way out; recreate on a later pass.
			return childApplyResult{
				replacing: true,
				status: dov1alpha1.CompositeResourceStatus{
					Name:         desired.Labels[labelCompositeItem],
					ResourceName: desired.Name,
					Ready:        false,
					ID:           existing.Status.ID,
				},
			}, nil
		}
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, child, func() error {
		if child.Labels == nil {
			child.Labels = map[string]string{}
		}
		for k, v := range desired.Labels {
			child.Labels[k] = v
		}
		// Merge, never replace: the child self-persists its external-name
		// annotation after create, which a wholesale overwrite would drop.
		for k, v := range desired.Annotations {
			if child.Annotations == nil {
				child.Annotations = map[string]string{}
			}
			child.Annotations[k] = v
		}
		child.Spec = desired.Spec
		return controllerutil.SetControllerReference(comp, child, r.Scheme)
	})
	if err != nil {
		return childApplyResult{}, fmt.Errorf("%w %q: %w", errApplyChild, desired.Name, err)
	}
	if op != controllerutil.OperationResultNone {
		log.Info("applied child resource", "name", desired.Name, "op", op)
	}
	return childApplyResult{
		status: dov1alpha1.CompositeResourceStatus{
			Name:         child.Labels[labelCompositeItem],
			ResourceName: child.Name,
			Ready:        meta.IsStatusConditionTrue(child.Status.Conditions, dov1alpha1.ConditionReady),
			ID:           child.Status.ID,
		},
	}, nil
}

// markCompositeFailed records a terminal-until-change failure. Ready is
// cleared too: a composite whose current generation cannot render or apply
// must not keep advertising availability from an earlier generation.
func (r *DoCompositeReconciler) markCompositeFailed(ctx context.Context, comp *dov1alpha1.DoComposite, reason string, err error) (ctrl.Result, error) {
	logf.FromContext(ctx).Error(err, "composite reconcile failed", "reason", reason)
	r.Recorder.Eventf(comp, "Warning", reason, "%s", compact(err.Error()))
	setCompositeCondition(comp, dov1alpha1.ConditionSynced, metav1.ConditionFalse, reason, compact(err.Error()))
	setCompositeCondition(comp, dov1alpha1.ConditionReady, metav1.ConditionFalse, reason,
		"current generation failed to render or apply")
	if persistErr := r.persistCompositeStatus(ctx, comp); persistErr != nil {
		return ctrl.Result{}, persistErr
	}
	// DefinitionNotFound heals when the definition appears (watched); the
	// requeue is a backstop.
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func (r *DoCompositeReconciler) persistCompositeStatus(ctx context.Context, comp *dov1alpha1.DoComposite) error {
	// Detached from reconcile cancellation so shutdown does not lose the
	// roll-up of already-performed work.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	status := comp.Status
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dov1alpha1.DoComposite{}
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(comp), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		latest.Status = status
		return client.IgnoreNotFound(r.Status().Update(ctx, latest))
	})
}

// updateDefinitionUsage records how many composites currently use the
// definition (informational; best effort).
func (r *DoCompositeReconciler) updateDefinitionUsage(ctx context.Context, def *dov1alpha1.DoCompositeDefinition) {
	var comps dov1alpha1.DoCompositeList
	if err := r.List(ctx, &comps, client.MatchingFields{compositeDefIndexKey: def.Name}); err != nil {
		return
	}
	count := int32(len(comps.Items)) // #nosec G115 -- list sizes are far below int32 range
	if def.Status.Composites == count {
		return
	}
	def.Status.Composites = count
	_ = r.Status().Update(ctx, def)
}

func (r *DoCompositeReconciler) reader() client.Reader {
	if r.Live != nil {
		return r.Live
	}
	return r.Client
}

func setCompositeCondition(comp *dov1alpha1.DoComposite, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&comp.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: comp.Generation,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *DoCompositeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(), &dov1alpha1.DoComposite{}, compositeDefIndexKey,
		func(o client.Object) []string {
			comp, ok := o.(*dov1alpha1.DoComposite)
			if !ok || comp.Spec.Definition == "" {
				return nil
			}
			return []string{comp.Spec.Definition}
		}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&dov1alpha1.DoComposite{}).
		Owns(&dov1alpha1.DoResource{}).
		Watches(&dov1alpha1.DoCompositeDefinition{}, handler.EnqueueRequestsFromMapFunc(r.mapDefinition)).
		Named("docomposite").
		Complete(r)
}

// mapDefinition re-renders all composites using a changed definition.
func (r *DoCompositeReconciler) mapDefinition(ctx context.Context, obj client.Object) []reconcile.Request {
	var comps dov1alpha1.DoCompositeList
	if err := r.List(ctx, &comps, client.MatchingFields{compositeDefIndexKey: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(comps.Items))
	for i := range comps.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&comps.Items[i])})
	}
	return reqs
}
