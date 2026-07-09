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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
)

var _ = Describe("DoComposite revisions and update policy", func() {
	const ns = "default"
	const defName = "rev-def"

	ctx := context.Background()

	var reconciler *DoCompositeReconciler

	nn := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: ns, Name: name}
	}
	reconcileComp := func(name string) {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
		Expect(err).NotTo(HaveOccurred())
	}
	definition := func(prefix string) *dov1alpha1.DoCompositeDefinition {
		return &dov1alpha1.DoCompositeDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: defName},
			Spec: dov1alpha1.DoCompositeDefinitionSpec{
				Resources: []dov1alpha1.CompositeResourceTemplate{{
					Name:       "pet",
					Type:       "random:index/randomPet:RandomPet",
					Properties: &apiextensionsv1.JSON{Raw: []byte(`{"length": 2, "prefix": "` + prefix + `"}`)},
				}},
			},
		}
	}
	childPrefix := func(comp string) string {
		child := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn(comp+"-pet"), child)).To(Succeed())
		return string(child.Spec.Properties.Raw)
	}

	BeforeEach(func() {
		reconciler = &DoCompositeReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(64),
		}
	})

	AfterEach(func() {
		for _, name := range []string{"rev-auto", "rev-manual"} {
			comp := &dov1alpha1.DoComposite{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
			_ = k8sClient.Delete(ctx, comp)
			child := &dov1alpha1.DoResource{ObjectMeta: metav1.ObjectMeta{Name: name + "-pet", Namespace: ns}}
			child.Finalizers = nil
			_ = k8sClient.Delete(ctx, child)
		}
		def := &dov1alpha1.DoCompositeDefinition{ObjectMeta: metav1.ObjectMeta{Name: defName}}
		_ = k8sClient.Delete(ctx, def)
		revs := &dov1alpha1.DoCompositeDefinitionRevisionList{}
		if err := k8sClient.List(ctx, revs); err == nil {
			for i := range revs.Items {
				_ = k8sClient.Delete(ctx, &revs.Items[i])
			}
		}
	})

	It("definition edits reach Automatic instances only; Manual stays pinned", func() {
		Expect(k8sClient.Create(ctx, definition("one"))).To(Succeed())
		for _, c := range []*dov1alpha1.DoComposite{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "rev-auto", Namespace: ns},
				Spec:       dov1alpha1.DoCompositeSpec{Definition: defName},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "rev-manual", Namespace: ns},
				Spec:       dov1alpha1.DoCompositeSpec{Definition: defName, UpdatePolicy: dov1alpha1.UpdateManual},
			},
		} {
			Expect(k8sClient.Create(ctx, c)).To(Succeed())
		}

		reconcileComp("rev-auto")
		reconcileComp("rev-manual")

		// First render snapshots the definition as revision v1.
		rev := &dov1alpha1.DoCompositeDefinitionRevision{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: defName + "-v1"}, rev)).To(Succeed())
		auto := &dov1alpha1.DoComposite{}
		Expect(k8sClient.Get(ctx, nn("rev-auto"), auto)).To(Succeed())
		Expect(auto.Status.Revision).To(Equal(defName + "-v1"))

		// Edit the definition: a new revision appears; only the Automatic
		// instance re-renders.
		def := &dov1alpha1.DoCompositeDefinition{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: defName}, def)).To(Succeed())
		def.Spec = definition("two").Spec
		Expect(k8sClient.Update(ctx, def)).To(Succeed())

		reconcileComp("rev-auto")
		reconcileComp("rev-manual")

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: defName + "-v2"}, rev)).To(Succeed())
		Expect(k8sClient.Get(ctx, nn("rev-auto"), auto)).To(Succeed())
		Expect(auto.Status.Revision).To(Equal(defName + "-v2"))
		Expect(childPrefix("rev-auto")).To(ContainSubstring(`"two"`))

		manual := &dov1alpha1.DoComposite{}
		Expect(k8sClient.Get(ctx, nn("rev-manual"), manual)).To(Succeed())
		Expect(manual.Status.Revision).To(Equal(defName+"-v1"), "Manual must not follow the edit")
		Expect(childPrefix("rev-manual")).To(ContainSubstring(`"one"`))

		// An explicit revisionRef moves the Manual instance deliberately.
		manual.Spec.RevisionRef = &dov1alpha1.RevisionReference{Name: defName + "-v2"}
		Expect(k8sClient.Update(ctx, manual)).To(Succeed())
		reconcileComp("rev-manual")
		Expect(k8sClient.Get(ctx, nn("rev-manual"), manual)).To(Succeed())
		Expect(manual.Status.Revision).To(Equal(defName + "-v2"))
		Expect(childPrefix("rev-manual")).To(ContainSubstring(`"two"`))
	})

	It("prunes revisions beyond the history limit but never referenced ones", func() {
		Expect(k8sClient.Create(ctx, definition("one"))).To(Succeed())
		comp := &dov1alpha1.DoComposite{
			ObjectMeta: metav1.ObjectMeta{Name: "rev-manual", Namespace: ns},
			Spec:       dov1alpha1.DoCompositeSpec{Definition: defName, UpdatePolicy: dov1alpha1.UpdateManual},
		}
		Expect(k8sClient.Create(ctx, comp)).To(Succeed())
		reconcileComp("rev-manual") // pins the Manual instance to v1

		// Edit the definition well past the history limit.
		def := &dov1alpha1.DoCompositeDefinition{}
		for i := 2; i <= revisionHistoryLimit+3; i++ {
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: defName}, def)).To(Succeed())
			def.Spec = definition(fmt.Sprintf("p%d", i)).Spec
			Expect(k8sClient.Update(ctx, def)).To(Succeed())
			reconcileComp("rev-manual")
		}

		rev := &dov1alpha1.DoCompositeDefinitionRevision{}
		// v1 survives despite being beyond the limit: the Manual composite
		// still renders from it.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: defName + "-v1"}, rev)).To(Succeed())
		// v2 is unreferenced and beyond the limit: pruned.
		err := k8sClient.Get(ctx, types.NamespacedName{Name: defName + "-v2"}, rev)
		Expect(apierrors.IsNotFound(err)).To(BeTrue(), "v2 must be pruned, got: %v", err)
		// The newest revision always survives.
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: fmt.Sprintf("%s-v%d", defName, revisionHistoryLimit+3)}, rev)).To(Succeed())
	})

	It("marks a composite RevisionInvalid when revisionRef pins another definition's revision", func() {
		Expect(k8sClient.Create(ctx, definition("one"))).To(Succeed())

		// A second definition owning its own revision snapshot.
		otherRev := &dov1alpha1.DoCompositeDefinitionRevision{
			ObjectMeta: metav1.ObjectMeta{Name: "other-def-v1"},
			Spec: dov1alpha1.DoCompositeDefinitionRevisionSpec{
				DefinitionName: "other-def",
				Revision:       1,
				Definition:     definition("other").Spec,
			},
		}
		Expect(k8sClient.Create(ctx, otherRev)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, otherRev) })

		// A composite of rev-def mis-pinned to other-def's revision.
		comp := &dov1alpha1.DoComposite{
			ObjectMeta: metav1.ObjectMeta{Name: "mispinned", Namespace: ns},
			Spec: dov1alpha1.DoCompositeSpec{
				Definition:  defName,
				RevisionRef: &dov1alpha1.RevisionReference{Name: "other-def-v1"},
			},
		}
		Expect(k8sClient.Create(ctx, comp)).To(Succeed())
		reconcileComp("mispinned")

		Expect(k8sClient.Get(ctx, nn("mispinned"), comp)).To(Succeed())
		cond := meta.FindStatusCondition(comp.Status.Conditions, dov1alpha1.ConditionSynced)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("RevisionInvalid"), "a permanent misconfiguration must surface as a condition, not hot-loop")
		// Ready is cleared too, so the composite stops advertising availability.
		Expect(meta.IsStatusConditionTrue(comp.Status.Conditions, dov1alpha1.ConditionReady)).To(BeFalse())
	})
})
