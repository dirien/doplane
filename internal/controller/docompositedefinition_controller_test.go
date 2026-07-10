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
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

func TestCheckTemplateParams(t *testing.T) {
	spec := func(params *apiextensionsv1.JSONSchemaProps, props map[string]any, externalName string) *dov1alpha1.DoCompositeDefinitionSpec {
		var properties *apiextensionsv1.JSON
		if props != nil {
			properties = jsonRaw(t, props)
		}
		return &dov1alpha1.DoCompositeDefinitionSpec{
			API: &dov1alpha1.CompositeAPI{Kind: "Website", ParametersSchema: params},
			Resources: []dov1alpha1.CompositeResourceTemplate{{
				Name: "bucket", Type: "t:m:R",
				Properties:   properties,
				ExternalName: externalName,
			}},
		}
	}
	envOnly := &apiextensionsv1.JSONSchemaProps{
		Type:       "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{"env": {Type: "string"}},
	}

	if err := checkTemplateParams(spec(envOnly, map[string]any{"bucket": "${params.env}"}, "")); err != nil {
		t.Errorf("declared parameter must pass: %v", err)
	}
	if err := checkTemplateParams(spec(envOnly, map[string]any{"bucket": "${params.gone}"}, "")); err == nil ||
		!strings.Contains(err.Error(), "gone") {
		t.Errorf("undeclared parameter must fail naming it, got %v", err)
	}
	if err := checkTemplateParams(spec(envOnly, nil, "${params.legacy}")); err == nil {
		t.Error("externalName expressions are checked too")
	}
	if err := checkTemplateParams(spec(envOnly, map[string]any{"bucket": "$${params.gone}"}, "")); err != nil {
		t.Errorf("escaped expressions are literals, not references: %v", err)
	}
	if err := checkTemplateParams(spec(nil, map[string]any{"bucket": "${params.anything}"}, "")); err != nil {
		t.Errorf("no schema means nothing to check: %v", err)
	}
	open := envOnly.DeepCopy()
	open.XPreserveUnknownFields = ptr.To(true)
	if err := checkTemplateParams(spec(open, map[string]any{"bucket": "${params.extra}"}, "")); err != nil {
		t.Errorf("schemas preserving unknown fields accept any parameter: %v", err)
	}
}

func TestTypedCompositeCRDShape(t *testing.T) {
	api := &dov1alpha1.CompositeAPI{
		Group:              "platform.acme.com",
		Kind:               "Website",
		Version:            "v1",
		DeprecatedVersions: []string{"v1alpha1"},
		ParametersSchema: &apiextensionsv1.JSONSchemaProps{
			Type:       "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{"env": {Type: "string"}},
		},
	}
	crd, err := typedCompositeCRD("site-def", api)
	if err != nil {
		t.Fatal(err)
	}
	if crd.Name != "websites.platform.acme.com" || crd.Spec.Group != "platform.acme.com" {
		t.Errorf("platform group must shape the CRD: %s / %s", crd.Name, crd.Spec.Group)
	}
	if crd.Annotations[annTypedOwner] != "composite:site-def" {
		t.Errorf("owner annotation: %v", crd.Annotations)
	}
	if len(crd.Spec.Versions) != 2 {
		t.Fatalf("want current + deprecated version, got %v", crd.Spec.Versions)
	}
	current, deprecated := crd.Spec.Versions[0], crd.Spec.Versions[1]
	if current.Name != "v1" || !current.Storage || current.Deprecated {
		t.Errorf("current version: %+v", current)
	}
	if deprecated.Name != "v1alpha1" || deprecated.Storage || !deprecated.Deprecated || !deprecated.Served {
		t.Errorf("deprecated version: %+v", deprecated)
	}
	spec := current.Schema.OpenAPIV3Schema.Properties["spec"]
	if _, ok := spec.Properties[doplaneReservedProperty]; !ok {
		t.Error("the reserved doplane block must be injected into the schema")
	}
	if _, ok := spec.Properties["env"]; !ok {
		t.Error("platform parameters stay flat at the top level of spec")
	}

	reserved := api.DeepCopy()
	reserved.ParametersSchema.Properties[doplaneReservedProperty] = apiextensionsv1.JSONSchemaProps{Type: "string"}
	if _, err := typedCompositeCRD("site-def", reserved); err == nil {
		t.Error("a parameter named doplane must be rejected")
	}

	overlap := api.DeepCopy()
	overlap.DeprecatedVersions = []string{"v1"}
	if _, err := typedCompositeCRD("site-def", overlap); err == nil {
		t.Error("the current version cannot also be deprecated")
	}
}

func TestPopDoplaneBlock(t *testing.T) {
	params := map[string]any{
		"env": "prod",
		"doplane": map[string]any{
			"updatePolicy": "Manual",
			"revisionRef":  map[string]any{"name": "site-def-v3"},
		},
	}
	policy, ref := popDoplaneBlock(params)
	if policy != dov1alpha1.UpdateManual {
		t.Errorf("updatePolicy: %q", policy)
	}
	if ref == nil || ref.Name != "site-def-v3" {
		t.Errorf("revisionRef: %+v", ref)
	}
	if _, still := params["doplane"]; still {
		t.Error("the doplane block must not leak into composite parameters")
	}
	if params["env"] != "prod" {
		t.Error("platform parameters stay untouched")
	}

	policy, ref = popDoplaneBlock(map[string]any{"env": "prod"})
	if policy != "" || ref != nil {
		t.Errorf("absent block: %q %+v", policy, ref)
	}
}

var _ = Describe("DoCompositeDefinition typed platform APIs", func() {
	newReconciler := func(groups ...string) *DoCompositeDefinitionReconciler {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:  kscheme.Scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		Expect(err).NotTo(HaveOccurred())
		return &DoCompositeDefinitionReconciler{
			Client:        k8sClient,
			Live:          k8sClient,
			Scheme:        kscheme.Scheme,
			Recorder:      record.NewFakeRecorder(32),
			Typed:         &TypedRegistrar{Manager: mgr},
			AllowedGroups: groups,
		}
	}
	apiServed := func(name string) *metav1.Condition {
		def := &dov1alpha1.DoCompositeDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, def)).To(Succeed())
		return meta.FindStatusCondition(def.Status.Conditions, condAPIServed)
	}

	It("refuses to serve a group missing from the allowlist", func() {
		def := &dov1alpha1.DoCompositeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "rogue-def"},
			Spec: dov1alpha1.DoCompositeDefinitionSpec{
				Resources: []dov1alpha1.CompositeResourceTemplate{{Name: "a", Type: "t:m:R"}},
				API:       &dov1alpha1.CompositeAPI{Group: "platform.acme.com", Kind: "Rogue"},
			},
		}
		Expect(k8sClient.Create(ctx, def)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, def) })

		rec := newReconciler() // no extra groups allowed
		_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: def.Name}})
		Expect(err).NotTo(HaveOccurred())

		cond := apiServed(def.Name)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("GroupNotAllowed"))

		crd := &apiextensionsv1.CustomResourceDefinition{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "rogues.platform.acme.com"}, crd)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "no CRD may be created for a disallowed group")
	})

	It("rejects a parameters schema declaring the reserved doplane property", func() {
		def := &dov1alpha1.DoCompositeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "reserved-def"},
			Spec: dov1alpha1.DoCompositeDefinitionSpec{
				Resources: []dov1alpha1.CompositeResourceTemplate{{Name: "a", Type: "t:m:R"}},
				API: &dov1alpha1.CompositeAPI{
					Kind: "Reserved",
					ParametersSchema: &apiextensionsv1.JSONSchemaProps{
						Type:       "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{"doplane": {Type: "string"}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, def)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, def) })

		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: def.Name}})
		Expect(err).NotTo(HaveOccurred())
		cond := apiServed(def.Name)
		Expect(cond.Reason).To(Equal("InvalidSchema"))
		Expect(cond.Message).To(ContainSubstring("doplane"))
	})

	It("serves a platform API, tracks versions, and blocks deletion while objects exist", func() {
		group := "platform.example.org"
		def := &dov1alpha1.DoCompositeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "portal-def-typed"},
			Spec: dov1alpha1.DoCompositeDefinitionSpec{
				Resources: []dov1alpha1.CompositeResourceTemplate{{
					Name: "site", Type: "t:m:R",
					Properties: &apiextensionsv1.JSON{Raw: []byte(`{"team":"${params.team}"}`)},
				}},
				API: &dov1alpha1.CompositeAPI{
					Group: group,
					Kind:  "PortalSite",
					ParametersSchema: &apiextensionsv1.JSONSchemaProps{
						Type:       "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{"team": {Type: "string"}},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, def)).To(Succeed())
		rec := newReconciler(group)
		key := reconcile.Request{NamespacedName: types.NamespacedName{Name: def.Name}}
		crdName := "portalsites." + group
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: crdName}})
			latest := &dov1alpha1.DoCompositeDefinition{}
			if err := k8sClient.Get(ctx, key.NamespacedName, latest); err == nil {
				latest.Finalizers = nil
				_ = k8sClient.Update(ctx, latest)
				_ = k8sClient.Delete(ctx, latest)
			}
		})

		_, err := rec.Reconcile(ctx, key)
		Expect(err).NotTo(HaveOccurred())

		By("persisting ownership on the CRD and the finalizer on the definition")
		crd := &apiextensionsv1.CustomResourceDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, crd)).To(Succeed())
		Expect(crd.Annotations[annTypedOwner]).To(Equal("composite:" + def.Name))
		Expect(crd.Labels[labelManagedByKey]).To(Equal("doplane"))
		fresh := &dov1alpha1.DoCompositeDefinition{}
		Expect(k8sClient.Get(ctx, key.NamespacedName, fresh)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(fresh, typedAPIFinalizer)).To(BeTrue())
		Expect(apiServed(def.Name).Status).To(Equal(metav1.ConditionTrue))

		By("counting typed objects per version")
		typed := &unstructured.Unstructured{}
		typed.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: "v1alpha1", Kind: "PortalSite"})
		typed.SetNamespace("default")
		typed.SetName("acme-portal")
		Expect(unstructured.SetNestedMap(typed.Object, map[string]any{"team": "docs"}, "spec")).To(Succeed())
		Eventually(func() error {
			return k8sClient.Create(ctx, typed)
		}, 10*time.Second, 200*time.Millisecond).Should(Succeed())
		Eventually(func() int32 {
			_, _ = rec.Reconcile(ctx, key)
			latest := &dov1alpha1.DoCompositeDefinition{}
			if err := k8sClient.Get(ctx, key.NamespacedName, latest); err != nil {
				return -1
			}
			for _, v := range latest.Status.APIVersions {
				if v.Name == "v1alpha1" {
					return v.Objects
				}
			}
			return -1
		}, 10*time.Second, 200*time.Millisecond).Should(Equal(int32(1)))

		By("refusing to drop a version whose objects still exist")
		Expect(k8sClient.Get(ctx, key.NamespacedName, fresh)).To(Succeed())
		fresh.Spec.API.Version = "v1beta1"
		Expect(k8sClient.Update(ctx, fresh)).To(Succeed(), "bump without deprecating the stored version")
		_, err = rec.Reconcile(ctx, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(apiServed(def.Name).Reason).To(Equal("StoredVersionInUse"))

		By("serving old and new versions side by side during migration")
		Expect(k8sClient.Get(ctx, key.NamespacedName, fresh)).To(Succeed())
		fresh.Spec.API.DeprecatedVersions = []string{"v1alpha1"}
		Expect(k8sClient.Update(ctx, fresh)).To(Succeed())
		_, err = rec.Reconcile(ctx, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, crd)).To(Succeed())
		Expect(crd.Spec.Versions).To(HaveLen(2))
		Expect(apiServed(def.Name).Status).To(Equal(metav1.ConditionTrue))

		By("blocking definition deletion while typed objects exist")
		Expect(k8sClient.Delete(ctx, fresh)).To(Succeed())
		_, err = rec.Reconcile(ctx, key)
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, key.NamespacedName, fresh)).To(Succeed(), "finalizer must hold the definition")
		Expect(apiServed(def.Name).Reason).To(Equal("DeletionBlocked"))
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, crd)).To(Succeed(), "the CRD stays while objects exist")

		By("tearing down the CRD and releasing the finalizer at zero objects")
		Expect(k8sClient.Delete(ctx, typed)).To(Succeed())
		Eventually(func() bool {
			_, err := rec.Reconcile(ctx, key)
			if err != nil {
				return false
			}
			return apierrors.IsNotFound(k8sClient.Get(ctx, key.NamespacedName, &dov1alpha1.DoCompositeDefinition{}))
		}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: crdName}, &apiextensionsv1.CustomResourceDefinition{}))
		}, 10*time.Second, 200*time.Millisecond).Should(BeTrue(), "the generated CRD is deleted with the definition")
	})
})
