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
	"regexp"
	"slices"
	"strings"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

// +kubebuilder:rbac:groups=do.pulumi.com,resources=docompositedefinitions,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=do.pulumi.com,resources=docompositedefinitions/finalizers,verbs=update

// condAPIServed reports whether a definition's typed platform API is being
// served, and exactly why not. Reasons are machine API.
const condAPIServed = "APIServed"

// typedAPIFinalizer blocks definition deletion while typed objects of its
// API still exist: delete-and-recreate is the only rename path, so a
// definition must never vanish under live platform objects. At zero
// objects the generated CRD is deleted and the finalizer released.
const typedAPIFinalizer = "do.pulumi.com/typed-api"

// DoCompositeDefinitionReconciler serves definitions that declare a typed
// platform API (spec.api): it generates the CRD in the definition's chosen
// (allowlisted) group and starts the controller translating typed objects
// (e.g. `kind: Website`) into DoComposites.
type DoCompositeDefinitionReconciler struct {
	client.Client
	// Live bypasses the informer cache: typed objects are listed for
	// status counts without spinning up informers per kind.
	Live     client.Reader
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Typed    *TypedRegistrar
	// AllowedGroups are the platform API groups definitions may serve, on
	// top of the always-allowed typed.do.pulumi.com. Install-time only —
	// each group needs matching manager RBAC, rendered from the same Helm
	// value.
	AllowedGroups []string
}

// countRefreshInterval keeps status.apiVersions object counts (the
// migration signal for version bumps) reasonably fresh without watching
// every generated kind from this reconciler.
const countRefreshInterval = time.Minute

// Reconcile ensures the definition's typed API exists and is served, and
// rolls the outcome up into the APIServed condition.
func (r *DoCompositeDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	def := &dov1alpha1.DoCompositeDefinition{}
	if err := r.Get(ctx, req.NamespacedName, def); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !def.DeletionTimestamp.IsZero() {
		return r.finalizeDefinition(ctx, def)
	}
	if def.Spec.API == nil || r.Typed == nil {
		return ctrl.Result{}, nil
	}
	api := def.Spec.API

	group := compositeAPIGroup(api)
	if group != typedGroup && !slices.Contains(r.AllowedGroups, group) {
		return r.markAPIFailed(ctx, def, "GroupNotAllowed",
			fmt.Errorf("group %q is not on the install-time allowlist; add it to the compositeApiGroups Helm value (which also renders the manager RBAC for it)", group))
	}
	crd, err := typedCompositeCRD(def.Name, api)
	if err != nil {
		return r.markAPIFailed(ctx, def, "InvalidSchema", err)
	}
	if err := checkTemplateParams(&def.Spec); err != nil {
		return r.markAPIFailed(ctx, def, "InvalidSchema", err)
	}

	if !controllerutil.ContainsFinalizer(def, typedAPIFinalizer) {
		controllerutil.AddFinalizer(def, typedAPIFinalizer)
		if err := r.Update(ctx, def); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.Typed.EnsureCompositeAPI(ctx, crd, def.Name); err != nil {
		switch {
		case errors.Is(err, errCRDConflict):
			return r.markAPIFailed(ctx, def, "CRDConflict", err)
		case errors.Is(err, errStoredVersionInUse):
			// Heals as objects migrate off the dropped version.
			res, ferr := r.markAPIFailed(ctx, def, "StoredVersionInUse", err)
			if ferr == nil {
				res = ctrl.Result{RequeueAfter: countRefreshInterval}
			}
			return res, ferr
		case apierrors.IsInvalid(err):
			// The apiserver is the authority on CRD schema validity; its
			// rejection is terminal until the definition changes.
			return r.markAPIFailed(ctx, def, "InvalidSchema", err)
		default:
			r.Recorder.Eventf(def, "Warning", "TypedAPIFailed", "%s", compact(err.Error()))
			return ctrl.Result{}, err
		}
	}

	def.Status.APIVersions = r.countTypedObjects(ctx, api)
	setDefinitionCondition(def, metav1.ConditionTrue, "Served",
		fmt.Sprintf("typed API %s served", crd.Name))
	if err := r.persistDefinitionStatus(ctx, def); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: countRefreshInterval}, nil
}

// finalizeDefinition blocks deletion while typed objects exist and, once
// the API is unused, deletes the generated CRD and releases the finalizer.
// This is the fail-safe teardown prescribed in typed_crd.go: never an
// owner-ref cascade that could mass-delete live infrastructure.
func (r *DoCompositeDefinitionReconciler) finalizeDefinition(ctx context.Context, def *dov1alpha1.DoCompositeDefinition) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(def, typedAPIFinalizer) {
		return ctrl.Result{}, nil
	}
	api := def.Spec.API
	if api != nil {
		if n := r.totalTypedObjects(ctx, api); n > 0 {
			msg := fmt.Sprintf("deletion blocked: %d %s object(s) still exist; delete them first", n, api.Kind)
			r.Recorder.Eventf(def, "Warning", "DeletionBlocked", "%s", msg)
			setDefinitionCondition(def, metav1.ConditionFalse, "DeletionBlocked", msg)
			if err := r.persistDefinitionStatus(ctx, def); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if err := r.deleteServedCRD(ctx, def); err != nil {
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(def, typedAPIFinalizer)
	return ctrl.Result{}, r.Update(ctx, def)
}

// deleteServedCRD removes the definition's generated CRD — only when it is
// doplane-managed and still owned by this definition.
func (r *DoCompositeDefinitionReconciler) deleteServedCRD(ctx context.Context, def *dov1alpha1.DoCompositeDefinition) error {
	api := def.Spec.API
	name := compositeAPIPlural(api) + "." + compositeAPIGroup(api)
	crd := &apiextensionsv1.CustomResourceDefinition{}
	if err := r.Live.Get(ctx, types.NamespacedName{Name: name}, crd); err != nil {
		return client.IgnoreNotFound(err)
	}
	if crd.Labels[labelManagedByKey] != "doplane" || crd.Annotations[annTypedOwner] != "composite:"+def.Name {
		return nil // not ours (anymore); leave it alone
	}
	if err := r.Delete(ctx, crd, client.Preconditions{UID: &crd.UID}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if r.Typed != nil {
		r.Typed.Forget(ctx, name)
	}
	r.Recorder.Eventf(def, "Normal", "TypedAPIRemoved", "deleted generated CRD %s", name)
	return nil
}

// typedObjectList lists all objects of the definition's API (any served
// version sees the full set — conversion is None).
func (r *DoCompositeDefinitionReconciler) typedObjectList(ctx context.Context, api *dov1alpha1.CompositeAPI) (*unstructured.UnstructuredList, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   compositeAPIGroup(api),
		Version: compositeAPIVersion(api),
		Kind:    api.Kind + "List",
	})
	err := r.Live.List(ctx, list)
	return list, err
}

func (r *DoCompositeDefinitionReconciler) totalTypedObjects(ctx context.Context, api *dov1alpha1.CompositeAPI) int {
	list, err := r.typedObjectList(ctx, api)
	if err != nil {
		// Fail safe: an unreadable API blocks deletion rather than
		// releasing the finalizer over possibly-live objects.
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return 0
		}
		return 1
	}
	return len(list.Items)
}

// countTypedObjects reports, per served version, how many objects' manifests
// last wrote that version — the signal for dropping a deprecated version.
// Attribution comes from managedFields (the only per-object record of the
// API version a client used under conversion None); status writes are
// excluded, unattributable objects count against the storage version.
func (r *DoCompositeDefinitionReconciler) countTypedObjects(ctx context.Context, api *dov1alpha1.CompositeAPI) []dov1alpha1.TypedAPIVersionStatus {
	storage := compositeAPIVersion(api)
	counts := map[string]int32{storage: 0}
	for _, v := range api.DeprecatedVersions {
		counts[v] = 0
	}
	list, err := r.typedObjectList(ctx, api)
	if err == nil {
		group := compositeAPIGroup(api)
		for i := range list.Items {
			counts[writtenVersion(&list.Items[i], group, storage)]++
		}
	}
	out := []dov1alpha1.TypedAPIVersionStatus{{Name: storage, Objects: counts[storage]}}
	delete(counts, storage)
	versions := make([]string, 0, len(counts))
	for v := range counts {
		versions = append(versions, v)
	}
	slices.Sort(versions)
	for _, v := range versions {
		out = append(out, dov1alpha1.TypedAPIVersionStatus{
			Name: v, Deprecated: slices.Contains(api.DeprecatedVersions, v), Objects: counts[v],
		})
	}
	return out
}

// writtenVersion extracts the API version of an object's newest non-status
// field manager, falling back to the storage version.
func writtenVersion(obj *unstructured.Unstructured, group, fallback string) string {
	version := fallback
	var newest *metav1.Time
	for _, mf := range obj.GetManagedFields() {
		if mf.Subresource != "" || !strings.HasPrefix(mf.APIVersion, group+"/") {
			continue
		}
		if mf.Time == nil || (newest != nil && !mf.Time.After(newest.Time)) {
			continue
		}
		newest = mf.Time
		version = strings.TrimPrefix(mf.APIVersion, group+"/")
	}
	return version
}

// paramsRootRe extracts the root parameter of dotted ${params.*}
// expressions. Bracket-quoted roots (rare) are deliberately not matched —
// the check must never reject a valid definition.
var paramsRootRe = regexp.MustCompile(`\$\{\s*params\.([A-Za-z0-9_-]+)`)

// checkTemplateParams cross-checks the templates' ${params.*} usage against
// the parameters schema: a parameter the schema would prune can never be
// supplied, so referencing it is a definition bug caught at apply time.
func checkTemplateParams(spec *dov1alpha1.DoCompositeDefinitionSpec) error {
	params := spec.API.ParametersSchema
	if params == nil || len(params.Properties) == 0 ||
		(params.XPreserveUnknownFields != nil && *params.XPreserveUnknownFields) ||
		(params.AdditionalProperties != nil && (params.AdditionalProperties.Allows || params.AdditionalProperties.Schema != nil)) {
		return nil
	}
	for i := range spec.Resources {
		tpl := &spec.Resources[i]
		var raw []byte
		if tpl.Properties != nil {
			raw = tpl.Properties.Raw
		}
		text := strings.ReplaceAll(string(raw)+" "+tpl.ExternalName, "$${", "")
		for _, m := range paramsRootRe.FindAllStringSubmatch(text, -1) {
			if _, ok := params.Properties[m[1]]; !ok {
				return fmt.Errorf("resource %q references ${params.%s}, but the parameters schema declares no property %q (it would be pruned at admission)",
					tpl.Name, m[1], m[1])
			}
		}
	}
	return nil
}

// markAPIFailed records a terminal-until-change serving failure.
func (r *DoCompositeDefinitionReconciler) markAPIFailed(ctx context.Context, def *dov1alpha1.DoCompositeDefinition, reason string, err error) (ctrl.Result, error) {
	r.Recorder.Eventf(def, "Warning", "TypedAPIFailed", "%s", compact(err.Error()))
	setDefinitionCondition(def, metav1.ConditionFalse, reason, compact(err.Error()))
	return ctrl.Result{}, r.persistDefinitionStatus(ctx, def)
}

func setDefinitionCondition(def *dov1alpha1.DoCompositeDefinition, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&def.Status.Conditions, metav1.Condition{
		Type:               condAPIServed,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: def.Generation,
	})
}

// persistDefinitionStatus writes conditions and counts with conflict retry:
// DoCompositeReconciler concurrently updates status.composites.
func (r *DoCompositeDefinitionReconciler) persistDefinitionStatus(ctx context.Context, def *dov1alpha1.DoCompositeDefinition) error {
	conditions := def.Status.Conditions
	apiVersions := def.Status.APIVersions
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dov1alpha1.DoCompositeDefinition{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(def), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		latest.Status.Conditions = conditions
		latest.Status.APIVersions = apiVersions
		return client.IgnoreNotFound(r.Status().Update(ctx, latest))
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *DoCompositeDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dov1alpha1.DoCompositeDefinition{}).
		Named("docompositedefinition").
		Complete(r)
}
