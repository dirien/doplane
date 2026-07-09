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
	"encoding/json"

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

var _ = Describe("DoResource reference graph", func() {
	const ns = "default"
	const token = "random:index/randomPet:RandomPet"
	ctx := context.Background()

	var runner *fakeRunner
	var reconciler *DoResourceReconciler

	nn := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: ns, Name: name}
	}
	reconcileName := func(name string) error {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn(name)})
		return err
	}
	makeRes := func(name string, props map[string]any, refs []dov1alpha1.Reference) *dov1alpha1.DoResource {
		raw, err := json.Marshal(props)
		Expect(err).NotTo(HaveOccurred())
		return &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: dov1alpha1.DoResourceSpec{
				Type:       token,
				Properties: &apiextensionsv1.JSON{Raw: raw},
				References: refs,
			},
		}
	}
	cleanup := func(names ...string) {
		for range 5 { // teardown ordering may need several passes
			remaining := false
			for _, name := range names {
				res := &dov1alpha1.DoResource{}
				err := k8sClient.Get(ctx, nn(name), res)
				if errors.IsNotFound(err) {
					continue
				}
				Expect(err).NotTo(HaveOccurred())
				remaining = true
				if res.DeletionTimestamp.IsZero() {
					Expect(k8sClient.Delete(ctx, res)).To(Succeed())
				}
				_ = reconcileName(name)
			}
			if !remaining {
				return
			}
		}
	}

	BeforeEach(func() {
		runner = &fakeRunner{}
		reconciler = &DoResourceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(64),
			Runner:   runner,
			Schemas:  pulumido.NewSchemaCache(runner),
		}
	})

	It("wakes referenced sources only while terminating, so volatile outputs cannot drift-loop", func() {
		defer cleanup("loop-b", "loop-a")

		refs := []dov1alpha1.Reference{{
			ToPath: "prefix",
			From:   dov1alpha1.ReferenceSource{Name: "loop-b", FieldPath: "status.id"},
		}}
		Expect(k8sClient.Create(ctx, makeRes("loop-b", map[string]any{"length": 2}, nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, makeRes("loop-a", map[string]any{"length": 2}, refs))).To(Succeed())

		names := func(reqs []reconcile.Request) []string {
			out := make([]string, 0, len(reqs))
			for _, r := range reqs {
				out = append(out, r.Name)
			}
			return out
		}

		a := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn("loop-a"), a)).To(Succeed())
		// A settled resource's status churn must NOT wake the source it
		// references — that source leg is what let two volatile-output
		// resources ping-pong drift-read Jobs endlessly.
		Expect(names(reconciler.mapGraphNeighbors(ctx, a))).NotTo(ContainElement("loop-b"))

		// While terminating, the source leg fires so the referenced source's
		// teardown can unblock.
		now := metav1.Now()
		a.DeletionTimestamp = &now
		Expect(names(reconciler.mapGraphNeighbors(ctx, a))).To(ContainElement("loop-b"))

		// The dependent leg is unconditional: a source always wakes the
		// resources referencing it (value propagation / readiness gating).
		b := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn("loop-b"), b)).To(Succeed())
		Expect(names(reconciler.mapGraphNeighbors(ctx, b))).To(ContainElement("loop-a"))
	})

	It("gates on dependencies, resolves and propagates values, and blocks teardown", func() {
		defer cleanup("pet-b", "pet-a")

		By("creating the dependent before its dependency")
		refs := []dov1alpha1.Reference{{
			ToPath:   "prefix",
			From:     dov1alpha1.ReferenceSource{Name: "pet-a", FieldPath: "status.id"},
			Template: "from-${value}",
		}}
		Expect(k8sClient.Create(ctx, makeRes("pet-b", map[string]any{"length": 2}, refs))).To(Succeed())
		Expect(reconcileName("pet-b")).To(Succeed()) // finalizer
		Expect(reconcileName("pet-b")).To(Succeed()) // waits

		b := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn("pet-b"), b)).To(Succeed())
		ready := meta.FindStatusCondition(b.Status.Conditions, dov1alpha1.ConditionReady)
		Expect(ready).NotTo(BeNil())
		Expect(ready.Reason).To(Equal("WaitingForDependency"))
		Expect(runner.created).To(BeEmpty())

		By("creating the dependency; the dependent then resolves")
		Expect(k8sClient.Create(ctx, makeRes("pet-a", map[string]any{"length": 3}, nil))).To(Succeed())
		Expect(reconcileName("pet-a")).To(Succeed())
		Expect(reconcileName("pet-a")).To(Succeed())
		Expect(reconcileName("pet-b")).To(Succeed())

		Expect(k8sClient.Get(ctx, nn("pet-b"), b)).To(Succeed())
		Expect(b.Status.ID).NotTo(BeEmpty())
		Expect(meta.IsStatusConditionTrue(b.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
		var appliedPrefix any
		for _, state := range runner.created {
			if p, ok := state["prefix"]; ok {
				appliedPrefix = p
			}
		}
		Expect(appliedPrefix).To(Equal("from-fake-id-1"))

		By("propagating an upstream output change with a patch")
		a := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn("pet-a"), a)).To(Succeed())
		a.Status.ID = "fake-id-CHANGED"
		Expect(k8sClient.Status().Update(ctx, a)).To(Succeed())
		Expect(reconcileName("pet-b")).To(Succeed())
		Expect(runner.patched).To(HaveLen(1))
		Expect(runner.patched[0]["prefix"]).To(Equal("from-fake-id-CHANGED"))

		By("blocking dependency deletion while the dependent exists")
		Expect(k8sClient.Get(ctx, nn("pet-a"), a)).To(Succeed())
		Expect(k8sClient.Delete(ctx, a)).To(Succeed())
		Expect(reconcileName("pet-a")).To(Succeed())
		Expect(k8sClient.Get(ctx, nn("pet-a"), a)).To(Succeed()) // still there
		ready = meta.FindStatusCondition(a.Status.Conditions, dov1alpha1.ConditionReady)
		Expect(ready.Reason).To(Equal("BlockedByDependents"))
		Expect(runner.deleted).To(BeEmpty())

		By("tearing down in reverse order once the dependent is gone")
		Expect(k8sClient.Get(ctx, nn("pet-b"), b)).To(Succeed())
		Expect(k8sClient.Delete(ctx, b)).To(Succeed())
		Expect(reconcileName("pet-b")).To(Succeed())
		Expect(errors.IsNotFound(k8sClient.Get(ctx, nn("pet-b"), b))).To(BeTrue())
		Expect(reconcileName("pet-a")).To(Succeed())
		Expect(errors.IsNotFound(k8sClient.Get(ctx, nn("pet-a"), a))).To(BeTrue())
		Expect(runner.deleted).To(HaveLen(2))
	})

	It("tears down mutually-referencing resources without deadlocking", func() {
		defer cleanup("mut-a", "mut-b")

		By("creating two independent resources with external state")
		Expect(k8sClient.Create(ctx, makeRes("mut-a", map[string]any{"length": 2}, nil))).To(Succeed())
		Expect(k8sClient.Create(ctx, makeRes("mut-b", map[string]any{"length": 2}, nil))).To(Succeed())
		for _, n := range []string{"mut-a", "mut-b"} {
			Expect(reconcileName(n)).To(Succeed())
			Expect(reconcileName(n)).To(Succeed())
		}

		By("wiring them into a reference cycle after the fact")
		for _, pair := range [][2]string{{"mut-a", "mut-b"}, {"mut-b", "mut-a"}} {
			res := &dov1alpha1.DoResource{}
			Expect(k8sClient.Get(ctx, nn(pair[0]), res)).To(Succeed())
			res.Spec.References = []dov1alpha1.Reference{{
				ToPath: "prefix",
				From:   dov1alpha1.ReferenceSource{Name: pair[1], FieldPath: "status.id"},
			}}
			Expect(k8sClient.Update(ctx, res)).To(Succeed())
		}

		By("deleting both; the name tie-break lets exactly one side yield")
		for _, n := range []string{"mut-a", "mut-b"} {
			res := &dov1alpha1.DoResource{}
			Expect(k8sClient.Get(ctx, nn(n), res)).To(Succeed())
			Expect(k8sClient.Delete(ctx, res)).To(Succeed())
		}
		for range 4 {
			_ = reconcileName("mut-a")
			_ = reconcileName("mut-b")
		}
		a, b := &dov1alpha1.DoResource{}, &dov1alpha1.DoResource{}
		Expect(errors.IsNotFound(k8sClient.Get(ctx, nn("mut-a"), a))).To(BeTrue(), "mut-a must not wedge in Terminating")
		Expect(errors.IsNotFound(k8sClient.Get(ctx, nn("mut-b"), b))).To(BeTrue(), "mut-b must not wedge in Terminating")
		Expect(runner.deleted).To(HaveLen(2), "both external resources torn down")
	})

	It("detects reference cycles", func() {
		defer cleanup("cyc-a", "cyc-b")

		refAB := []dov1alpha1.Reference{{ToPath: "prefix", From: dov1alpha1.ReferenceSource{Name: "cyc-b", FieldPath: "status.id"}}}
		refBA := []dov1alpha1.Reference{{ToPath: "prefix", From: dov1alpha1.ReferenceSource{Name: "cyc-a", FieldPath: "status.id"}}}
		Expect(k8sClient.Create(ctx, makeRes("cyc-a", map[string]any{}, refAB))).To(Succeed())
		Expect(k8sClient.Create(ctx, makeRes("cyc-b", map[string]any{}, refBA))).To(Succeed())
		Expect(reconcileName("cyc-a")).To(Succeed())
		Expect(reconcileName("cyc-a")).To(Succeed())

		a := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn("cyc-a"), a)).To(Succeed())
		synced := meta.FindStatusCondition(a.Status.Conditions, dov1alpha1.ConditionSynced)
		Expect(synced).NotTo(BeNil())
		Expect(synced.Reason).To(Equal("CyclicReference"))
	})
})
