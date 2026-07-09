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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

var _ = Describe("DoProvider Controller", func() {
	const providerName = "test-provider"
	const secretName = "test-provider-creds"

	ctx := context.Background()
	providerKey := types.NamespacedName{Name: providerName}
	secretKey := types.NamespacedName{Namespace: "default", Name: secretName}

	var reconciler *DoProviderReconciler
	var runner *fakeRunner

	newProvider := func(spec dov1alpha1.DoProviderSpec) *dov1alpha1.DoProvider {
		provider := &dov1alpha1.DoProvider{
			ObjectMeta: metav1.ObjectMeta{Name: providerName},
			Spec:       spec,
		}
		Expect(k8sClient.Create(ctx, provider)).To(Succeed())
		return provider
	}

	reconcileProvider := func() *dov1alpha1.DoProvider {
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: providerKey})
		Expect(err).NotTo(HaveOccurred())
		provider := &dov1alpha1.DoProvider{}
		Expect(k8sClient.Get(ctx, providerKey, provider)).To(Succeed())
		return provider
	}

	BeforeEach(func() {
		runner = &fakeRunner{}
		reconciler = &DoProviderReconciler{
			Client:          k8sClient,
			Scheme:          k8sClient.Scheme(),
			Recorder:        record.NewFakeRecorder(32),
			Schemas:         pulumido.NewSchemaCache(runner),
			RunnerNamespace: "default",
			PluginCachePath: "/var/lib/doplane/pulumi-home",
		}
	})

	AfterEach(func() {
		provider := &dov1alpha1.DoProvider{ObjectMeta: metav1.ObjectMeta{Name: providerName}}
		_ = k8sClient.Delete(ctx, provider)
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: secretName}}
		_ = k8sClient.Delete(ctx, secret)
	})

	It("becomes Ready when schema and credentials check out", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: secretName},
			Data:       map[string][]byte{"RANDOM_TOKEN": []byte("t")},
		})).To(Succeed())
		newProvider(dov1alpha1.DoProviderSpec{
			Package:              "random@4.21.0",
			CredentialsSecretRef: &dov1alpha1.LocalSecretReference{Name: secretName},
			CredentialKeys:       []string{"RANDOM_TOKEN"},
		})

		provider := reconcileProvider()
		Expect(meta.IsStatusConditionTrue(provider.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
		Expect(meta.IsStatusConditionTrue(provider.Status.Conditions, dov1alpha1.ConditionSchemaFetched)).To(BeTrue())
		Expect(meta.IsStatusConditionTrue(provider.Status.Conditions, dov1alpha1.ConditionPluginReady)).To(BeTrue())
		Expect(meta.IsStatusConditionTrue(provider.Status.Conditions, dov1alpha1.ConditionCredentialsReady)).To(BeTrue())
		Expect(provider.Status.Package).NotTo(BeNil())
		Expect(provider.Status.Package.Name).To(Equal("random"))
		Expect(provider.Status.Package.Version).To(Equal("4.21.0"))
		Expect(provider.Status.Plugin).NotTo(BeNil())
		Expect(provider.Status.Plugin.Ready).To(BeTrue())
		Expect(provider.Status.Plugin.CachePath).To(Equal("/var/lib/doplane/pulumi-home"))
		Expect(provider.Status.LastSchemaFetchTime).NotTo(BeNil())

		// The schema fetch must run with the profile's own credentials in
		// the runner namespace, not the deployment default.
		Expect(runner.schemaCreds).To(ConsistOf(secretName))
		Expect(runner.schemaNamespaces).To(ConsistOf("default"))
	})

	It("counts dependent resources", func() {
		newProvider(dov1alpha1.DoProviderSpec{Package: "random@4.21.0"})
		res := &dov1alpha1.DoResource{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "test-provider-dependent"},
			Spec: dov1alpha1.DoResourceSpec{
				Type:        "random:index/randomPet:RandomPet",
				ProviderRef: &dov1alpha1.ProviderReference{Name: providerName},
			},
		}
		Expect(k8sClient.Create(ctx, res)).To(Succeed())
		DeferCleanup(func() {
			res.Finalizers = nil
			_ = k8sClient.Update(ctx, res)
			_ = k8sClient.Delete(ctx, res)
		})

		provider := reconcileProvider()
		Expect(provider.Status.Dependents).To(Equal(int32(1)))
	})

	It("reports SecretNotFound when the credentials Secret is missing", func() {
		newProvider(dov1alpha1.DoProviderSpec{
			Package:              "random@4.21.0",
			CredentialsSecretRef: &dov1alpha1.LocalSecretReference{Name: secretName},
		})

		provider := reconcileProvider()
		cond := meta.FindStatusCondition(provider.Status.Conditions, dov1alpha1.ConditionCredentialsReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("SecretNotFound"))
		ready := meta.FindStatusCondition(provider.Status.Conditions, dov1alpha1.ConditionReady)
		Expect(ready.Status).To(Equal(metav1.ConditionFalse))
		Expect(ready.Reason).To(Equal("CredentialsNotReady"))
	})

	It("reports KeysMissing when required keys are absent", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: secretName},
			Data:       map[string][]byte{"OTHER": []byte("x")},
		})).To(Succeed())
		newProvider(dov1alpha1.DoProviderSpec{
			Package:              "random@4.21.0",
			CredentialsSecretRef: &dov1alpha1.LocalSecretReference{Name: secretName},
			CredentialKeys:       []string{"RANDOM_TOKEN"},
		})

		provider := reconcileProvider()
		cond := meta.FindStatusCondition(provider.Status.Conditions, dov1alpha1.ConditionCredentialsReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("KeysMissing"))
		Expect(cond.Message).To(ContainSubstring("RANDOM_TOKEN"))

		// Fixing the Secret turns the provider Ready on the next pass.
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, secretKey, secret)).To(Succeed())
		secret.Data["RANDOM_TOKEN"] = []byte("t")
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())
		provider = reconcileProvider()
		Expect(meta.IsStatusConditionTrue(provider.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
	})

	It("is Ready without a credentials Secret (credential-free providers)", func() {
		newProvider(dov1alpha1.DoProviderSpec{Package: "random@4.21.0"})

		provider := reconcileProvider()
		Expect(meta.IsStatusConditionTrue(provider.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
		cond := meta.FindStatusCondition(provider.Status.Conditions, dov1alpha1.ConditionCredentialsReady)
		Expect(cond.Reason).To(Equal("NotRequired"))

		// No credentialsSecretRef → the schema fetch stays untagged so
		// shared fetches keep using the deployment default.
		Expect(runner.schemaCreds).To(ConsistOf(""))
		Expect(runner.schemaNamespaces).To(ConsistOf(""))
	})

	It("checks DoProviderConfig credentials in the config's own namespace", func() {
		// Per-resource runner mode: Jobs (and thus Secrets) live in the
		// tenant namespace — that is where readiness must be validated.
		const tenantNamespace = "tenant-schema"
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: tenantNamespace},
		}))).To(Succeed())
		configReconciler := &DoProviderConfigReconciler{Profile: reconciler, PerResourceNamespace: true}
		config := &dov1alpha1.DoProviderConfig{
			ObjectMeta: metav1.ObjectMeta{Namespace: tenantNamespace, Name: providerName},
			Spec: dov1alpha1.DoProviderSpec{
				Package:              "random@4.21.0",
				CredentialsSecretRef: &dov1alpha1.LocalSecretReference{Name: secretName},
				CredentialKeys:       []string{"RANDOM_TOKEN"},
			},
		}
		Expect(k8sClient.Create(ctx, config)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, config) })
		configKey := types.NamespacedName{Namespace: tenantNamespace, Name: providerName}

		// Secret missing in the tenant namespace → not Ready.
		_, err := configReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: configKey})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, configKey, config)).To(Succeed())
		cond := meta.FindStatusCondition(config.Status.Conditions, dov1alpha1.ConditionCredentialsReady)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal("SecretNotFound"))

		// The schema fetch must carry the config's credentials Secret and
		// tenant namespace — a private registry package is only readable
		// with the tenant's PULUMI_ACCESS_TOKEN.
		Expect(runner.schemaCreds).To(ConsistOf(secretName))
		Expect(runner.schemaNamespaces).To(ConsistOf(tenantNamespace))

		// Tenant creates the Secret in their namespace → Ready.
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: tenantNamespace, Name: secretName},
			Data:       map[string][]byte{"RANDOM_TOKEN": []byte("t")},
		})).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: tenantNamespace, Name: secretName},
			})
		})
		_, err = configReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: configKey})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient.Get(ctx, configKey, config)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(config.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
	})
})
