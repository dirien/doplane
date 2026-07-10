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
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
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

func TestCheckCRDOwnership(t *testing.T) {
	crd := func(labels, annotations map[string]string) *apiextensionsv1.CustomResourceDefinition {
		return &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{
			Name: "buckets.typed.do.pulumi.com", Labels: labels, Annotations: annotations,
		}}
	}
	ours := map[string]string{labelManagedByKey: "doplane"}
	if err := checkCRDOwnership(crd(ours, map[string]string{annTypedOwner: "resource:aws:s3/bucket:Bucket"}),
		"resource:aws:s3/bucket:Bucket"); err != nil {
		t.Fatalf("same owner must pass: %v", err)
	}
	if err := checkCRDOwnership(crd(ours, nil), "resource:aws:s3/bucket:Bucket"); err != nil {
		t.Fatalf("doplane-managed CRD without owner annotation (pre-annotation release) must be adopted: %v", err)
	}
	err := checkCRDOwnership(crd(ours, map[string]string{annTypedOwner: "resource:aws:s3/bucket:Bucket"}),
		"composite:site-def")
	if err == nil || !contains(err.Error(), "aws:s3/bucket:Bucket") {
		t.Fatalf("a different owner must be rejected, naming the current one: %v", err)
	}
	if !errorsIsCRDConflict(err) {
		t.Fatalf("collision must be errCRDConflict: %v", err)
	}
	if err := checkCRDOwnership(crd(nil, nil), "composite:site-def"); err == nil {
		t.Fatal("a foreign (unlabelled) CRD must never be overwritten")
	}
}

func errorsIsCRDConflict(err error) bool { return errors.Is(err, errCRDConflict) }

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

		// An unchanged status must not be rewritten — the typed kind is
		// watched without predicates, so a no-op write would requeue forever.
		rv := typed.GetResourceVersion()
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, typed)).To(Succeed())
		Expect(typed.GetResourceVersion()).To(Equal(rv))
	})

	It("refuses to adopt a DoResource it does not control", func() {
		const strToken = "random:index/randomString:RandomString"
		pkgSchema, err := (&fakeRunner{}).FetchSchema(ctx, "random@4.21.0", strToken)
		Expect(err).NotTo(HaveOccurred())
		crd, err := typedResourceCRD(strToken, pkgSchema)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, crd)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: crd.Name},
			})
		})

		// A DoResource with the colliding name that no typed object owns.
		existing := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "shared-name"},
			Spec:       dov1alpha1.DoResourceSpec{Type: token},
		}
		Expect(k8sClient.Create(ctx, existing)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, existing) })

		gvk := schema.GroupVersionKind{Group: typedGroup, Version: typedVersion, Kind: "RandomString"}
		typed := &unstructured.Unstructured{}
		typed.SetGroupVersionKind(gvk)
		typed.SetNamespace("default")
		typed.SetName("shared-name")
		Expect(unstructured.SetNestedMap(typed.Object, map[string]any{"length": int64(4)}, "spec", "forProvider")).To(Succeed())
		Eventually(func() error {
			return k8sClient.Create(ctx, typed)
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, typed) })

		rec := &TypedResourceReconciler{Client: k8sClient, Scheme: kscheme.Scheme, GVK: gvk, Token: strToken}
		key := types.NamespacedName{Namespace: "default", Name: "shared-name"}
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(MatchError(ContainSubstring("not controlled by")))

		// The colliding DoResource stays untouched and unowned.
		fresh := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, key, fresh)).To(Succeed())
		Expect(fresh.Spec.Type).To(Equal(token))
		Expect(fresh.OwnerReferences).To(BeEmpty())
	})

	It("serves component schemas as typed CRDs and drives mirrors through the component engine", func() {
		const (
			//nolint:gosec // G101 false positive: a Pulumi type token, not a credential.
			componentToken = "web-app:index:WebAppComponent"
			providerName   = "typed-web-app-provider"
		)
		runner := &fakeRunner{componentMode: true}
		pkgSchema, err := runner.FetchSchema(ctx, "private/ediri/web-app", componentToken)
		Expect(err).NotTo(HaveOccurred())

		crd, err := typedResourceCRD(componentToken, pkgSchema)
		Expect(err).NotTo(HaveOccurred())
		Expect(crd.Name).To(Equal("webappcomponents." + typedGroup))
		Expect(crd.Spec.Names.Kind).To(Equal("WebAppComponent"))
		Expect(crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Description).To(ContainSubstring("Pulumi component"))
		Expect(k8sClient.Create(ctx, crd)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: crd.Name},
			})
		})

		provider := &dov1alpha1.DoProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
			Spec: dov1alpha1.DoProviderSpec{
				Package:          "private/ediri/web-app",
				AllowedResources: []string{componentToken},
				TypedResources:   []string{componentToken},
			},
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, provider) })

		gvk := schema.GroupVersionKind{Group: typedGroup, Version: typedVersion, Kind: "WebAppComponent"}
		typed := &unstructured.Unstructured{}
		typed.SetGroupVersionKind(gvk)
		typed.SetNamespace("default")
		typed.SetName("typed-web-app")
		Expect(unstructured.SetNestedMap(typed.Object, map[string]any{
			"length": int64(2),
			"prefix": "web",
		}, "spec", "forProvider")).To(Succeed())
		Eventually(func() error {
			return k8sClient.Create(ctx, typed)
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, typed) })

		key := types.NamespacedName{Namespace: "default", Name: "typed-web-app"}
		typedRec := &TypedResourceReconciler{
			Client: k8sClient, Scheme: kscheme.Scheme, GVK: gvk, Token: componentToken, ProviderName: providerName,
		}
		_, err = typedRec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		mirror := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, key, mirror)).To(Succeed())
		Expect(mirror.Spec.Type).To(Equal(componentToken))
		Expect(mirror.Spec.Package).To(BeEmpty())
		Expect(mirror.Spec.ProviderRef).NotTo(BeNil())
		Expect(mirror.Spec.ProviderRef.Name).To(Equal(providerName))
		Expect(string(mirror.Spec.Properties.Raw)).To(ContainSubstring(`"prefix":"web"`))
		DeferCleanup(func() {
			current := &dov1alpha1.DoResource{}
			if err := k8sClient.Get(ctx, key, current); err != nil {
				return
			}
			current.Finalizers = nil
			_ = k8sClient.Update(ctx, current)
			_ = k8sClient.Delete(ctx, current)
		})

		resourceRec := &DoResourceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(32),
			Runner:   runner,
			Schemas:  pulumido.NewSchemaCache(runner),
		}
		for i := range 2 {
			_, err = resourceRec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("resource reconcile #%d", i+1))
		}

		Expect(runner.created).To(BeEmpty(), "component typed resources must not use stateless create")
		Expect(runner.componentCreates).To(HaveLen(1))
		Expect(runner.componentCreates[0]).To(HaveKeyWithValue("prefix", "web"))

		Expect(k8sClient.Get(ctx, key, mirror)).To(Succeed())
		Expect(mirror.Status.ID).To(ContainSubstring(componentToken))
		Expect(mirror.Status.EngineState).NotTo(BeNil())
		Expect(string(mirror.Status.Outputs.Raw)).To(ContainSubstring(`"endpoint":"svc.local:8080"`))

		_, err = typedRec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, typed)).To(Succeed())
		id, _, _ := unstructured.NestedString(typed.Object, "status", "id")
		Expect(id).To(Equal(mirror.Status.ID))
		outputs, _, _ := unstructured.NestedMap(typed.Object, "status", "outputs")
		Expect(outputs).To(HaveKeyWithValue("endpoint", "svc.local:8080"))
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
		Expect(unstructured.SetNestedMap(typed.Object, map[string]any{
			"team": "docs",
			"doplane": map[string]any{
				"updatePolicy": "Manual",
				"revisionRef":  map[string]any{"name": "site-def-v2"},
			},
		}, "spec")).To(Succeed())
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
		// The reserved doplane block maps to lifecycle knobs, not parameters.
		Expect(string(mirror.Spec.Parameters.Raw)).NotTo(ContainSubstring("doplane"))
		Expect(mirror.Spec.UpdatePolicy).To(Equal(dov1alpha1.UpdateManual))
		Expect(mirror.Spec.RevisionRef).NotTo(BeNil())
		Expect(mirror.Spec.RevisionRef.Name).To(Equal("site-def-v2"))
		Expect(metav1.GetControllerOf(mirror)).NotTo(BeNil())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, mirror) })

		// The roll-up status is unchanged → no write, no requeue storm.
		Expect(k8sClient.Get(ctx, key, typed)).To(Succeed())
		rv := typed.GetResourceVersion()
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key, typed)).To(Succeed())
		Expect(typed.GetResourceVersion()).To(Equal(rv))
	})

	It("refuses to adopt a DoComposite it does not control", func() {
		api := &dov1alpha1.CompositeAPI{Kind: "Portal"}
		crd, err := typedCompositeCRD("portal-def", api)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Create(ctx, crd)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{Name: crd.Name},
			})
		})

		existing := &dov1alpha1.DoComposite{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "shared-portal"},
			Spec:       dov1alpha1.DoCompositeSpec{Definition: "other-def"},
		}
		Expect(k8sClient.Create(ctx, existing)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, existing) })

		gvk := schema.GroupVersionKind{Group: typedGroup, Version: typedVersion, Kind: "Portal"}
		typed := &unstructured.Unstructured{}
		typed.SetGroupVersionKind(gvk)
		typed.SetNamespace("default")
		typed.SetName("shared-portal")
		Expect(unstructured.SetNestedMap(typed.Object, map[string]any{"team": "docs"}, "spec")).To(Succeed())
		Eventually(func() error {
			return k8sClient.Create(ctx, typed)
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, typed) })

		rec := &TypedCompositeReconciler{Client: k8sClient, Scheme: kscheme.Scheme, GVK: gvk, Definition: "portal-def"}
		key := types.NamespacedName{Namespace: "default", Name: "shared-portal"}
		_, err = rec.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(MatchError(ContainSubstring("not controlled by")))

		fresh := &dov1alpha1.DoComposite{}
		Expect(k8sClient.Get(ctx, key, fresh)).To(Succeed())
		Expect(fresh.Spec.Definition).To(Equal("other-def"))
		Expect(fresh.OwnerReferences).To(BeEmpty())
	})
})
