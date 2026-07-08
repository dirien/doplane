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

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

// DoCompositeDefinitionReconciler serves definitions that declare a typed
// platform API (spec.api): it generates the CRD and starts the controller
// translating typed objects (e.g. `kind: StaticSite`) into DoComposites.
type DoCompositeDefinitionReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Typed    *TypedRegistrar
}

// Reconcile ensures the definition's typed API exists and is served.
func (r *DoCompositeDefinitionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	def := &dov1alpha1.DoCompositeDefinition{}
	if err := r.Get(ctx, req.NamespacedName, def); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if def.Spec.API == nil || r.Typed == nil || !def.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	crd, err := typedCompositeCRD(def.Name, def.Spec.API)
	if err != nil {
		r.Recorder.Eventf(def, "Warning", "TypedAPIFailed", "%s", compact(err.Error()))
		return ctrl.Result{}, nil // terminal until the spec changes
	}
	if err := r.Typed.EnsureCompositeAPI(ctx, crd, def.Name); err != nil {
		r.Recorder.Eventf(def, "Warning", "TypedAPIFailed", "%s", compact(err.Error()))
		return ctrl.Result{}, err
	}
	r.Recorder.Eventf(def, "Normal", "TypedAPIServed", "typed API %s.%s served", crd.Spec.Names.Plural, typedGroup)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DoCompositeDefinitionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dov1alpha1.DoCompositeDefinition{}).
		Named("docompositedefinition").
		Complete(r)
}
