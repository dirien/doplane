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

package runnerops

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// Names inside the throwaway stack the delete fallback destroys. They only
// have to agree between the project file, the stack and the checkpoint URNs.
const (
	deleteFallbackProject  = "doplane"
	deleteFallbackStack    = "dev"
	deleteFallbackResource = "target"
	// deleteFallbackProviderID is the id of the synthesized default provider
	// resource. The engine requires a non-empty id; the value is opaque.
	deleteFallbackProviderID = "00000000-0000-4000-8000-000000000001"
)

// deleteFallbackApplies reports whether a failed stateless delete died in
// its read-before-delete phase. `pulumi do --stateless delete` imports the
// resource by id before calling the provider's Delete and gives up when the
// provider has no read/import for the type (ReadNotSupported) or its
// importer rejects the id — bridged importers report that as an "import"
// error (RandomInteger, for one, expects "{result},{min},{max}"). A failure
// of the provider's Delete call itself is not retried here: the operator
// requeues those, and repeating the mutation inside the same operation would
// only double the provider's work.
func deleteFallbackApplies(res Result) bool {
	switch res.Code {
	case CodeReadNotSupported:
		return true
	case CodeOperationFailed:
		return strings.Contains(ProviderErrorText(res.Message, ""), "import")
	default:
		return false
	}
}

// deletePluginRef resolves the provider plugin the fallback checkpoint
// names: the pinned plain package, an unpinned plain package (any installed
// version), or the package inferred from the token. Git and registry
// packages are not supported — their plugin only exists after `pulumi
// package add`, which the checkpoint path does not run.
func deletePluginRef(op Op) (name, version string, ok bool) {
	if op.Package == "" {
		return PackageForToken(op.Token), "", true
	}
	if name, version, ok := ParsePluginRef(op.Package); ok {
		return name, version, true
	}
	if kind, _ := ClassifyPackageRef(op.Package); kind == PkgKindPlain {
		return op.Package, "", true
	}
	return "", "", false
}

// checkpointResource is one entry of a synthesized deployment checkpoint.
type checkpointResource struct {
	URN      string         `json:"urn"`
	Custom   bool           `json:"custom"`
	Type     string         `json:"type"`
	ID       string         `json:"id"`
	Inputs   map[string]any `json:"inputs"`
	Outputs  map[string]any `json:"outputs"`
	Provider string         `json:"provider,omitempty"`
}

// deleteCheckpoint renders the checkpoint `pulumi stack import` needs to
// destroy exactly one custom resource: its default provider (pinned to
// version when known) and the resource itself with the recorded state as
// both inputs and outputs — the same state a successful read would have
// produced for the stateless delete. The recorded "id" property is dropped
// from inputs (it is never an input); outputs keep it, as `pulumi do` prints
// it.
func deleteCheckpoint(pluginName, pluginVersion, token, id string, state map[string]any) ([]byte, error) {
	providerURN := fmt.Sprintf("urn:pulumi:%s::%s::pulumi:providers:%s::default",
		deleteFallbackStack, deleteFallbackProject, pluginName)
	providerProps := map[string]any{}
	if pluginVersion != "" {
		providerProps["version"] = pluginVersion
	}
	inputs := maps.Clone(state)
	delete(inputs, "id")
	checkpoint := map[string]any{
		"version": 3,
		"deployment": map[string]any{
			"manifest": map[string]any{"time": "0001-01-01T00:00:00Z", "magic": "", "version": ""},
			"resources": []checkpointResource{
				{
					URN:     providerURN,
					Custom:  true,
					Type:    "pulumi:providers:" + pluginName,
					ID:      deleteFallbackProviderID,
					Inputs:  providerProps,
					Outputs: maps.Clone(providerProps),
				},
				{
					URN: fmt.Sprintf("urn:pulumi:%s::%s::%s::%s",
						deleteFallbackStack, deleteFallbackProject, token, deleteFallbackResource),
					Custom:   true,
					Type:     token,
					ID:       id,
					Inputs:   inputs,
					Outputs:  maps.Clone(state),
					Provider: providerURN + "::" + deleteFallbackProviderID,
				},
			},
		},
	}
	return json.Marshal(checkpoint)
}

// executeStateDelete deletes a resource whose stateless delete could not
// read it back, by handing the provider the recorded state through an
// ephemeral engine: a synthesized checkpoint holding the provider and the
// resource (id plus last recorded state) is imported into a throwaway stack
// on the workspace's file backend and destroyed. The engine calls the
// provider's Delete with that state — what the stateless path passes after a
// successful read — and never reads first. primary is the stateless failure,
// reported alongside a fallback failure so both are visible.
func (r *Runner) executeStateDelete(ctx context.Context, ws *workspace, op Op, primary Result) Result {
	pluginName, pluginVersion, ok := deletePluginRef(op)
	if !ok {
		return primary
	}
	checkpoint, err := deleteCheckpoint(pluginName, pluginVersion, op.Token, op.ID, op.State)
	if err != nil {
		return failure(CodeInvalidSpec, "rendering delete checkpoint: %v", err)
	}
	// A project file is required for a stack; the program is never run
	// (destroy works from the imported state alone).
	project := fmt.Sprintf("{\"name\": %q, \"runtime\": \"yaml\"}\n", deleteFallbackProject)
	if err := os.WriteFile(filepath.Join(ws.projectDir(), "Pulumi.yaml"), []byte(project), 0o600); err != nil {
		return failure(CodeOperationFailed, "writing project: %v", err)
	}
	checkpointFile := filepath.Join(ws.root, "delete-state.json")
	if err := os.WriteFile(checkpointFile, checkpoint, 0o600); err != nil {
		return failure(CodeOperationFailed, "writing delete checkpoint: %v", err)
	}

	steps := [][]string{
		{"stack", "init", deleteFallbackStack, "--non-interactive"},
		{"stack", "import", "--file", checkpointFile},
		{"destroy", "--yes", "--non-interactive"},
	}
	for _, step := range steps {
		if _, err := r.runIn(ctx, ws, ws.projectDir(), step...); err != nil {
			// The provider's own verdict still counts: a resource that turned
			// out to be gone is reported as such, and the operator finalizes.
			if fb := classifyDoFailure(err, ""); fb.Code == CodeNotFound {
				return fb
			}
			return failure(CodeOperationFailed,
				"stateless delete failed (%s: %s); engine destroy fallback failed: pulumi %s: %v",
				primary.Code, Truncate(primary.Message, 1500), strings.Join(step[:min(2, len(step))], " "), err)
		}
	}
	return Result{OK: true, ID: op.ID}
}
