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
	"time"

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

var _ = Describe("DoResource lifecycle annotations", func() {
	const resourceName = "test-annresource"
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

	AfterEach(func() {
		res := &dov1alpha1.DoResource{}
		if err := k8sClient.Get(ctx, resourceKey, res); err == nil {
			if res.Annotations[annPaused] == "true" {
				delete(res.Annotations, annPaused)
				Expect(k8sClient.Update(ctx, res)).To(Succeed())
			}
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
	})

	createResource := func(annotations map[string]string) {
		res := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: resourceName, Annotations: annotations},
			Spec: dov1alpha1.DoResourceSpec{
				Type:       token,
				Properties: &apiextensionsv1.JSON{Raw: []byte(`{"length": 3}`)},
			},
		}
		Expect(k8sClient.Create(ctx, res)).To(Succeed())
	}

	reconcileN := func(n int) {
		for range n {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
			Expect(err).NotTo(HaveOccurred())
		}
	}

	It("paused stops all cloud calls until the annotation is removed", func() {
		createResource(map[string]string{annPaused: "true"})

		reconcileN(2)
		Expect(runner.created).To(BeEmpty(), "no cloud call while paused")
		res := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		cond := meta.FindStatusCondition(res.Status.Conditions, dov1alpha1.ConditionSynced)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("ReconcilePaused"))

		delete(res.Annotations, annPaused)
		Expect(k8sClient.Update(ctx, res)).To(Succeed())
		reconcileN(2)
		Expect(runner.created).To(HaveLen(1), "resumes after unpausing")
	})

	It("adopts an annotated external resource instead of creating", func() {
		createResource(map[string]string{annExternalName: "pre-existing-pet"})

		reconcileN(2)

		Expect(runner.created).To(BeEmpty(), "adoption must not create")
		res := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(res.Status.ID).To(Equal("pre-existing-pet"))
		Expect(meta.IsStatusConditionTrue(res.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
	})

	It("persists the external name annotation on create (crash-window bookkeeping)", func() {
		createResource(nil)

		reconcileN(2)

		res := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(res.Annotations[annExternalName]).To(Equal("fake-id-1"))
		Expect(res.Status.ID).To(Equal("fake-id-1"))
	})

	It("poll-interval overrides the drift-read requeue", func() {
		createResource(map[string]string{annPollInterval: "42m"})

		var result reconcile.Result
		for range 2 {
			var err error
			result, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(result.RequeueAfter).To(Equal(42 * time.Minute))
	})
})
