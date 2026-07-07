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
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// providerResyncInterval re-checks providers periodically: credentials
// Secrets change out of band (rotation, sync scripts) and there is no watch
// on them.
const providerResyncInterval = 10 * time.Minute

// DoProviderReconciler validates cluster-scoped provider profiles: schema
// availability, plugin readiness and credentials. It performs no cloud
// mutations — a not-Ready provider is a signal to platform teams, and
// DoResources referencing it fail with their own conditions.
type DoProviderReconciler struct {
	client.Client
	Live     client.Reader
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Schemas  *pulumido.SchemaCache

	// RunnerNamespace is where the credentials Secret is checked — the
	// namespace runner Jobs execute in ("" skips the check in dev mode).
	RunnerNamespace string
	// PluginCachePath is the shared plugin cache mount in runner pods
	// ("" when the cache is disabled).
	PluginCachePath string
}

// +kubebuilder:rbac:groups=do.pulumi.com,resources=doproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=do.pulumi.com,resources=doproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// Reconcile fetches the provider schema, records the resolved package and
// verifies the credentials Secret, rolling everything up into Ready.
func (r *DoProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	provider := &dov1alpha1.DoProvider{}
	if err := r.reader().Get(ctx, req.NamespacedName, provider); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.validateProfile(ctx, provider, "doprovider/"+provider.Name,
		provider.Spec, &provider.Status, provider.Generation, r.RunnerNamespace); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.persistStatus(ctx, provider); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: providerResyncInterval}, nil
}

// validateProfile runs the shared profile checks (schema, plugin,
// credentials, Ready roll-up) for a DoProvider or DoProviderConfig,
// mutating status in place. secretNamespace is where the credentials
// Secret is checked: the runner namespace for cluster profiles, the
// config's own namespace for tenant profiles.
func (r *DoProviderReconciler) validateProfile(ctx context.Context, obj runtime.Object, owner string,
	spec dov1alpha1.DoProviderSpec, status *dov1alpha1.DoProviderStatus, generation int64, secretNamespace string,
) error {
	log := logf.FromContext(ctx)

	if !pulumido.PackagePinned(spec.Package) {
		r.Recorder.Eventf(obj, "Warning", "ProviderNotPinned",
			"spec.package %q has no pinned version; operations are not reproducible", spec.Package)
	}

	// Schema fetch runs through the runner (a Job in-cluster), which also
	// installs the pinned plugin into the shared cache when enabled — one
	// step proves both schema and plugin availability.
	schemaOK := true
	schema, err := r.Schemas.Get(pulumido.WithOwner(ctx, owner), spec.Package, "")
	if err != nil {
		schemaOK = false
		reason := schemaFailureReason(err)
		setProfileCondition(status, generation, dov1alpha1.ConditionSchemaFetched, metav1.ConditionFalse, reason, compact(err.Error()))
		setProfileCondition(status, generation, dov1alpha1.ConditionPluginReady, metav1.ConditionFalse, reason,
			"plugin availability unknown while the schema cannot be fetched")
		status.Plugin = &dov1alpha1.ProviderPluginStatus{Ready: false, CachePath: r.PluginCachePath}
		log.Error(err, "provider schema fetch failed", "package", spec.Package)
	} else {
		setProfileCondition(status, generation, dov1alpha1.ConditionSchemaFetched, metav1.ConditionTrue, "SchemaFetched",
			fmt.Sprintf("schema for %s@%s fetched", schema.Name, schema.Version))
		setProfileCondition(status, generation, dov1alpha1.ConditionPluginReady, metav1.ConditionTrue, "PluginAvailable",
			pluginReadyMessage(r.PluginCachePath))
		status.Package = &dov1alpha1.ProviderPackageStatus{Name: schema.Name, Version: schema.Version}
		status.Plugin = &dov1alpha1.ProviderPluginStatus{Ready: true, CachePath: r.PluginCachePath}
	}

	credsOK, credsErr := r.checkCredentials(ctx, spec, status, generation, secretNamespace)
	if credsErr != nil {
		return credsErr
	}

	if schemaOK && credsOK {
		setProfileCondition(status, generation, dov1alpha1.ConditionReady, metav1.ConditionTrue, "Ready", "provider profile validated")
	} else {
		reason := "SchemaUnavailable"
		if schemaOK {
			reason = "CredentialsNotReady"
		}
		setProfileCondition(status, generation, dov1alpha1.ConditionReady, metav1.ConditionFalse, reason,
			"one or more provider checks failed; see the other conditions")
	}
	status.ObservedGeneration = generation
	return nil
}

// checkCredentials verifies the configured Secret exists in
// secretNamespace and holds every required key. The bool is the condition
// outcome; the error is only for API failures worth a retry.
func (r *DoProviderReconciler) checkCredentials(ctx context.Context, spec dov1alpha1.DoProviderSpec,
	status *dov1alpha1.DoProviderStatus, generation int64, secretNamespace string,
) (bool, error) {
	ref := spec.CredentialsSecretRef
	if ref == nil {
		setProfileCondition(status, generation, dov1alpha1.ConditionCredentialsReady, metav1.ConditionTrue, "NotRequired",
			"no credentials Secret configured; the deployment-wide default applies")
		return true, nil
	}
	if secretNamespace == "" {
		setProfileCondition(status, generation, dov1alpha1.ConditionCredentialsReady, metav1.ConditionTrue, "CheckSkipped",
			"no runner namespace configured (dev mode); Secret not verified")
		return true, nil
	}

	secret := &corev1.Secret{}
	err := r.reader().Get(ctx, types.NamespacedName{Namespace: secretNamespace, Name: ref.Name}, secret)
	switch {
	case err == nil:
		var missing []string
		for _, key := range spec.CredentialKeys {
			if _, ok := secret.Data[key]; !ok {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			setProfileCondition(status, generation, dov1alpha1.ConditionCredentialsReady, metav1.ConditionFalse, "KeysMissing",
				fmt.Sprintf("secret %s/%s is missing keys: %s", secretNamespace, ref.Name, strings.Join(missing, ", ")))
			return false, nil
		}
		setProfileCondition(status, generation, dov1alpha1.ConditionCredentialsReady, metav1.ConditionTrue, "CredentialsReady",
			fmt.Sprintf("secret %s/%s holds all required keys", secretNamespace, ref.Name))
		return true, nil
	case client.IgnoreNotFound(err) == nil:
		setProfileCondition(status, generation, dov1alpha1.ConditionCredentialsReady, metav1.ConditionFalse, "SecretNotFound",
			fmt.Sprintf("secret %s/%s not found", secretNamespace, ref.Name))
		return false, nil
	default:
		return false, err
	}
}

// schemaFailureReason maps a schema fetch error onto its condition reason,
// preferring the runner's typed code.
func schemaFailureReason(err error) string {
	var coded *pulumido.CodedError
	if errors.As(err, &coded) && coded.Code != "" {
		return coded.Code
	}
	return "SchemaUnavailable"
}

func pluginReadyMessage(cachePath string) string {
	if cachePath == "" {
		return "plugin served from baked image plugins or on-demand download"
	}
	return "plugin available via the shared cache at " + cachePath
}

func setProfileCondition(profileStatus *dov1alpha1.DoProviderStatus, generation int64,
	condType string, status metav1.ConditionStatus, reason, message string,
) {
	meta.SetStatusCondition(&profileStatus.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

// persistStatus writes provider.Status durably with conflict retries,
// detached from reconcile cancellation (same contract as the DoResource
// controller).
func (r *DoProviderReconciler) persistStatus(ctx context.Context, provider *dov1alpha1.DoProvider) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	status := provider.Status
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &dov1alpha1.DoProvider{}
		if err := r.reader().Get(ctx, client.ObjectKeyFromObject(provider), latest); err != nil {
			return client.IgnoreNotFound(err)
		}
		latest.Status = status
		return client.IgnoreNotFound(r.Status().Update(ctx, latest))
	})
}

func (r *DoProviderReconciler) reader() client.Reader {
	if r.Live != nil {
		return r.Live
	}
	return r.Client
}

// SetupWithManager sets up the controller with the Manager.
func (r *DoProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dov1alpha1.DoProvider{}).
		Named("doprovider").
		Complete(r)
}
