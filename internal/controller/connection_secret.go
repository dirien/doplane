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
	"reflect"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// publishConnectionSecret writes the selected connection details into the
// spec.writeConnectionSecretToRef Secret in the resource's namespace. The
// Secret is owned by the resource (garbage-collected with it). Values never
// travel through events or logs — messages carry key names and field paths
// only. Reads go through the live API reader so Secrets are never pulled
// into the informer cache.
func (r *DoResourceReconciler) publishConnectionSecret(ctx context.Context, res *dov1alpha1.DoResource) error {
	ref := res.Spec.WriteConnectionSecretToRef
	if ref == nil {
		return nil
	}

	existing := &corev1.Secret{}
	err := r.reader().Get(ctx, types.NamespacedName{Namespace: res.Namespace, Name: ref.Name}, existing)
	exists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading connection secret %q: %w", ref.Name, err)
	}
	if exists {
		// Refuse to overwrite a Secret this resource does not own: another
		// object (or a user) created it, and clobbering it would be a
		// cross-resource write primitive.
		if owner := metav1.GetControllerOf(existing); owner == nil || owner.UID != res.UID {
			return fmt.Errorf("connection secret %q exists but is not owned by this resource; choose another name", ref.Name)
		}
	}

	data := map[string][]byte{}
	for i, d := range res.Spec.ConnectionDetails {
		switch {
		case d.Value != "" && d.FromFieldPath != "":
			return fmt.Errorf("connectionDetails[%d] (%s): fromFieldPath and value are mutually exclusive", i, d.Name)
		case d.Value != "":
			data[d.Name] = []byte(d.Value)
		case d.FromFieldPath != "":
			value, ok, err := resolveFieldPath(res, d.FromFieldPath)
			if err != nil {
				return fmt.Errorf("connectionDetails[%d] (%s): %w", i, d.Name, err)
			}
			if !ok {
				// Refresh reads can return partial state (providers without
				// full read support); a previously published value must
				// survive that, so carry it over instead of dropping the key.
				if prev, held := existing.Data[d.Name]; exists && held {
					data[d.Name] = prev
					continue
				}
				// Path names are not sensitive; values never appear here.
				r.Recorder.Eventf(res, "Warning", "ConnectionDetailUnresolved",
					"connection detail %q: %s is not populated yet; key omitted", d.Name, d.FromFieldPath)
				continue
			}
			data[d.Name] = []byte(pulumido.RenderScalar(value))
		default:
			return fmt.Errorf("connectionDetails[%d] (%s): one of fromFieldPath or value is required", i, d.Name)
		}
	}

	if !exists {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: res.Namespace, Name: ref.Name},
			Data:       data,
		}
		if err := controllerutil.SetControllerReference(res, secret, r.Scheme); err != nil {
			return fmt.Errorf("connection secret owner reference: %w", err)
		}
		if err := r.Create(ctx, secret); err != nil {
			return fmt.Errorf("creating connection secret %q: %w", ref.Name, err)
		}
		r.Recorder.Eventf(res, "Normal", "ConnectionSecretCreated", "connection details written to secret %q", ref.Name)
		return nil
	}

	if reflect.DeepEqual(existing.Data, data) {
		return nil
	}
	existing.Data = data
	if err := r.Update(ctx, existing); err != nil {
		return fmt.Errorf("updating connection secret %q: %w", ref.Name, err)
	}
	r.Recorder.Eventf(res, "Normal", "ConnectionSecretUpdated", "connection details written to secret %q", ref.Name)
	return nil
}
