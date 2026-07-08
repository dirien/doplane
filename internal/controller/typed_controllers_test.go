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
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

func TestPluralize(t *testing.T) {
	tests := map[string]string{
		"BucketV2":   "bucketv2s",
		"Instance":   "instances",
		"Address":    "addresses",
		"Policy":     "policies",
		"StaticSite": "staticsites",
	}
	for kind, want := range tests {
		if got := pluralize(kind); got != want {
			t.Errorf("pluralize(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestTypedRegistrarClaim(t *testing.T) {
	reg := &TypedRegistrar{}
	if err := reg.claim("buckets", "resource:aws:s3/bucket:Bucket"); err != nil {
		t.Fatalf("first claim must succeed: %v", err)
	}
	if err := reg.claim("buckets", "resource:aws:s3/bucket:Bucket"); err != nil {
		t.Fatalf("re-claim by the same owner must succeed: %v", err)
	}
	err := reg.claim("buckets", "resource:gcp:storage/bucket:Bucket")
	if err == nil {
		t.Fatal("a different owner claiming the same plural must be rejected")
	}
	if got := err.Error(); !contains(got, "aws:s3/bucket:Bucket") {
		t.Errorf("error must name the existing owner: %v", err)
	}
	if err := reg.claim("staticsites", "composite:site-def"); err != nil {
		t.Fatalf("unrelated plurals stay claimable: %v", err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

var _ = Describe("Typed managed resources and composite APIs", func() {
	const token = "random:index/randomPet:RandomPet"
	ctx := context.Background()

	It("generates a CRD from the provider schema and translates typed objects", func() {
		pkgSchema, err := (&fakeRunner{}).FetchSchema(ctx, "random@4.21.0", token)
		Expect(err).NotTo(HaveOccurred())

		crd, err := typedResourceCRD(token, pkgSchema)
		Expect(err).NotTo(HaveOccurred())
		Expect(crd.Name).To(Equal("randompets." + typedGroup))
		Expect(crd.Spec.Names.Kind).To(Equal("RandomPet"))
		props := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"].Properties
		Expect(props["forProvider"].Properties["length"].Type).To(Equal("integer"))
		Expect(props["forProvider"].Properties["prefix"].Type).To(Equal("string"))

		Expect(k8sClient.Create(ctx, crd)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: crd.Name},
			})
		})

		gvk := schema.GroupVersionKind{Group: typedGroup, Version: typedVersion, Kind: "RandomPet"}
		typed := &unstructured.Unstructured{}
		typed.SetGroupVersionKind(gvk)
		typed.SetNamespace("default")
		typed.SetName("typed-pet")
		Expect(unstructured.SetNestedMap(typed.Object, map[string]any{
			"length": int64(2), "prefix": "typed",
		}, "spec", "forProvider")).To(Succeed())
		// Discovery needs a moment to serve the fresh CRD.
		Eventually(func() error {
			return k8sClient.Create(ctx, typed)
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, typed) })

		rec := &TypedResourceReconciler{
			Client: k8sClient, Scheme: kscheme.Scheme, GVK: gvk, Token: token, ProviderName: "random",
		}
		key := types.NamespacedName{Namespace: "default", Name: "typed-pet"}
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		mirror := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, key, mirror)).To(Succeed())
		Expect(mirror.Spec.Type).To(Equal(token))
		Expect(mirror.Spec.ProviderRef).NotTo(BeNil())
		Expect(mirror.Spec.ProviderRef.Name).To(Equal("random"))
		Expect(string(mirror.Spec.Properties.Raw)).To(ContainSubstring(`"prefix":"typed"`))
		owner := metav1.GetControllerOf(mirror)
		Expect(owner).NotTo(BeNil())
		Expect(owner.Kind).To(Equal("RandomPet"))
		DeferCleanup(func() {
			mirror.Finalizers = nil
			_ = k8sClient.Update(ctx, mirror)
			_ = k8sClient.Delete(ctx, mirror)
		})

		// Status flows back to the typed object.
		mirror.Status.ID = "typed-id"
		Expect(k8sClient.Status().Update(ctx, mirror)).To(Succeed())
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, typed)).To(Succeed())
		id, _, _ := unstructured.NestedString(typed.Object, "status", "id")
		Expect(id).To(Equal("typed-id"))
	})

	It("serves platform APIs that translate to DoComposites", func() {
		api := &dov1alpha1.CompositeAPI{Kind: "StaticSite"}
		crd, err := typedCompositeCRD("site-def", api)
		Expect(err).NotTo(HaveOccurred())
		Expect(crd.Name).To(Equal("staticsites." + typedGroup))

		Expect(k8sClient.Create(ctx, crd)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: crd.Name},
			})
		})

		gvk := schema.GroupVersionKind{Group: typedGroup, Version: typedVersion, Kind: "StaticSite"}
		typed := &unstructured.Unstructured{}
		typed.SetGroupVersionKind(gvk)
		typed.SetNamespace("default")
		typed.SetName("my-site")
		Expect(unstructured.SetNestedMap(typed.Object, map[string]any{"team": "docs"}, "spec")).To(Succeed())
		Eventually(func() error {
			return k8sClient.Create(ctx, typed)
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, typed) })

		rec := &TypedCompositeReconciler{Client: k8sClient, Scheme: kscheme.Scheme, GVK: gvk, Definition: "site-def"}
		key := types.NamespacedName{Namespace: "default", Name: "my-site"}
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		mirror := &dov1alpha1.DoComposite{}
		Expect(k8sClient.Get(ctx, key, mirror)).To(Succeed())
		Expect(mirror.Spec.Definition).To(Equal("site-def"))
		Expect(string(mirror.Spec.Parameters.Raw)).To(ContainSubstring(`"team":"docs"`))
		Expect(metav1.GetControllerOf(mirror)).NotTo(BeNil())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, mirror) })
	})
})
