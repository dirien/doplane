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
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// secretInputPlaceholder stands in for valuesFrom values in the properties
// the controller handles (schema validation, hashing, the op document).
// The runner replaces it with the real value just before the provider call.
const secretInputPlaceholder = "doplane:secret-input"

// mixSecretVersions folds the resourceVersions of same-namespace valuesFrom
// Secrets into the applied hash: the values themselves never reach the
// controller, so a rotation would otherwise be invisible and never
// re-patch. Metadata-only reads keep secret data out of controller memory;
// Secrets living elsewhere (operator-namespace runner mode) are skipped —
// rotation then applies with the next spec change or drift patch.
//
// It also returns salt, a digest of the same versions independent of hash.
// The controller tags the runner context with salt so a rotation yields a
// distinct Job name and cannot adopt a completed Job that ran with the old
// value; keeping salt separate leaves the appliedHash formula unchanged, so
// upgrades do not spuriously re-patch every resource.
func (r *DoResourceReconciler) mixSecretVersions(ctx context.Context, res *dov1alpha1.DoResource, hash string) (mixed, salt string) {
	if len(res.Spec.ValuesFrom) == 0 {
		return hash, ""
	}
	full := sha256.New()
	full.Write([]byte(hash))
	saltH := sha256.New()
	for _, v := range res.Spec.ValuesFrom {
		pm := &metav1.PartialObjectMetadata{}
		pm.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
		if err := r.reader().Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: v.SecretKeyRef.Name}, pm); err != nil {
			continue
		}
		_, _ = fmt.Fprintf(full, "%s=%s;", v.SecretKeyRef.Name, pm.ResourceVersion)
		_, _ = fmt.Fprintf(saltH, "%s=%s;", v.SecretKeyRef.Name, pm.ResourceVersion)
	}
	return hex.EncodeToString(full.Sum(nil))[:16], hex.EncodeToString(saltH.Sum(nil))[:16]
}

// stageSecretInputs prepares spec.valuesFrom for the operation: a
// placeholder lands in props (satisfying schema validation), and the
// path→Secret plan is tagged onto ctx for the runner's out-of-band env
// injection. A non-nil halt means a terminal condition was recorded.
func (r *DoResourceReconciler) stageSecretInputs(ctx context.Context, res *dov1alpha1.DoResource, props map[string]any) (
	context.Context, *ctrl.Result, error,
) {
	if len(res.Spec.ValuesFrom) == 0 {
		return ctx, nil, nil
	}
	inputs := make([]pulumido.SecretInput, 0, len(res.Spec.ValuesFrom))
	for i, v := range res.Spec.ValuesFrom {
		if err := pulumido.SetPath(props, v.ToPath, secretInputPlaceholder); err != nil {
			result, ferr := r.markSyncFailed(ctx, res, "InvalidValuesFrom", fmt.Errorf("valuesFrom[%d]: %w", i, err), false)
			return ctx, &result, ferr
		}
		inputs = append(inputs, pulumido.SecretInput{
			ToPath: v.ToPath, SecretName: v.SecretKeyRef.Name, SecretKey: v.SecretKeyRef.Key,
		})
	}
	return pulumido.WithSecretInputs(ctx, inputs), nil, nil
}
