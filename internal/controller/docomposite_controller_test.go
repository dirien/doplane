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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/pulumi-do-operator/api/v1alpha1"
)

var _ = Describe("DoComposite Controller", func() {
	const ns = "default"
	ctx := context.Background()

	var reconciler *DoCompositeReconciler

	nn := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: ns, Name: name}
	}
	reconcileComp := func(name string) error {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
		return err
	}
	simpleDefinition := func(name, resourceName string) *dov1alpha1.DoCompositeDefinition {
		return &dov1alpha1.DoCompositeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: dov1alpha1.DoCompositeDefinitionSpec{
				Resources: []dov1alpha1.CompositeResourceTemplate{{
					Name:       resourceName,
					Type:       "random:index/randomPet:RandomPet",
					Properties: &apiextensionsv1.JSON{Raw: []byte(`{"length": 2}`)},
				}},
			},
		}
	}

	BeforeEach(func() {
		reconciler = &DoCompositeReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(64),
		}
	})

	It("clears Ready when a previously ready composite fails to reconcile", func() {
		def := simpleDefinition("cc-def", "pet")
		Expect(k8sClient.Create(ctx, def)).To(Succeed())
		comp := &dov1alpha1.DoComposite{
			ObjectMeta: metav1.ObjectMeta{Name: "cc-comp", Namespace: ns},
			Spec:       dov1alpha1.DoCompositeSpec{Definition: "cc-def"},
		}
		Expect(k8sClient.Create(ctx, comp)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, comp)
			child := &dov1alpha1.DoResource{ObjectMeta: metav1.ObjectMeta{Name: "cc-comp-pet", Namespace: ns}}
			_ = k8sClient.Delete(ctx, child)
		})

		Expect(reconcileComp("cc-comp")).To(Succeed())

		By("marking the child ready so the composite becomes Ready")
		child := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn("cc-comp-pet"), child)).To(Succeed())
		meta.SetStatusCondition(&child.Status.Conditions, metav1.Condition{
			Type: dov1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Available",
		})
		Expect(k8sClient.Status().Update(ctx, child)).To(Succeed())
		Expect(reconcileComp("cc-comp")).To(Succeed())
		Expect(k8sClient.Get(ctx, nn("cc-comp"), comp)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(comp.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())

		By("deleting the definition and reconciling again")
		Expect(k8sClient.Delete(ctx, def)).To(Succeed())
		Expect(reconcileComp("cc-comp")).To(Succeed())
		Expect(k8sClient.Get(ctx, nn("cc-comp"), comp)).To(Succeed())
		synced := meta.FindStatusCondition(comp.Status.Conditions, dov1alpha1.ConditionSynced)
		Expect(synced.Reason).To(Equal("DefinitionNotFound"))
		ready := meta.FindStatusCondition(comp.Status.Conditions, dov1alpha1.ConditionReady)
		Expect(ready.Status).To(Equal(metav1.ConditionFalse), "stale Ready=True must be cleared on failure")
	})

	It("replaces a child when its immutable type changes in the definition", func() {
		Expect(k8sClient.Create(ctx, simpleDefinition("rp-def", "res"))).To(Succeed())
		comp := &dov1alpha1.DoComposite{
			ObjectMeta: metav1.ObjectMeta{Name: "rp-comp", Namespace: ns},
			Spec:       dov1alpha1.DoCompositeSpec{Definition: "rp-def"},
		}
		Expect(k8sClient.Create(ctx, comp)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, comp)
			child := &dov1alpha1.DoResource{ObjectMeta: metav1.ObjectMeta{Name: "rp-comp-res", Namespace: ns}}
			_ = k8sClient.Delete(ctx, child)
			def := &dov1alpha1.DoCompositeDefinition{ObjectMeta: metav1.ObjectMeta{Name: "rp-def"}}
			_ = k8sClient.Delete(ctx, def)
		})

		Expect(reconcileComp("rp-comp")).To(Succeed())
		child := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn("rp-comp-res"), child)).To(Succeed())
		oldUID := child.UID
		Expect(child.Spec.Type).To(Equal("random:index/randomPet:RandomPet"))

		By("changing the template's type under the same name")
		def := &dov1alpha1.DoCompositeDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rp-def"}, def)).To(Succeed())
		def.Spec.Resources[0].Type = "random:index/randomString:RandomString"
		Expect(k8sClient.Update(ctx, def)).To(Succeed())

		By("first pass deletes the old child and reports ReplacingChildren")
		Expect(reconcileComp("rp-comp")).To(Succeed())
		Expect(k8sClient.Get(ctx, nn("rp-comp"), comp)).To(Succeed())
		ready := meta.FindStatusCondition(comp.Status.Conditions, dov1alpha1.ConditionReady)
		Expect(ready.Reason).To(Equal("ReplacingChildren"))

		By("second pass recreates the child with the new type")
		Expect(reconcileComp("rp-comp")).To(Succeed())
		Expect(k8sClient.Get(ctx, nn("rp-comp-res"), child)).To(Succeed())
		Expect(child.Spec.Type).To(Equal("random:index/randomString:RandomString"))
		Expect(child.UID).NotTo(Equal(oldUID), "child must be a new object, not an in-place update")
	})

	It("never prunes labeled resources it does not own", func() {
		Expect(k8sClient.Create(ctx, simpleDefinition("pr-def", "pet"))).To(Succeed())
		comp := &dov1alpha1.DoComposite{
			ObjectMeta: metav1.ObjectMeta{Name: "pr-comp", Namespace: ns},
			Spec:       dov1alpha1.DoCompositeSpec{Definition: "pr-def"},
		}
		Expect(k8sClient.Create(ctx, comp)).To(Succeed())

		By("planting a decoy with the composite label but no owner reference")
		decoy := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pr-decoy",
				Namespace: ns,
				Labels:    map[string]string{labelComposite: compositeLabelValue("pr-comp")},
			},
			Spec: dov1alpha1.DoResourceSpec{Type: "random:index/randomPet:RandomPet"},
		}
		Expect(k8sClient.Create(ctx, decoy)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, comp)
			for _, n := range []string{"pr-decoy", "pr-comp-pet", "pr-comp-cat"} {
				child := &dov1alpha1.DoResource{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: ns}}
				_ = k8sClient.Delete(ctx, child)
			}
			def := &dov1alpha1.DoCompositeDefinition{ObjectMeta: metav1.ObjectMeta{Name: "pr-def"}}
			_ = k8sClient.Delete(ctx, def)
		})

		Expect(reconcileComp("pr-comp")).To(Succeed())
		Expect(k8sClient.Get(ctx, nn("pr-decoy"), decoy)).To(Succeed(), "decoy must survive reconcile")

		By("renaming the template resource so the old owned child becomes prunable")
		def := &dov1alpha1.DoCompositeDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "pr-def"}, def)).To(Succeed())
		def.Spec.Resources[0].Name = "cat"
		Expect(k8sClient.Update(ctx, def)).To(Succeed())
		Expect(reconcileComp("pr-comp")).To(Succeed())

		oldChild := &dov1alpha1.DoResource{}
		err := k8sClient.Get(ctx, nn("pr-comp-pet"), oldChild)
		Expect(errors.IsNotFound(err) || !oldChild.DeletionTimestamp.IsZero()).To(BeTrue(),
			"owned stale child must be pruned")
		Expect(k8sClient.Get(ctx, nn("pr-comp-cat"), &dov1alpha1.DoResource{})).To(Succeed())
		Expect(k8sClient.Get(ctx, nn("pr-decoy"), decoy)).To(Succeed(), "decoy must still survive pruning")
		Expect(decoy.DeletionTimestamp.IsZero()).To(BeTrue())
	})
})
