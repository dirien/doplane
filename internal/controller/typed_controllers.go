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

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;create;update
// +kubebuilder:rbac:groups=typed.do.pulumi.com,resources=*,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=typed.do.pulumi.com,resources=*/status,verbs=get;update;patch

// TypedRegistrar applies generated CRDs and starts one dynamic controller
// per typed kind at runtime (the manager accepts runnables after Start).
// Registrations are idempotent per owner — re-registering the same source
// re-applies the CRD; a *different* source claiming an already-served
// plural is rejected, so colliding kinds fail fast instead of silently
// reconciling against the wrong backing resource.
type TypedRegistrar struct {
	Manager ctrl.Manager

	mu sync.Mutex
	// owners maps a served plural to the identity that registered it
	// ("resource:<token>" / "composite:<definition>"); started marks
	// plurals whose controller is already running.
	owners  map[string]string
	started map[string]bool
}

// claim records ownership of a plural, rejecting a second owner.
func (reg *TypedRegistrar) claim(plural, owner string) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if existing, ok := reg.owners[plural]; ok && existing != owner {
		return fmt.Errorf("typed API %q is already served for %s; rename the kind (or set spec.api.plural) to avoid the collision",
			plural+"."+typedGroup, existing)
	}
	if reg.owners == nil {
		reg.owners = map[string]string{}
	}
	reg.owners[plural] = owner
	return nil
}

// applyCRD creates or updates a generated CRD in place.
func (reg *TypedRegistrar) applyCRD(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) error {
	c := reg.Manager.GetClient()
	if err := c.Create(ctx, crd); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating CRD %s: %w", crd.Name, err)
		}
		existing := &apiextensionsv1.CustomResourceDefinition{}
		if err := reg.Manager.GetAPIReader().Get(ctx, types.NamespacedName{Name: crd.Name}, existing); err != nil {
			return err
		}
		existing.Spec = crd.Spec
		if err := c.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating CRD %s: %w", crd.Name, err)
		}
	}
	return nil
}

// EnsureResourceAPI applies the typed CRD for a provider token and starts
// its translation controller.
func (reg *TypedRegistrar) EnsureResourceAPI(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition,
	token, providerName string,
) error {
	if err := reg.claim(crd.Spec.Names.Plural, "resource:"+token); err != nil {
		return err
	}
	if err := reg.applyCRD(ctx, crd); err != nil {
		return err
	}
	gvk := schema.GroupVersionKind{Group: typedGroup, Version: typedVersion, Kind: crd.Spec.Names.Kind}
	rec := &TypedResourceReconciler{
		Client:       reg.Manager.GetClient(),
		Scheme:       reg.Manager.GetScheme(),
		GVK:          gvk,
		Token:        token,
		ProviderName: providerName,
	}
	return reg.startController("typed-"+crd.Spec.Names.Plural, gvk, rec, &dov1alpha1.DoResource{})
}

// EnsureCompositeAPI applies the typed CRD for a definition's platform API
// and starts its translation controller.
func (reg *TypedRegistrar) EnsureCompositeAPI(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition,
	definition string,
) error {
	if err := reg.claim(crd.Spec.Names.Plural, "composite:"+definition); err != nil {
		return err
	}
	if err := reg.applyCRD(ctx, crd); err != nil {
		return err
	}
	gvk := schema.GroupVersionKind{Group: typedGroup, Version: typedVersion, Kind: crd.Spec.Names.Kind}
	rec := &TypedCompositeReconciler{
		Client:     reg.Manager.GetClient(),
		Scheme:     reg.Manager.GetScheme(),
		GVK:        gvk,
		Definition: definition,
	}
	return reg.startController("typed-"+crd.Spec.Names.Plural, gvk, rec, &dov1alpha1.DoComposite{})
}

// startController wires a dynamic controller for gvk: reconcile on typed
// object events plus on changes of the owned mirror object.
func (reg *TypedRegistrar) startController(name string, gvk schema.GroupVersionKind,
	rec reconcile.Reconciler, owned client.Object,
) error {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.started[name] {
		return nil
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
	if reg.started == nil {
		reg.started = map[string]bool{}
	}
	reg.started[name] = true
	return nil
}

// TypedResourceReconciler translates one generated managed-resource kind
// into DoResources: spec.forProvider becomes properties, the typed object
// owns the mirror (GC on delete; the mirror's finalizer tears down the
// cloud resource), and the mirror's status is copied back.
type TypedResourceReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	GVK          schema.GroupVersionKind
	Token        string
	ProviderName string
}

// Reconcile mirrors one typed object.
func (r *TypedResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
// (e.g. StaticSite) into DoComposites: the typed object's spec becomes the
// composite's parameters and the roll-up status is copied back.
type TypedCompositeReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	GVK        schema.GroupVersionKind
	Definition string
}

// Reconcile mirrors one typed composite object.
func (r *TypedCompositeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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
