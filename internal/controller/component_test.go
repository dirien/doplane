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

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

var _ = Describe("Component resources (ephemeral engine)", func() {
	const ns = "default"
	const componentType = "ai-model:index:AIModelComponent"
	ctx := context.Background()
	nn := types.NamespacedName{Namespace: ns, Name: "comp-test"}

	var runner *fakeRunner
	var reconciler *DoResourceReconciler

	reconcileIt := func() error {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		return err
	}

	BeforeEach(func() {
		runner = &fakeRunner{componentMode: true}
		reconciler = &DoResourceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(64),
			Runner:   runner,
			Schemas:  pulumido.NewSchemaCache(runner),
		}
	})

	It("constructs, updates and destroys a component through the engine", func() {
		res := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Name: nn.Name, Namespace: ns},
			Spec: dov1alpha1.DoResourceSpec{
				Type:       componentType,
				Package:    "private/ediri/ai-model@0.4.0",
				Properties: &apiextensionsv1.JSON{Raw: []byte(`{"length": 1}`)},
			},
		}
		Expect(k8sClient.Create(ctx, res)).To(Succeed())
		DeferCleanup(func() {
			got := &dov1alpha1.DoResource{}
			if err := k8sClient.Get(ctx, nn, got); errors.IsNotFound(err) {
				return
			}
			_ = k8sClient.Delete(ctx, got)
			_ = reconcileIt()
		})

		By("constructing via the engine")
		Expect(reconcileIt()).To(Succeed()) // finalizer
		Expect(reconcileIt()).To(Succeed()) // create
		got := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, nn, got)).To(Succeed())
		Expect(got.Status.ID).To(HavePrefix("urn:pulumi:"))
		Expect(got.Status.EngineState).NotTo(BeNil())
		Expect(meta.IsStatusConditionTrue(got.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
		Expect(runner.componentCreates).To(HaveLen(1))
		Expect(runner.created).To(BeEmpty(), "components must never hit the stateless create path")

		By("updating through the engine on spec change")
		got.Spec.Properties = &apiextensionsv1.JSON{Raw: []byte(`{"length": 2}`)}
		Expect(k8sClient.Update(ctx, got)).To(Succeed())
		Expect(reconcileIt()).To(Succeed())
		Expect(k8sClient.Get(ctx, nn, got)).To(Succeed())
		Expect(runner.componentUpdates).To(HaveLen(1))
		Expect(string(got.Status.EngineState.Raw)).To(ContainSubstring("updated"))

		By("settled reconcile performs no engine work")
		Expect(reconcileIt()).To(Succeed())
		Expect(runner.componentCreates).To(HaveLen(1))
		Expect(runner.componentUpdates).To(HaveLen(1))

		By("destroying from the persisted checkpoint on delete")
		Expect(k8sClient.Delete(ctx, got)).To(Succeed())
		Expect(reconcileIt()).To(Succeed())
		Expect(errors.IsNotFound(k8sClient.Get(ctx, nn, got))).To(BeTrue())
		Expect(runner.componentDeletes).To(HaveLen(1))
		Expect(string(runner.componentDeletes[0])).To(ContainSubstring("updated"), "delete must use the latest checkpoint")
		Expect(runner.deleted).To(BeEmpty(), "components must never hit the stateless delete path")
	})
})
