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
	"bytes"
	"encoding/json"

	dov1alpha1 "github.com/dirien/doplane/api/v1alpha1"
	"github.com/dirien/doplane/internal/pulumido"
)

// deleteState returns the last recorded external state of res for the
// runner's Delete, or nil when none is recorded. Every spec.valuesFrom path
// is removed first: status.outputs echoes the substituted secret values, and
// the state travels in the runner Job spec, where a secret must never land.
// A provider's Delete does not need those values — the identity it acts on
// is the id plus the remaining state.
func deleteState(res *dov1alpha1.DoResource) map[string]any {
	if res.Status.Outputs == nil || len(res.Status.Outputs.Raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(res.Status.Outputs.Raw))
	dec.UseNumber()
	var state map[string]any
	if err := dec.Decode(&state); err != nil || len(state) == 0 {
		return nil
	}
	for _, v := range res.Spec.ValuesFrom {
		// An unparseable path was rejected at create time (InvalidValuesFrom);
		// nothing was substituted for it, so there is nothing to strip.
		_, _ = pulumido.DeletePath(state, v.ToPath)
	}
	return state
}
