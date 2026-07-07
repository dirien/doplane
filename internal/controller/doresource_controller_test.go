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
	"maps"

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

// fakeRunner implements pulumido.Runner in memory.
type fakeRunner struct {
	created map[string]map[string]any
	patched []map[string]any
	deleted []string

	// componentMode makes FetchSchema report tokens as components and
	// records engine operations.
	componentMode    bool
	componentCreates []map[string]any
	componentUpdates []map[string]any
	componentDeletes [][]byte
}

func (f *fakeRunner) CreateComponent(_ context.Context, token, _ string, props map[string]any) (string, map[string]any, []byte, error) {
	f.componentCreates = append(f.componentCreates, props)
	return "urn:pulumi:dev::doplane::" + token + "::res",
		map[string]any{"endpoint": "svc.local:8080"},
		[]byte(`{"version":3,"deployment":{"resources":[]}}`), nil
}

func (f *fakeRunner) UpdateComponent(_ context.Context, _, _ string, props map[string]any, _ []byte) (map[string]any, []byte, error) {
	f.componentUpdates = append(f.componentUpdates, props)
	return map[string]any{"endpoint": "svc.local:8080", "updated": true},
		[]byte(`{"version":3,"deployment":{"resources":["updated"]}}`), nil
}

func (f *fakeRunner) DeleteComponent(_ context.Context, _, _ string, engineState []byte) error {
	f.componentDeletes = append(f.componentDeletes, engineState)
	return nil
}

func (f *fakeRunner) Create(_ context.Context, token, _ string, props map[string]any) (string, map[string]any, error) {
	id := fmt.Sprintf("fake-id-%d", len(f.created)+1)
	state := map[string]any{"id": id}
	maps.Copy(state, props)
	if f.created == nil {
		f.created = map[string]map[string]any{}
	}
	f.created[token+"/"+id] = state
	return id, state, nil
}

func (f *fakeRunner) Patch(_ context.Context, _, _, id string, props map[string]any) (map[string]any, error) {
	state := map[string]any{"id": id}
	maps.Copy(state, props)
	f.patched = append(f.patched, state)
	return state, nil
}

func (f *fakeRunner) Read(_ context.Context, _, _, id string) (map[string]any, error) {
	return map[string]any{"id": id}, nil
}

func (f *fakeRunner) Delete(_ context.Context, _, _, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeRunner) FetchSchema(_ context.Context, _, token string) (*pulumido.PackageSchema, error) {
	return &pulumido.PackageSchema{
		Name:    "random",
		Version: "4.21.0",
		Resources: map[string]pulumido.ResourceSchema{
			token: {
				IsComponent: f.componentMode,
				InputProperties: map[string]pulumido.PropertySchema{
					"length": {Type: "integer"},
					"prefix": {Type: "string"},
				},
			},
		},
	}, nil
}

var _ = Describe("DoResource Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"
		const token = "random:index/randomPet:RandomPet"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

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

			doresource := &dov1alpha1.DoResource{}
			err := k8sClient.Get(ctx, typeNamespacedName, doresource)
			if err != nil && errors.IsNotFound(err) {
				resource := &dov1alpha1.DoResource{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: dov1alpha1.DoResourceSpec{
						Type: token,
						Properties: &apiextensionsv1.JSON{
							Raw: []byte(`{"length": 3, "prefix": "doplane"}`),
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &dov1alpha1.DoResource{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if errors.IsNotFound(err) {
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			// Run the finalizer so the object actually goes away.
			for range 3 {
				if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName}); err != nil {
					break
				}
				if err := k8sClient.Get(ctx, typeNamespacedName, resource); errors.IsNotFound(err) {
					return
				}
			}
		})

		reconcileN := func(n int) {
			for i := range n {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("reconcile #%d", i+1))
			}
		}

		It("should create the external resource and record its state", func() {
			// First reconcile adds the finalizer, second creates.
			reconcileN(2)

			updated := &dov1alpha1.DoResource{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Finalizers).To(ContainElement("do.pulumi.com/finalizer"))
			Expect(updated.Status.ID).To(Equal("fake-id-1"))
			Expect(updated.Status.Outputs).NotTo(BeNil())
			Expect(string(updated.Status.Outputs.Raw)).To(ContainSubstring(`"id":"fake-id-1"`))
			Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, dov1alpha1.ConditionReady)).To(BeTrue())
			Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, dov1alpha1.ConditionSynced)).To(BeTrue())
			Expect(runner.created).To(HaveLen(1))
		})

		It("should delete the external resource when the object is deleted", func() {
			reconcileN(2)

			resource := &dov1alpha1.DoResource{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			reconcileN(1)
			Expect(runner.deleted).To(ContainElement("fake-id-1"))
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})

		It("should reject properties that violate the provider schema", func() {
			resource := &dov1alpha1.DoResource{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, resource)).To(Succeed())
			resource.Spec.Properties = &apiextensionsv1.JSON{Raw: []byte(`{"bogus": true}`)}
			Expect(k8sClient.Update(ctx, resource)).To(Succeed())

			reconcileN(2)

			updated := &dov1alpha1.DoResource{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			cond := meta.FindStatusCondition(updated.Status.Conditions, dov1alpha1.ConditionSynced)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("ValidationFailed"))
			Expect(cond.Message).To(ContainSubstring("bogus"))
			Expect(runner.created).To(BeEmpty())
		})
	})
})
