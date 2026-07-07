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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

func TestTokenAllowed(t *testing.T) {
	const bucketType = "aws:s3/bucketV2:BucketV2"
	tests := []struct {
		name    string
		allowed []string
		want    bool
	}{
		{"empty list allows all", nil, true},
		{"star allows all", []string{"*"}, true},
		{"full token", []string{"aws:s3/bucketV2:BucketV2"}, true},
		{"module path", []string{"s3/bucketV2"}, true},
		{"module path is case-insensitive", []string{"S3/BucketV2"}, true},
		{"module glob", []string{"s3/*"}, true},
		{"other module glob", []string{"ec2/*"}, false},
		{"unrelated token", []string{"aws:ec2/instance:Instance"}, false},
		{"several entries, one matches", []string{"ec2/*", "s3/*"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tokenAllowed(tt.allowed, bucketType); got != tt.want {
				t.Errorf("tokenAllowed(%v, %q) = %t, want %t", tt.allowed, bucketType, got, tt.want)
			}
		})
	}
}

var _ = Describe("DoResource providerRef resolution", func() {
	const resourceName = "test-refresource"
	const providerName = "test-refprovider"
	const token = "random:index/randomPet:RandomPet"

	ctx := context.Background()
	resourceKey := types.NamespacedName{Namespace: "default", Name: resourceName}

	var runner *fakeRunner
	var reconciler *DoResourceReconciler

	BeforeEach(func() {
		runner = &fakeRunner{}
		reconciler = &DoResourceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(32),
			Runner:   runner,
			Schemas:  pulumido.NewSchemaCache(runner),
		}
	})

	createProvider := func(spec dov1alpha1.DoProviderSpec) {
		provider := &dov1alpha1.DoProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
	}

	createResource := func(spec dov1alpha1.DoResourceSpec) {
		if spec.Properties == nil {
			spec.Properties = &apiextensionsv1.JSON{Raw: []byte(`{"length": 3}`)}
		}
		res := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: resourceName},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, res)).To(Succeed())
	}

	reconcileN := func(n int) {
		for range n {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
			Expect(err).NotTo(HaveOccurred())
		}
	}

	syncedCondition := func() *metav1.Condition {
		res := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		return meta.FindStatusCondition(res.Status.Conditions, dov1alpha1.ConditionSynced)
	}

	AfterEach(func() {
		res := &dov1alpha1.DoResource{}
		if err := k8sClient.Get(ctx, resourceKey, res); err == nil {
			_ = k8sClient.Delete(ctx, res)
			for range 3 {
				if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey}); err != nil {
					break
				}
				if err := k8sClient.Get(ctx, resourceKey, res); errors.IsNotFound(err) {
					break
				}
			}
		}
		provider := &dov1alpha1.DoProvider{ObjectMeta: metav1.ObjectMeta{Name: providerName}}
		_ = k8sClient.Delete(ctx, provider)
	})

	It("resolves the package from the provider profile", func() {
		createProvider(dov1alpha1.DoProviderSpec{Package: "random@4.21.0"})
		createResource(dov1alpha1.DoResourceSpec{
			Type:        token,
			ProviderRef: &dov1alpha1.ProviderReference{Name: providerName},
		})

		reconcileN(2)
		Expect(runner.createdPkgs).To(ConsistOf("random@4.21.0"))
	})

	It("rejects a spec.package that conflicts with the provider", func() {
		createProvider(dov1alpha1.DoProviderSpec{Package: "random@4.21.0"})
		createResource(dov1alpha1.DoResourceSpec{
			Type:        token,
			Package:     "random@4.20.0",
			ProviderRef: &dov1alpha1.ProviderReference{Name: providerName},
		})

		reconcileN(2)
		cond := syncedCondition()
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("ProviderPackageMismatch"))
		Expect(runner.created).To(BeEmpty())
	})

	It("rejects resource types outside allowedResources", func() {
		createProvider(dov1alpha1.DoProviderSpec{
			Package:          "random@4.21.0",
			AllowedResources: []string{"index/randomString"},
		})
		createResource(dov1alpha1.DoResourceSpec{
			Type:        token,
			ProviderRef: &dov1alpha1.ProviderReference{Name: providerName},
		})

		reconcileN(2)
		cond := syncedCondition()
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("ResourceNotAllowed"))
		Expect(runner.created).To(BeEmpty())
	})

	It("reports ProviderNotFound for a dangling providerRef", func() {
		createResource(dov1alpha1.DoResourceSpec{
			Type:        token,
			ProviderRef: &dov1alpha1.ProviderReference{Name: providerName},
		})

		reconcileN(2)
		cond := syncedCondition()
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("ProviderNotFound"))
		Expect(runner.created).To(BeEmpty())
	})

	It("resolves a namespaced DoProviderConfig in the resource's namespace", func() {
		config := &dov1alpha1.DoProviderConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: providerName},
			Spec:       dov1alpha1.DoProviderSpec{Package: "random@4.19.0"},
		}
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, config) })

		// A cluster-scoped DoProvider with the same name but a different
		// package proves kind selection: the config must win.
		createProvider(dov1alpha1.DoProviderSpec{Package: "random@4.21.0"})
		createResource(dov1alpha1.DoResourceSpec{
			Type:        token,
			ProviderRef: &dov1alpha1.ProviderReference{Name: providerName, Kind: dov1alpha1.ProviderKindConfig},
		})

		reconcileN(2)
		Expect(runner.createdPkgs).To(ConsistOf("random@4.19.0"))
	})
})
