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

package pulumido

import (
	"context"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dirien/doplane/internal/runnerops"
)

func TestJobNamespace(t *testing.T) {
	tenantCtx := WithNamespace(context.Background(), "tenant-a")

	tests := []struct {
		name        string
		perResource bool
		ctx         context.Context
		want        string
	}{
		{"operator mode ignores the namespace tag", false, tenantCtx, "doplane-system"},
		{"resource mode runs in the tenant namespace", true, tenantCtx, "tenant-a"},
		{"resource mode without a tag falls back to the operator namespace", true, context.Background(), "doplane-system"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &JobRunner{Namespace: "doplane-system", PerResourceNamespace: tt.perResource}
			if got := r.jobNamespace(tt.ctx); got != tt.want {
				t.Errorf("jobNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

// namespaceTerminatingError mimics the API server rejecting object creation
// in a namespace that is being deleted.
func namespaceTerminatingError(ns string) error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusForbidden,
		Reason:  metav1.StatusReasonForbidden,
		Message: "unable to create new content in namespace " + ns + " because it is being terminated",
		Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: corev1.NamespaceTerminatingCause}}},
	}}
}

// terminatingNamespaceClientset rejects Job creation in ns the way the API
// server does for a terminating namespace.
func terminatingNamespaceClientset(ns string) *fake.Clientset {
	clientset := fake.NewClientset()
	clientset.PrependReactor("create", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == ns {
			return true, nil, namespaceTerminatingError(ns)
		}
		return false, nil, nil
	})
	return clientset
}

func TestEnsureJobTerminatingNamespaceFallback(t *testing.T) {
	ctx := context.Background()

	t.Run("teardown verbs fall back to the operator namespace", func(t *testing.T) {
		for _, verb := range []string{runnerops.VerbDelete, runnerops.VerbEngineDestroy} {
			r := &JobRunner{Clientset: terminatingNamespaceClientset("tenant-a"), Namespace: "doplane-system"}
			got, err := r.ensureJob(ctx, "tenant-a", "do-"+verb+"-abc", verb, "{}", nil)
			if err != nil {
				t.Fatalf("%s: teardown must not wedge on a terminating namespace: %v", verb, err)
			}
			if got != "doplane-system" {
				t.Errorf("%s: job namespace = %q, want fallback to operator namespace", verb, got)
			}
			if _, err := r.Clientset.BatchV1().Jobs("doplane-system").Get(ctx, "do-"+verb+"-abc", metav1.GetOptions{}); err != nil {
				t.Errorf("%s: fallback job must exist in the operator namespace: %v", verb, err)
			}
		}
	})

	t.Run("non-teardown verbs surface the error", func(t *testing.T) {
		r := &JobRunner{Clientset: terminatingNamespaceClientset("tenant-a"), Namespace: "doplane-system"}
		if _, err := r.ensureJob(ctx, "tenant-a", "do-create-abc", runnerops.VerbCreate, "{}", nil); err == nil {
			t.Error("create in a terminating namespace must fail, not silently run with operator credentials")
		}
	})

	t.Run("healthy namespace keeps the tenant job", func(t *testing.T) {
		r := &JobRunner{Clientset: fake.NewClientset(), Namespace: "doplane-system"}
		got, err := r.ensureJob(ctx, "tenant-a", "do-delete-abc", runnerops.VerbDelete, "{}", nil)
		if err != nil || got != "tenant-a" {
			t.Fatalf("ensureJob() = %q, %v; want tenant-a, nil", got, err)
		}
	})

	t.Run("existing job is adopted", func(t *testing.T) {
		r := &JobRunner{Clientset: fake.NewClientset(), Namespace: "doplane-system"}
		if _, err := r.ensureJob(ctx, "tenant-a", "do-delete-abc", runnerops.VerbDelete, "{}", nil); err != nil {
			t.Fatal(err)
		}
		got, err := r.ensureJob(ctx, "tenant-a", "do-delete-abc", runnerops.VerbDelete, "{}", nil)
		if err != nil || got != "tenant-a" {
			t.Fatalf("adoption: ensureJob() = %q, %v; want tenant-a, nil", got, err)
		}
	})
}

func TestBuildJobNamespaceAndCredentials(t *testing.T) {
	r := &JobRunner{Namespace: "doplane-system", CredentialsSecret: "provider-credentials"}
	job := r.buildJob("do-create-abc", "tenant-a", r.CredentialsSecret, "create", "{}", nil, false)

	if job.Namespace != "tenant-a" {
		t.Errorf("job namespace = %q, want the namespace passed in", job.Namespace)
	}
	envFrom := job.Spec.Template.Spec.Containers[0].EnvFrom
	if len(envFrom) != 1 || envFrom[0].SecretRef.Name != "provider-credentials" {
		t.Errorf("credentials secret must be referenced locally (resolved in the Job's namespace): %+v", envFrom)
	}
	if envFrom[0].SecretRef.Optional == nil || !*envFrom[0].SecretRef.Optional {
		t.Error("credentials secret must stay optional so tenants without cloud creds still run")
	}
}

func TestJobNameSecretVersionSalt(t *testing.T) {
	r := &JobRunner{Namespace: "doplane-system"}
	ctx := WithOwner(context.Background(), "ns/name")
	const opJSON = `{"verb":"patch","id":"x"}`

	base := r.jobName(ctx, runnerops.VerbPatch, opJSON)
	// An identical op without a salt must yield the same name so a retry
	// adopts the in-flight Job instead of re-running the mutation.
	if again := r.jobName(ctx, runnerops.VerbPatch, opJSON); again != base {
		t.Fatalf("job name not stable across identical ops: %q vs %q", base, again)
	}
	// A rotated secret (new salt, byte-identical opJSON) must change the name
	// so the runner cannot adopt a completed Job that ran with the old value.
	rotated := r.jobName(WithSecretVersionSalt(ctx, "v2digest"), runnerops.VerbPatch, opJSON)
	if rotated == base {
		t.Fatal("secret version salt did not change the job name")
	}
	// The salted name is itself stable so the rotated op is still adoptable.
	if again := r.jobName(WithSecretVersionSalt(ctx, "v2digest"), runnerops.VerbPatch, opJSON); again != rotated {
		t.Fatalf("salted job name not stable: %q vs %q", rotated, again)
	}
}

func TestJobTTLSeconds(t *testing.T) {
	// Read/schema results are cheap to reproduce → short TTL.
	for _, verb := range []string{runnerops.VerbRead, runnerops.VerbSchema} {
		if got := jobTTLSeconds(verb); got != 600 {
			t.Errorf("jobTTLSeconds(%q) = %d, want 600", verb, got)
		}
	}
	// A finished mutation Job is the only record of the cloud change until
	// consumed, so it must outlive a realistic operator outage.
	for _, verb := range []string{runnerops.VerbCreate, runnerops.VerbPatch, runnerops.VerbDelete, runnerops.VerbEngineUp} {
		if got := jobTTLSeconds(verb); got != 86400 {
			t.Errorf("jobTTLSeconds(%q) = %d, want 86400", verb, got)
		}
	}
	r := &JobRunner{Namespace: "doplane-system"}
	job := r.buildJob("do-create-abc", "tenant-a", "", runnerops.VerbCreate, "{}", nil, false)
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 86400 {
		t.Errorf("mutation job TTL not wired onto the Job: %v", job.Spec.TTLSecondsAfterFinished)
	}
}

func TestCacheAvailable(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewClientset(&corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-b", Name: "plugin-cache"},
	})
	r := &JobRunner{Clientset: clientset, Namespace: "doplane-system", PluginCachePVC: "plugin-cache"}

	if !r.cacheAvailable(ctx, "doplane-system") {
		t.Error("operator namespace must always mount the cache")
	}
	if !r.cacheAvailable(ctx, "tenant-b") {
		t.Error("tenant namespace with a same-named claim must mount it")
	}
	if r.cacheAvailable(ctx, "tenant-a") {
		t.Error("tenant namespace without a claim must skip the cache")
	}
	// The negative lookup is remembered: a claim created moments later
	// only takes effect after the TTL.
	_, err := clientset.CoreV1().PersistentVolumeClaims("tenant-a").Create(ctx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: "plugin-cache"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.cacheAvailable(ctx, "tenant-a") {
		t.Error("lookup must be cached within the TTL")
	}

	disabled := &JobRunner{Clientset: clientset, Namespace: "doplane-system"}
	if disabled.cacheAvailable(ctx, "doplane-system") {
		t.Error("no PVC configured means no cache anywhere")
	}
}

func TestBuildJobPluginCache(t *testing.T) {
	r := &JobRunner{
		Namespace:            "doplane-system",
		PluginCachePVC:       "doplane-plugin-cache",
		PluginCacheMountPath: "/var/lib/doplane/pulumi-home",
	}

	t.Run("operator-namespace jobs mount the cache", func(t *testing.T) {
		job := r.buildJob("do-create-abc", "doplane-system", "", "create", "{}", nil, true)
		spec := job.Spec.Template.Spec
		var mounted bool
		for _, v := range spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "doplane-plugin-cache" {
				mounted = true
			}
		}
		if !mounted {
			t.Errorf("plugin cache PVC must be a pod volume: %+v", spec.Volumes)
		}
		var cacheEnv string
		for _, e := range spec.Containers[0].Env {
			if e.Name == "DOPLANE_PLUGIN_CACHE" {
				cacheEnv = e.Value
			}
		}
		if cacheEnv != "/var/lib/doplane/pulumi-home" {
			t.Errorf("DOPLANE_PLUGIN_CACHE = %q, want the mount path", cacheEnv)
		}
		if spec.SecurityContext.FSGroup == nil || *spec.SecurityContext.FSGroup != 65532 {
			t.Error("fsGroup 65532 required so the non-root runner can write the PVC")
		}
		if sc := spec.Containers[0].SecurityContext; sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
			t.Error("root filesystem must stay read-only with the cache mounted")
		}
	})

	t.Run("tenant-namespace jobs skip the cache (PVCs are namespace-local)", func(t *testing.T) {
		job := r.buildJob("do-create-abc", "tenant-a", "", "create", "{}", nil, false)
		spec := job.Spec.Template.Spec
		for _, v := range spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				t.Errorf("tenant-namespace job must not reference the operator-namespace PVC: %+v", v)
			}
		}
		if spec.SecurityContext.FSGroup != nil {
			t.Error("fsGroup should stay unset without the cache mount")
		}
	})
}
