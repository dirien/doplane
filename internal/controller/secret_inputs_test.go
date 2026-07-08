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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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

var _ = Describe("DoResource secret input sources", func() {
	const resourceName = "test-valresource"
	const inputRefName = "test-db-auth"
	const token = "random:index/randomPet:RandomPet"

	ctx := context.Background()
	resourceKey := types.NamespacedName{Namespace: "default", Name: resourceName}

	var runner *fakeRunner
	var reconciler *DoResourceReconciler

	BeforeEach(func() {
		runner = &fakeRunner{}
		reconciler = &DoResourceReconciler{
			Client:   k8sClient,
			Live:     k8sClient,
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
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: inputRefName}}
		_ = k8sClient.Delete(ctx, secret)
	})

	createResource := func() {
		res := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: resourceName},
			Spec: dov1alpha1.DoResourceSpec{
				Type:       token,
				Properties: &apiextensionsv1.JSON{Raw: []byte(`{"length": 3}`)},
				ValuesFrom: []dov1alpha1.ValueFrom{{
					ToPath:       "prefix",
					SecretKeyRef: dov1alpha1.SecretKeySelector{Name: inputRefName, Key: "password"},
				}},
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

	It("passes only a placeholder and the mapping — never the value", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: inputRefName},
			Data:       map[string][]byte{"password": []byte("hunter2")},
		})).To(Succeed())
		createResource()

		reconcileN(2)

		// The runner call received the placeholder, not the raw value ...
		Expect(runner.createdProps).To(HaveLen(1))
		Expect(runner.createdProps[0]["prefix"]).To(Equal(secretInputPlaceholder))
		// ... plus the secret input plan for out-of-band injection.
		Expect(runner.secretInputs).To(ConsistOf(pulumido.SecretInput{
			ToPath: "prefix", SecretName: inputRefName, SecretKey: "password",
		}))

		// Nothing on the object carries the raw value: spec is untouched
		// and status holds only what the runner returned.
		updated := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, updated)).To(Succeed())
		Expect(string(updated.Spec.Properties.Raw)).NotTo(ContainSubstring("hunter2"))
		Expect(string(updated.Spec.Properties.Raw)).NotTo(ContainSubstring(secretInputPlaceholder))
		if updated.Status.Outputs != nil {
			Expect(string(updated.Status.Outputs.Raw)).NotTo(ContainSubstring("hunter2"))
		}
	})

	It("re-patches when the referenced Secret rotates", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: inputRefName},
			Data:       map[string][]byte{"password": []byte("hunter2")},
		})).To(Succeed())
		createResource()
		reconcileN(2)
		Expect(runner.patched).To(BeEmpty())

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: "default", Name: inputRefName}, secret)).To(Succeed())
		secret.Data["password"] = []byte("hunter3")
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())

		reconcileN(1)
		Expect(runner.patched).To(HaveLen(1), "secret rotation must trigger a re-patch")
	})

	It("stops terminally when the provider id would embed a secret", func() {
		runner.createErrs = []error{
			&pulumido.CodedError{Code: "SecretInputInID", Message: "id embeds a secret input value"},
			&pulumido.CodedError{Code: "SecretInputInID", Message: "id embeds a secret input value"},
		}
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: inputRefName},
			Data:       map[string][]byte{"password": []byte("hunter2")},
		})).To(Succeed())
		createResource()

		// Both reconciles must return nil errors (terminal, no retry — a
		// retry would orphan another external resource per attempt).
		reconcileN(3)

		updated := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, updated)).To(Succeed())
		cond := meta.FindStatusCondition(updated.Status.Conditions, dov1alpha1.ConditionSynced)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("SecretInputInID"))
		Expect(updated.Status.ID).To(BeEmpty(), "no leaking id may be persisted")
		Expect(runner.createErrs).ToNot(BeEmpty(), "terminal condition must not re-run the create")
	})

	It("rejects valuesFrom on component resources", func() {
		runner.componentMode = true
		createResource()

		reconcileN(2)

		updated := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, updated)).To(Succeed())
		cond := meta.FindStatusCondition(updated.Status.Conditions, dov1alpha1.ConditionSynced)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("ValuesFromUnsupported"))
		Expect(strings.Contains(cond.Message, "hunter2")).To(BeFalse())
		Expect(runner.componentCreates).To(BeEmpty())
	})
})
