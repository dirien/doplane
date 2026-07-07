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

package pulumido

import (
	"context"
	"fmt"
	"sort"
)

// SecretInput names one Secret key whose value materializes at a property
// path inside the runner — the value itself never passes through the
// controller, the object, or the operation document.
type SecretInput struct {
	// ToPath is the property path to set.
	ToPath string
	// SecretName is the Secret in the runner Job's namespace.
	SecretName string
	// SecretKey is the key within that Secret.
	SecretKey string
}

// secretInputsCtxKey carries the secret input plan of an operation.
type secretInputsCtxKey struct{}

// WithSecretInputs tags ctx with the operation's secret inputs. The
// JobRunner turns them into kubelet-resolved secretKeyRef environment
// variables; the ExecRunner resolves them through its ResolveSecret hook.
func WithSecretInputs(ctx context.Context, inputs []SecretInput) context.Context {
	return context.WithValue(ctx, secretInputsCtxKey{}, inputs)
}

// SecretInputsFromContext returns the secret inputs tagged onto ctx.
func SecretInputsFromContext(ctx context.Context) []SecretInput {
	inputs, _ := ctx.Value(secretInputsCtxKey{}).([]SecretInput)
	return inputs
}

// secretEnvName is the environment variable carrying the i-th secret input
// (in ToPath order) into the runner process.
func secretEnvName(i int) string {
	return fmt.Sprintf("DOPLANE_SECRET_%d", i)
}

// secretInputsPlan orders inputs deterministically by ToPath (Job names
// hash the op document, so the mapping must be stable) and returns the
// path→env mapping that travels in the op.
func secretInputsPlan(inputs []SecretInput) (map[string]string, []SecretInput) {
	ordered := make([]SecretInput, len(inputs))
	copy(ordered, inputs)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ToPath < ordered[j].ToPath })
	mapping := make(map[string]string, len(ordered))
	for i, in := range ordered {
		mapping[in.ToPath] = secretEnvName(i)
	}
	return mapping, ordered
}
