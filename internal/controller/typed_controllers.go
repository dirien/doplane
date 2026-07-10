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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;create;update;delete
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions/status,verbs=update
// +kubebuilder:rbac:groups=typed.do.pulumi.com,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=typed.do.pulumi.com,resources=*/status,verbs=get;update;patch

// errCRDConflict marks a registration colliding with a CRD another source
// (or another operator) owns — terminal until a spec changes.
var errCRDConflict = errors.New("typed API conflict")

// errStoredVersionInUse marks a version drop while objects of that version
// still exist — it heals as objects migrate, so callers requeue.
var errStoredVersionInUse = errors.New("stored version still in use")

// typedRegistration is the live controller serving one generated CRD.
type typedRegistration struct {
	owner string
	gvk   schema.GroupVersionKind
	rec   reconcile.Reconciler
	gen   int
}

// TypedRegistrar applies generated CRDs and starts one dynamic controller
// per typed kind at runtime (the manager accepts runnables after Start).
// Ownership is persisted on the CRD itself (managed-by label + owner
// annotation) and checked before any apply, so a *different* source
// claiming an already-served CRD is rejected deterministically — including
// after a manager restart — instead of silently reconciling against the
// wrong backing resource.
type TypedRegistrar struct {
	Manager ctrl.Manager

	mu sync.Mutex
	// registrations maps a CRD name to its serving controller. A superseded
	// registration (version bump, definition recreated) keeps its goroutines
	// but is fenced off via isCurrent and loses its typed-kind informer.
	registrations map[string]*typedRegistration
	// gens counts registrations per CRD name so replacement controllers get
	// unique names.
	gens map[string]int
}

// isCurrent reports whether rec is still the serving reconciler for the
// CRD. Superseded controllers cannot be stopped, so their reconcilers fence
// themselves out with this check instead of fighting the replacement.
func (reg *TypedRegistrar) isCurrent(crdName string, rec reconcile.Reconciler) bool {
	if reg == nil {
		return true
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	entry, ok := reg.registrations[crdName]
	return ok && entry.rec == rec
}

// applyCRD creates or updates a generated CRD after verifying ownership:
// an existing CRD must carry doplane's managed-by label and either no owner
// annotation (pre-annotation releases are adopted) or the same owner.
func (reg *TypedRegistrar) applyCRD(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) error {
	owner := crd.Annotations[annTypedOwner]
	c := reg.Manager.GetClient()
	if err := c.Create(ctx, crd); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating CRD %s: %w", crd.Name, err)
		}
		existing := &apiextensionsv1.CustomResourceDefinition{}
		if err := reg.Manager.GetAPIReader().Get(ctx, types.NamespacedName{Name: crd.Name}, existing); err != nil {
			return err
		}
		if err := checkCRDOwnership(existing, owner); err != nil {
			return err
		}
		existing.Spec = crd.Spec
		if existing.Annotations == nil {
			existing.Annotations = map[string]string{}
		}
		existing.Annotations[annTypedOwner] = owner
		if err := c.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating CRD %s: %w", crd.Name, err)
		}
	}
	return nil
}

// checkCRDOwnership rejects applying over a CRD doplane does not own.
func checkCRDOwnership(existing *apiextensionsv1.CustomResourceDefinition, owner string) error {
	if existing.Labels[labelManagedByKey] != "doplane" {
		return fmt.Errorf("%w: CRD %s exists but is not managed by doplane; refusing to overwrite it", errCRDConflict, existing.Name)
	}
	if got := existing.Annotations[annTypedOwner]; got != "" && got != owner {
		return fmt.Errorf("%w: typed API %s is already served for %s; rename the kind (or set spec.api.plural/group) to avoid the collision",
			errCRDConflict, existing.Name, got)
	}
	return nil
}

// prepareVersionDrop enforces status.storedVersions hygiene before a CRD
// update that removes versions: with objects still stored the drop is
// refused (errStoredVersionInUse); at zero objects the stale storedVersions
// entries are pruned so the apiserver accepts the spec update. Safe because
// generated CRDs use conversion None with round-trippable schemas — there
// is nothing to migrate, only bookkeeping to clean.
func (reg *TypedRegistrar) prepareVersionDrop(ctx context.Context, desired *apiextensionsv1.CustomResourceDefinition) error {
	existing := &apiextensionsv1.CustomResourceDefinition{}
	if err := reg.Manager.GetAPIReader().Get(ctx, types.NamespacedName{Name: desired.Name}, existing); err != nil {
		return client.IgnoreNotFound(err)
	}
	kept := map[string]bool{}
	for _, v := range desired.Spec.Versions {
		kept[v.Name] = true
	}
	var dropped []string
	remaining := existing.Status.StoredVersions[:0:0]
	for _, v := range existing.Status.StoredVersions {
		if kept[v] {
			remaining = append(remaining, v)
		} else {
			dropped = append(dropped, v)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(storageGVK(existing).GroupVersion().WithKind(existing.Spec.Names.ListKind))
	if err := reg.Manager.GetAPIReader().List(ctx, list); err != nil {
		return fmt.Errorf("listing %s objects before dropping versions %v: %w", existing.Spec.Names.Kind, dropped, err)
	}
	if n := len(list.Items); n > 0 {
		return fmt.Errorf("%w: cannot drop version(s) %v of %s while %d objects exist; keep them in deprecatedVersions until the status reports zero",
			errStoredVersionInUse, dropped, existing.Name, n)
	}
	existing.Status.StoredVersions = remaining
	if err := reg.Manager.GetClient().Status().Update(ctx, existing); err != nil {
		return fmt.Errorf("pruning storedVersions of %s: %w", existing.Name, err)
	}
	return nil
}

// storageGVK returns the GVK of a CRD's storage version.
func storageGVK(crd *apiextensionsv1.CustomResourceDefinition) schema.GroupVersionKind {
	version := crd.Spec.Versions[0].Name
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			version = v.Name
		}
	}
	return schema.GroupVersionKind{Group: crd.Spec.Group, Version: version, Kind: crd.Spec.Names.Kind}
}

// EnsureResourceAPI applies the typed CRD for a provider token and starts
// its translation controller.
func (reg *TypedRegistrar) EnsureResourceAPI(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition,
	token, providerName string,
) error {
	if err := reg.applyCRD(ctx, crd); err != nil {
		return err
	}
	gvk := storageGVK(crd)
	rec := &TypedResourceReconciler{
		Client:       reg.Manager.GetClient(),
		Scheme:       reg.Manager.GetScheme(),
		Registrar:    reg,
		CRDName:      crd.Name,
		GVK:          gvk,
		Token:        token,
		ProviderName: providerName,
	}
	return reg.startController(crd.Name, crd.Annotations[annTypedOwner], gvk, rec, &dov1alpha1.DoResource{})
}

// EnsureCompositeAPI applies the typed CRD for a definition's platform API
// and starts its translation controller.
func (reg *TypedRegistrar) EnsureCompositeAPI(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition,
	definition string,
) error {
	if err := reg.prepareVersionDrop(ctx, crd); err != nil {
		return err
	}
	if err := reg.applyCRD(ctx, crd); err != nil {
		return err
	}
	gvk := storageGVK(crd)
	rec := &TypedCompositeReconciler{
		Client:     reg.Manager.GetClient(),
		Scheme:     reg.Manager.GetScheme(),
		Registrar:  reg,
		CRDName:    crd.Name,
		GVK:        gvk,
		Definition: definition,
	}
	return reg.startController(crd.Name, crd.Annotations[annTypedOwner], gvk, rec, &dov1alpha1.DoComposite{})
}

// Forget drops the registration of a deleted CRD: the typed-kind informer
// is removed (its relist would error forever against a deleted API) and the
// entry is cleared so a future definition can re-serve the kinds. The old
// controller keeps running fenced-off — controllers cannot be stopped
// individually, and one idle goroutine set per teardown is a bounded leak.
func (reg *TypedRegistrar) Forget(ctx context.Context, crdName string) {
	reg.mu.Lock()
	entry, ok := reg.registrations[crdName]
	if ok {
		delete(reg.registrations, crdName)
	}
	reg.mu.Unlock()
	if ok {
		reg.removeInformer(ctx, entry.gvk)
	}
}

func (reg *TypedRegistrar) removeInformer(ctx context.Context, gvk schema.GroupVersionKind) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	_ = reg.Manager.GetCache().RemoveInformer(ctx, obj)
}

// startController wires a dynamic controller for gvk: reconcile on typed
// object events plus on changes of the owned mirror object. A registration
// whose owner or GVK changed (definition recreated, version bumped) is
// superseded: the old typed-kind informer is removed and a replacement
// controller takes over; the old reconciler fences itself via isCurrent.
func (reg *TypedRegistrar) startController(crdName, owner string, gvk schema.GroupVersionKind,
	rec reconcile.Reconciler, owned client.Object,
) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if existing, ok := reg.registrations[crdName]; ok {
		if existing.owner == owner && existing.gvk == gvk {
			return nil
		}
		reg.removeInformer(context.Background(), existing.gvk)
	}
	if reg.gens == nil {
		reg.gens = map[string]int{}
	}
	reg.gens[crdName]++
	name := "typed-" + crdName
	if gen := reg.gens[crdName]; gen > 1 {
		name = fmt.Sprintf("%s-r%d", name, gen)
	}
	c, err := controller.New(name, reg.Manager, controller.Options{Reconciler: rec})
	if err != nil {
		return err
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Watch(source.Kind(reg.Manager.GetCache(), client.Object(obj), &handler.EnqueueRequestForObject{})); err != nil {
		return err
	}
	if err := c.Watch(source.Kind(reg.Manager.GetCache(), owned,
		handler.EnqueueRequestForOwner(reg.Manager.GetScheme(), reg.Manager.GetRESTMapper(), obj, handler.OnlyControllerOwner()))); err != nil {
		return err
	}
	if reg.registrations == nil {
		reg.registrations = map[string]*typedRegistration{}
	}
	reg.registrations[crdName] = &typedRegistration{owner: owner, gvk: gvk, rec: rec, gen: reg.gens[crdName]}
	return nil
}

// TypedResourceReconciler translates one generated managed-resource kind
// into DoResources: spec.forProvider becomes properties, the typed object
// owns the mirror (GC on delete; the mirror's finalizer tears down the
// cloud resource), and the mirror's status is copied back.
type TypedResourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Registrar fences superseded controllers (nil in tests: always current).
	Registrar    *TypedRegistrar
	CRDName      string
	GVK          schema.GroupVersionKind
	Token        string
	ProviderName string
}

// Reconcile mirrors one typed object.
func (r *TypedResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !r.Registrar.isCurrent(r.CRDName, r) {
		return ctrl.Result{}, nil
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.GVK)
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		// Garbage collection deletes the owned DoResource, whose finalizer
		// performs the external teardown.
		return ctrl.Result{}, nil
	}

	spec, err := r.mirrorSpec(obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	mirror := &dov1alpha1.DoResource{ObjectMeta: metav1.ObjectMeta{Namespace: obj.GetNamespace(), Name: obj.GetName()}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, mirror, func() error {
		if err := ensureControlled(obj, mirror, "DoResource"); err != nil {
			return err
		}
		if mirror.Spec.Type == "" {
			mirror.Spec.Type = r.Token
		}
		mirror.Spec.Package = ""
		mirror.Spec.ProviderRef = spec.ProviderRef
		mirror.Spec.DeletionPolicy = spec.DeletionPolicy
		mirror.Spec.Properties = spec.Properties
		return controllerutil.SetControllerReference(obj, mirror, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, mirrorStatusBack(ctx, r.Client, obj, map[string]any{
		"id":         mirror.Status.ID,
		"conditions": mirror.Status.Conditions,
		"outputs":    mirror.Status.Outputs,
	})
}

// mirrorSpec extracts the DoResource spec fields from the typed object.
func (r *TypedResourceReconciler) mirrorSpec(obj *unstructured.Unstructured) (*dov1alpha1.DoResourceSpec, error) {
	forProvider, _, err := unstructured.NestedMap(obj.Object, "spec", "forProvider")
	if err != nil {
		return nil, fmt.Errorf("spec.forProvider: %w", err)
	}
	raw, err := json.Marshal(forProvider)
	if err != nil {
		return nil, err
	}
	spec := &dov1alpha1.DoResourceSpec{Properties: &apiextensionsv1.JSON{Raw: raw}}

	if name, _, _ := unstructured.NestedString(obj.Object, "spec", "providerRef", "name"); name != "" {
		kind, _, _ := unstructured.NestedString(obj.Object, "spec", "providerRef", "kind")
		spec.ProviderRef = &dov1alpha1.ProviderReference{Name: name, Kind: kind}
	} else if r.ProviderName != "" {
		spec.ProviderRef = &dov1alpha1.ProviderReference{Name: r.ProviderName}
	}
	if policy, _, _ := unstructured.NestedString(obj.Object, "spec", "deletionPolicy"); policy != "" {
		spec.DeletionPolicy = dov1alpha1.DeletionPolicy(policy)
	}
	return spec, nil
}

// ensureControlled rejects an existing mirror the typed object does not
// control. Typed object names are unique per kind only, while all mirrors
// of a namespace share one name space per mirror kind — so two typed kinds
// (or a hand-created mirror) can collide on a name, and adopting the
// object would overwrite state the typed object does not own.
func ensureControlled(obj *unstructured.Unstructured, mirror client.Object, mirrorKind string) error {
	if mirror.GetResourceVersion() == "" || metav1.IsControlledBy(mirror, obj) {
		return nil
	}
	return fmt.Errorf("%s %s/%s already exists and is not controlled by %s %q; rename the %s object or remove the conflicting %s",
		mirrorKind, mirror.GetNamespace(), mirror.GetName(), obj.GetKind(), obj.GetName(), obj.GetKind(), mirrorKind)
}

// mirrorStatusBack writes the mirror's observed state onto the typed
// object's status subresource. An unchanged status is not written: the
// typed kind is watched without predicates, so a no-op update would
// enqueue the object again and reconcile-write forever.
func mirrorStatusBack(ctx context.Context, c client.Client, obj *unstructured.Unstructured, status map[string]any) error {
	desired, err := toUnstructuredValue(status)
	if err != nil {
		return err
	}
	stripNilValues(desired)
	current, _, _ := unstructured.NestedMap(obj.Object, "status")
	stripNilValues(current)
	if sameJSON(current, desired) {
		return nil
	}
	obj.Object["status"] = desired
	return client.IgnoreNotFound(c.Status().Update(ctx, obj))
}

// stripNilValues drops top-level null entries so the comparison in
// mirrorStatusBack stays stable regardless of whether the API server
// preserves or prunes them.
func stripNilValues(m map[string]any) {
	for k, v := range m {
		if v == nil {
			delete(m, k)
		}
	}
}

// sameJSON compares two unstructured values by their canonical JSON
// encoding, tolerating the int64-vs-float64 skew between API-server
// decoding and local marshalling.
func sameJSON(a, b any) bool {
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(aj, bj)
}

// TypedCompositeReconciler translates one generated platform-API kind
// (e.g. Website) into DoComposites: the typed object's spec becomes the
// composite's parameters — except the reserved spec.doplane block, which
// maps to the composite's lifecycle knobs — and the roll-up status is
// copied back.
type TypedCompositeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Registrar fences superseded controllers (nil in tests: always current).
	Registrar  *TypedRegistrar
	CRDName    string
	GVK        schema.GroupVersionKind
	Definition string
}

// Reconcile mirrors one typed composite object.
func (r *TypedCompositeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !r.Registrar.isCurrent(r.CRDName, r) {
		return ctrl.Result{}, nil
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.GVK)
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}

	params, _, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("spec: %w", err)
	}
	updatePolicy, revisionRef := popDoplaneBlock(params)
	raw, err := json.Marshal(params)
	if err != nil {
		return ctrl.Result{}, err
	}
	mirror := &dov1alpha1.DoComposite{ObjectMeta: metav1.ObjectMeta{Namespace: obj.GetNamespace(), Name: obj.GetName()}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, mirror, func() error {
		if err := ensureControlled(obj, mirror, "DoComposite"); err != nil {
			return err
		}
		mirror.Spec.Definition = r.Definition
		mirror.Spec.Parameters = &apiextensionsv1.JSON{Raw: raw}
		mirror.Spec.UpdatePolicy = updatePolicy
		mirror.Spec.RevisionRef = revisionRef
		return controllerutil.SetControllerReference(obj, mirror, r.Scheme)
	}); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, mirrorStatusBack(ctx, r.Client, obj, map[string]any{
		"readyResources": mirror.Status.ReadyResources,
		"resources":      mirror.Status.Resources,
		"revision":       mirror.Status.Revision,
		"conditions":     mirror.Status.Conditions,
	})
}

// popDoplaneBlock removes the reserved doplane block from a typed spec and
// returns the lifecycle knobs it carries. Unknown or mistyped entries are
// ignored — the generated schema already validated the block at admission.
func popDoplaneBlock(params map[string]any) (dov1alpha1.UpdatePolicy, *dov1alpha1.RevisionReference) {
	block, _ := params[doplaneReservedProperty].(map[string]any)
	delete(params, doplaneReservedProperty)
	var policy dov1alpha1.UpdatePolicy
	if s, _ := block["updatePolicy"].(string); s != "" {
		policy = dov1alpha1.UpdatePolicy(s)
	}
	var revisionRef *dov1alpha1.RevisionReference
	if ref, _ := block["revisionRef"].(map[string]any); ref != nil {
		if name, _ := ref["name"].(string); name != "" {
			revisionRef = &dov1alpha1.RevisionReference{Name: name}
		}
	}
	return policy, revisionRef
}

// toUnstructuredValue converts typed values (conditions, JSON blobs) into
// plain map/slice/scalar form acceptable to unstructured objects.
func toUnstructuredValue(v map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
