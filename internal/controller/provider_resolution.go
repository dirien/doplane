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

// providerRefIndexKey indexes DoResources by the cluster-scoped DoProvider
// they reference; providerConfigIndexKey does the same for namespaced
// DoProviderConfigs. Profile events then re-enqueue exactly the resources
// using them.
const (
	providerRefIndexKey    = ".spec.providerRef.name"
	providerConfigIndexKey = ".spec.providerRef.configName"
)

// providerProfile is the kind-independent view of a resolved provider
// profile: the spec rules plus a human-readable reference for messages.
type providerProfile struct {
	ref  string
	spec dov1alpha1.DoProviderSpec
}

// getProvider fetches the referenced profile — a cluster-scoped DoProvider
// (default) or a DoProviderConfig in the resource's namespace. A nil
// profile with nil error means the resource has no providerRef; not-found
// surfaces as apierrors.IsNotFound for the caller to translate per
// reconcile phase.
func (r *DoResourceReconciler) getProvider(ctx context.Context, res *dov1alpha1.DoResource) (*providerProfile, error) {
	ref := res.Spec.ProviderRef
	if ref == nil {
		return nil, nil
	}
	if ref.Kind == dov1alpha1.ProviderKindConfig {
		config := &dov1alpha1.DoProviderConfig{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: ref.Name}, config); err != nil {
			return nil, err
		}
		return &providerProfile{
			ref:  fmt.Sprintf("doproviderconfig %q", res.Namespace+"/"+ref.Name),
			spec: config.Spec,
		}, nil
	}
	provider := &dov1alpha1.DoProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name}, provider); err != nil {
		return nil, err
	}
	return &providerProfile{ref: fmt.Sprintf("doprovider %q", ref.Name), spec: provider.Spec}, nil
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
	profile, err := r.getProvider(ctx, res)
	if err != nil && !isProviderNotFound(err) {
		return ctx, pkg, &ctrl.Result{}, err
	}
	if profile != nil {
		if profile.spec.CredentialsSecretRef != nil {
			ctx = pulumido.WithCredentialsSecret(ctx, profile.spec.CredentialsSecretRef.Name)
		}
		if pkg == "" || !res.DeletionTimestamp.IsZero() {
			// On the live path a conflicting spec.package is rejected below;
			// during teardown the profile's pin always wins.
			pkg = profile.spec.Package
		}
	}
	if !res.DeletionTimestamp.IsZero() {
		return ctx, pkg, nil, nil
	}
	if err != nil {
		result, ferr := r.markSyncFailed(ctx, res, "ProviderNotFound",
			fmt.Errorf("provider profile %q (kind %s) not found",
				res.Spec.ProviderRef.Name, refKind(res.Spec.ProviderRef)), false)
		return ctx, pkg, &result, ferr
	}
	if profile != nil {
		if pkg != profile.spec.Package {
			result, ferr := r.markSyncFailed(ctx, res, "ProviderPackageMismatch",
				fmt.Errorf("spec.package %q conflicts with %s package %q; drop spec.package or make them match",
					pkg, profile.ref, profile.spec.Package), false)
			return ctx, pkg, &result, ferr
		}
		if !tokenAllowed(profile.spec.AllowedResources, token) {
			result, ferr := r.markSyncFailed(ctx, res, "ResourceNotAllowed",
				fmt.Errorf("resource type %q is not in %s allowedResources", token, profile.ref), false)
			return ctx, pkg, &result, ferr
		}
	}
	return ctx, pkg, nil, nil
}

// refKind normalizes the providerRef kind (defaulted by the CRD, but the
// zero value can appear on objects created before defaulting).
func refKind(ref *dov1alpha1.ProviderReference) string {
	if ref != nil && ref.Kind == dov1alpha1.ProviderKindConfig {
		return dov1alpha1.ProviderKindConfig
	}
	return dov1alpha1.ProviderKindCluster
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
	return r.requestsFromIndex(ctx, providerRefIndexKey, obj.GetName(), "")
}

// mapProviderConfigResources re-enqueues DoResources in the config's own
// namespace that reference it (configs never apply across namespaces).
func (r *DoResourceReconciler) mapProviderConfigResources(ctx context.Context, obj client.Object) []reconcile.Request {
	return r.requestsFromIndex(ctx, providerConfigIndexKey, obj.GetName(), obj.GetNamespace())
}

func (r *DoResourceReconciler) requestsFromIndex(ctx context.Context, key, value, namespace string) []reconcile.Request {
	list := &dov1alpha1.DoResourceList{}
	opts := []client.ListOption{client.MatchingFields{key: value}}
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := r.List(ctx, list, opts...); err != nil {
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
