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

var _ = Describe("DoUsage deletion protection", func() {
	const resourceName = "test-useresource"
	const usageName = "test-usage"
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
		usage := &dov1alpha1.DoUsage{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: usageName}}
		_ = k8sClient.Delete(ctx, usage)
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
	})

	It("blocks external teardown while a usage exists, unblocks when it goes", func() {
		res := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: resourceName},
			Spec: dov1alpha1.DoResourceSpec{
				Type:       token,
				Properties: &apiextensionsv1.JSON{Raw: []byte(`{"length": 3}`)},
			},
		}
		Expect(k8sClient.Create(ctx, res)).To(Succeed())
		for range 2 {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
			Expect(err).NotTo(HaveOccurred())
		}

		Expect(k8sClient.Create(ctx, &dov1alpha1.DoUsage{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: usageName},
			Spec: dov1alpha1.DoUsageSpec{
				Of:     dov1alpha1.UsageTarget{Name: resourceName},
				Reason: "database is used by the payments namespace",
			},
		})).To(Succeed())

		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(k8sClient.Delete(ctx, res)).To(Succeed())
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
		Expect(err).NotTo(HaveOccurred())

		// Still present, external resource untouched, reason surfaced.
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(runner.deleted).To(BeEmpty())
		cond := meta.FindStatusCondition(res.Status.Conditions, dov1alpha1.ConditionReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("BlockedByDependents"))
		Expect(cond.Message).To(ContainSubstring("payments namespace"))

		// Removing the usage releases the teardown.
		usage := &dov1alpha1.DoUsage{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: usageName}}
		Expect(k8sClient.Delete(ctx, usage)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
		Expect(err).NotTo(HaveOccurred())
		Expect(runner.deleted).To(ContainElement("fake-id-1"))
		Expect(errors.IsNotFound(k8sClient.Get(ctx, resourceKey, res))).To(BeTrue())
	})
})
