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
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// providerRefIndexKey indexes DoResources by the DoProvider they reference,
// so provider events re-enqueue exactly the resources using them.
const providerRefIndexKey = ".spec.providerRef.name"

// getProvider fetches the referenced DoProvider. A nil provider with nil
// error means the resource has no providerRef; not-found surfaces as
// apierrors.IsNotFound for the caller to translate per reconcile phase.
func (r *DoResourceReconciler) getProvider(ctx context.Context, res *dov1alpha1.DoResource) (*dov1alpha1.DoProvider, error) {
	if res.Spec.ProviderRef == nil {
		return nil, nil
	}
	provider := &dov1alpha1.DoProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: res.Spec.ProviderRef.Name}, provider); err != nil {
		return nil, err
	}
	return provider, nil
}

// resolveProvider applies spec.providerRef to the reconcile: the provider's
// credentials Secret is tagged onto ctx, the effective package is resolved,
// and on the live path package conflicts and the allow-list are enforced.
// A non-nil halt means reconcile must return (*halt, err) immediately.
// During teardown a vanished provider does not halt — the finalizer must
// run, with the deployment-default credentials as fallback.
func (r *DoResourceReconciler) resolveProvider(ctx context.Context, res *dov1alpha1.DoResource, pkg, token string) (
	context.Context, string, *ctrl.Result, error,
) {
	provider, err := r.getProvider(ctx, res)
	if err != nil && !isProviderNotFound(err) {
		return ctx, pkg, &ctrl.Result{}, err
	}
	if provider != nil {
		if provider.Spec.CredentialsSecretRef != nil {
			ctx = pulumido.WithCredentialsSecret(ctx, provider.Spec.CredentialsSecretRef.Name)
		}
		if pkg == "" || !res.DeletionTimestamp.IsZero() {
			// On the live path a conflicting spec.package is rejected below;
			// during teardown the provider's pin always wins.
			pkg = provider.Spec.Package
		}
	}
	if !res.DeletionTimestamp.IsZero() {
		return ctx, pkg, nil, nil
	}
	if err != nil {
		result, ferr := r.markSyncFailed(ctx, res, "ProviderNotFound",
			fmt.Errorf("doprovider %q not found", res.Spec.ProviderRef.Name), false)
		return ctx, pkg, &result, ferr
	}
	if provider != nil {
		if pkg != provider.Spec.Package {
			result, ferr := r.markSyncFailed(ctx, res, "ProviderPackageMismatch",
				fmt.Errorf("spec.package %q conflicts with doprovider %q package %q; drop spec.package or make them match",
					pkg, provider.Name, provider.Spec.Package), false)
			return ctx, pkg, &result, ferr
		}
		if !tokenAllowed(provider.Spec.AllowedResources, token) {
			result, ferr := r.markSyncFailed(ctx, res, "ResourceNotAllowed",
				fmt.Errorf("resource type %q is not in doprovider %q allowedResources", token, provider.Name), false)
			return ctx, pkg, &result, ferr
		}
	}
	return ctx, pkg, nil, nil
}

// tokenAllowed reports whether a resource type token is permitted by a
// provider's allowedResources list. Entries match the full token
// ("aws:s3/bucketV2:BucketV2"), the module path ("s3/bucketV2",
// case-insensitive), a module glob ("s3/*") or everything ("*"). An empty
// list allows every resource.
func tokenAllowed(allowed []string, token string) bool {
	if len(allowed) == 0 {
		return true
	}
	module := token
	if parts := strings.SplitN(token, ":", 3); len(parts) == 3 {
		module = parts[1]
	}
	for _, pattern := range allowed {
		switch {
		case pattern == "*" || pattern == token:
			return true
		case strings.EqualFold(pattern, module):
			return true
		}
		if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
			if strings.EqualFold(prefix, module) ||
				strings.HasPrefix(strings.ToLower(module), strings.ToLower(prefix)+"/") {
				return true
			}
		}
	}
	return false
}

// mapProviderResources re-enqueues every DoResource referencing a changed
// DoProvider (profile edits change resolved packages and credentials).
func (r *DoResourceReconciler) mapProviderResources(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &dov1alpha1.DoResourceList{}
	if err := r.List(ctx, list, client.MatchingFields{providerRefIndexKey: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, res := range list.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: res.Namespace, Name: res.Name},
		})
	}
	return reqs
}

// isProviderNotFound distinguishes a vanished provider from API failures.
func isProviderNotFound(err error) bool {
	return apierrors.IsNotFound(err)
}
