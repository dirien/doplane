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
	"time"

	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

// DoProviderConfigReconciler validates namespaced, tenant-owned provider
// profiles. The checks are shared with the cluster-scoped DoProvider
// reconciler; the difference is ownership — the credentials Secret is
// checked in the config's own namespace, so tenants rotate their own
// credentials and pin their own versions.
type DoProviderConfigReconciler struct {
	// Profile carries the shared validation dependencies (client, schema
	// cache, recorder, plugin cache path).
	Profile *DoProviderReconciler
	// PerResourceNamespace mirrors the runner's namespace mode: it decides
	// where a config's credentials Secret is actually loaded from —
	// the tenant namespace in resource mode, the operator's runner
	// namespace otherwise — so Ready reflects the namespace the Job will
	// really use.
	PerResourceNamespace bool
}

// +kubebuilder:rbac:groups=do.pulumi.com,resources=doproviderconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=do.pulumi.com,resources=doproviderconfigs/status,verbs=get;update;patch

// Reconcile runs the shared profile checks against a namespaced config.
func (r *DoProviderConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	config := &dov1alpha1.DoProviderConfig{}
	if err := r.Profile.reader().Get(ctx, req.NamespacedName, config); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Runner Jobs load the credentials Secret from the namespace they
	// execute in: the config's namespace only in per-resource runner mode.
	// Validating anywhere else would mark configs Ready while operations
	// fail on a missing Secret.
	secretNamespace := r.Profile.RunnerNamespace
	if r.PerResourceNamespace {
		secretNamespace = config.Namespace
	}
	if err := r.Profile.validateProfile(ctx, config, "doproviderconfig/"+config.Namespace+"/"+config.Name,
		config.Spec, &config.Status, config.Generation, secretNamespace); err != nil {
		return ctrl.Result{}, err
	}
	if n, err := r.Profile.countDependents(ctx, dov1alpha1.ProviderKindConfig, config.Name, config.Namespace); err == nil {
		config.Status.Dependents = n
	}

	if err := r.persistStatus(ctx, config); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: providerResyncInterval}, nil
}

// persistStatus writes config.Status durably with conflict retries,
// detached from reconcile cancellation (same contract as the other
// controllers).
func (r *DoProviderConfigReconciler) persistStatus(ctx context.Context, config *dov1alpha1.DoProviderConfig) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	status := config.Status
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dov1alpha1.DoProviderConfig{}
		if err := r.Profile.reader().Get(ctx, client.ObjectKeyFromObject(config), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		latest.Status = status
		return client.IgnoreNotFound(r.Profile.Status().Update(ctx, latest))
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *DoProviderConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dov1alpha1.DoProviderConfig{}).
		Named("doproviderconfig").
		Complete(r)
}
