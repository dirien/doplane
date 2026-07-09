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
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
	"github.com/dirien/doplane/internal/runnerops"
)

var _ = Describe("DoResource replacement safety", func() {
	const resourceName = "test-replresource"
	const token = "random:index/randomPet:RandomPet"

	ctx := context.Background()
	resourceKey := types.NamespacedName{Namespace: "default", Name: resourceName}

	var runner *fakeRunner
	var reconciler *DoResourceReconciler

	replacementErr := &pulumido.CodedError{Code: runnerops.CodeReplacementRequired, Message: "prefix is immutable"}
	conflictErr := &pulumido.CodedError{Code: runnerops.CodeAlreadyExists, Message: "entity already exists"}

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

	// createAndDrift provisions the resource, then makes the next update
	// require replacement (spec bump + patch failing as immutable).
	createAndDrift := func(protect *bool) *dov1alpha1.DoResource {
		res := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: resourceName},
			Spec: dov1alpha1.DoResourceSpec{
				Type:       token,
				Protect:    protect,
				Properties: &apiextensionsv1.JSON{Raw: []byte(`{"prefix": "one"}`)},
			},
		}
		Expect(k8sClient.Create(ctx, res)).To(Succeed())
		for range 2 {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(res.Status.ID).To(Equal("fake-id-1"))

		res.Spec.Properties = &apiextensionsv1.JSON{Raw: []byte(`{"prefix": "two"}`)}
		Expect(k8sClient.Update(ctx, res)).To(Succeed())
		runner.patchErr = replacementErr
		return res
	}

	reconcileOnce := func() {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey})
		Expect(err).NotTo(HaveOccurred())
	}

	It("protected resources stop with ReplacementRequired until approved", func() {
		res := createAndDrift(nil) // unset behaves protected

		reconcileOnce()
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		cond := meta.FindStatusCondition(res.Status.Conditions, dov1alpha1.ConditionSynced)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("ReplacementRequired"))
		Expect(runner.deleted).To(BeEmpty())
		Expect(runner.created).To(HaveLen(1), "no replacement without approval")

		// Approving this generation authorizes exactly one replacement.
		res.Annotations = map[string]string{annApproveReplacement: strconv.FormatInt(res.Generation, 10)}
		Expect(k8sClient.Update(ctx, res)).To(Succeed())
		reconcileOnce()

		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(res.Status.ID).To(Equal("fake-id-2"))
		Expect(runner.deleted).To(ContainElement("fake-id-1"), "old external resource removed after the swap")
		Expect(meta.IsStatusConditionTrue(res.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
	})

	It("explicitly unprotected resources replace automatically", func() {
		createAndDrift(ptr.To(false))

		reconcileOnce()
		res := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(res.Status.ID).To(Equal("fake-id-2"))
		Expect(runner.deleted).To(ContainElement("fake-id-1"))
	})

	It("falls back to delete-before-create on identity conflicts", func() {
		createAndDrift(ptr.To(false))
		runner.createErrs = []error{conflictErr} // first replacement create collides

		reconcileOnce()
		res := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(res.Status.ID).To(Equal("fake-id-2"))
		Expect(runner.deleted).To(ContainElement("fake-id-1"), "old resource deleted before the retry create")
	})

	It("stays terminal on the replacement path once a secret embeds in the id", func() {
		createAndDrift(ptr.To(false)) // unprotected → would replace automatically
		secretInIDErr := &pulumido.CodedError{Code: runnerops.CodeSecretInputInID, Message: "id embeds a secret"}
		runner.createErrs = []error{secretInIDErr}

		// First replacement: the create runs, the provider-assigned id embeds
		// a secret → terminal SecretInputInID; the original is left in place.
		reconcileOnce()
		res := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(meta.FindStatusCondition(res.Status.Conditions, dov1alpha1.ConditionSynced).Reason).To(Equal("SecretInputInID"))
		Expect(res.Status.ID).To(Equal("fake-id-1"))
		Expect(runner.created).To(HaveLen(1), "the failed create is not a recorded success")

		// A generation-independent re-enqueue (createErrs now drained, so a
		// re-create would SUCCEED) must not run another Create and orphan a
		// second resource — the guard keeps it terminal, like the create path.
		reconcileOnce()
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		Expect(res.Status.ID).To(Equal("fake-id-1"))
		Expect(runner.created).To(HaveLen(1), "no second replacement Create after SecretInputInID")
		Expect(runner.deleted).To(BeEmpty(), "the original must not be deleted")
	})
})
