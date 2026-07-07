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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

var _ = Describe("DoResource connection secrets", func() {
	const resourceName = "test-connresource"
	const connSecretName = "test-conn"
	const token = "random:index/randomPet:RandomPet"

	ctx := context.Background()
	resourceKey := types.NamespacedName{Namespace: "default", Name: resourceName}
	secretKey := types.NamespacedName{Namespace: "default", Name: connSecretName}

	var runner *fakeRunner
	var recorder *record.FakeRecorder
	var reconciler *DoResourceReconciler

	BeforeEach(func() {
		runner = &fakeRunner{}
		recorder = record.NewFakeRecorder(32)
		reconciler = &DoResourceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: recorder,
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
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: connSecretName}}
		_ = k8sClient.Delete(ctx, secret)
	})

	createResource := func(details []dov1alpha1.ConnectionDetail) {
		res := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: resourceName},
			Spec: dov1alpha1.DoResourceSpec{
				Type:                       token,
				Properties:                 &apiextensionsv1.JSON{Raw: []byte(`{"length": 3, "prefix": "conn"}`)},
				WriteConnectionSecretToRef: &dov1alpha1.LocalSecretReference{Name: connSecretName},
				ConnectionDetails:          details,
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

	It("publishes selected outputs and static values into an owned Secret", func() {
		createResource([]dov1alpha1.ConnectionDetail{
			{Name: "endpoint", FromFieldPath: "status.outputs.prefix"},
			{Name: "id", FromFieldPath: "status.id"},
			{Name: "username", Value: "app"},
		})

		reconcileN(2)

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
		Expect(string(secret.Data["endpoint"])).To(Equal("conn"))
		Expect(string(secret.Data["id"])).To(Equal("fake-id-1"))
		Expect(string(secret.Data["username"])).To(Equal("app"))

		res := &dov1alpha1.DoResource{}
		Expect(k8sClient.Get(ctx, resourceKey, res)).To(Succeed())
		owner := metav1.GetControllerOf(secret)
		Expect(owner).NotTo(BeNil())
		Expect(owner.UID).To(Equal(res.UID))
	})

	It("omits unresolved paths with an event that never contains values", func() {
		createResource([]dov1alpha1.ConnectionDetail{
			{Name: "missing", FromFieldPath: "status.outputs.doesNotExist"},
			{Name: "username", Value: "app"},
		})

		reconcileN(2)

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
		Expect(secret.Data).NotTo(HaveKey("missing"))
		Expect(string(secret.Data["username"])).To(Equal("app"))

		var unresolved string
		for len(recorder.Events) > 0 {
			e := <-recorder.Events
			if strings.Contains(e, "ConnectionDetailUnresolved") {
				unresolved = e
			}
		}
		Expect(unresolved).To(ContainSubstring("status.outputs.doesNotExist"))
	})

	It("refuses to overwrite a Secret it does not own", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: connSecretName},
			Data:       map[string][]byte{"stolen": []byte("no")},
		})).To(Succeed())
		createResource([]dov1alpha1.ConnectionDetail{{Name: "username", Value: "app"}})

		// The sync succeeds but publishing must fail on the foreign Secret.
		var err error
		for range 2 {
			if _, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: resourceKey}); err != nil {
				break
			}
		}
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not owned"))

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
		Expect(string(secret.Data["stolen"])).To(Equal("no"))
	})
})
