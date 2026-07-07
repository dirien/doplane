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

// Package pulumido drives `pulumi do` CRUD operations against cloud
// providers, either by executing the CLI locally (ExecRunner, for
// development) or by spawning an isolated Kubernetes Job per operation
// (JobRunner, the in-cluster default).
package pulumido

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrReadNotSupported is returned by Read when the provider does not
// implement read/import for the resource type.
var ErrReadNotSupported = errors.New("provider does not support read for this resource")

// ErrNotFound is returned when the external resource no longer exists.
var ErrNotFound = errors.New("external resource not found")

// ErrOutputUnavailable is returned when an operation completed but its
// result could not be retrieved (e.g. runner pod logs unreadable). The
// mutation may have succeeded: callers must retry retrieval, never blindly
// re-run the mutation.
var ErrOutputUnavailable = errors.New("operation completed but its output is unavailable")

// Runner abstracts execution of `pulumi do` operations.
type Runner interface {
	// Create provisions a new resource and returns its id and full state.
	Create(ctx context.Context, token, pkg string, props map[string]any) (string, map[string]any, error)
	// Patch updates an existing resource in place and returns its new state.
	Patch(ctx context.Context, token, pkg, id string, props map[string]any) (map[string]any, error)
	// Read fetches the current state of an existing resource.
	Read(ctx context.Context, token, pkg, id string) (map[string]any, error)
	// Delete removes the external resource.
	Delete(ctx context.Context, token, pkg, id string) error
	// FetchSchema retrieves the provider schema (at minimum covering token)
	// from the Pulumi registry.
	FetchSchema(ctx context.Context, pkg, token string) (*PackageSchema, error)
}

// doArgs builds the `pulumi do` argument list shared by both runners. When
// inputFile is non-empty it is passed to create/patch verbs.
func doArgs(token, pkg, verb, id, inputFile string) []string {
	args := []string{"do", token}
	if pkg != "" {
		args = append(args, "--package", pkg)
	}
	args = append(args, verb)
	if id != "" {
		args = append(args, id)
	}
	if verb != "read" {
		args = append(args, "--yes")
	}
	if inputFile != "" {
		args = append(args, "--input-file", inputFile)
	}
	// Stateless mode runs CRUD directly against the provider without a
	// Pulumi state file — this operator's whole premise: the DoResource
	// status in etcd is the state store. Required since pulumi 3.250.
	args = append(args, "--stateless", "--non-interactive", "--color", "never")
	return args
}

// providerErrorText extracts the provider's own error lines from CLI/pod
// output (lines starting with "error"), appending any extra failure message.
// Classification runs against this text only — never against command lines
// or echoed inputs, whose contents (e.g. a resource named "not-found-test")
// would otherwise cause false classification.
func providerErrorText(output, extra string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(strings.ToLower(line))
		if strings.HasPrefix(trimmed, "error:") || strings.HasPrefix(trimmed, "error ") ||
			strings.HasPrefix(trimmed, "[error]") {
			lines = append(lines, trimmed)
		}
	}
	if extra != "" {
		lines = append(lines, strings.ToLower(extra))
	}
	return strings.Join(lines, "\n")
}

// classifyText maps provider error text (see providerErrorText) onto
// sentinel errors the reconciler can act on. Matching is necessarily
// heuristic: `pulumi do` surfaces raw provider messages. Returns nil when
// nothing matches.
func classifyText(msg string) error {
	if msg == "" {
		return nil
	}
	switch {
	case strings.Contains(msg, "not support import") || strings.Contains(msg, "import not implemented"):
		return ErrReadNotSupported
	case strings.Contains(msg, "not found") || strings.Contains(msg, "notfound") ||
		strings.Contains(msg, "nosuchbucket") || strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "status code: 404") || strings.Contains(msg, "statuscode: 404"):
		return ErrNotFound
	}
	return nil
}

// lastJSONObject extracts the final top-level JSON object from mixed CLI
// output. `pulumi do` prints a human preamble and an echo of the inputs
// before the resulting resource state; the state is always the last object.
func lastJSONObject(out string) (map[string]any, error) {
	var last map[string]any
	found := false
	for i := 0; i < len(out); i++ {
		if out[i] != '{' || (i > 0 && out[i-1] != '\n') {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(out[i:]))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err == nil {
			last = m
			found = true
			i += int(dec.InputOffset()) - 1
		}
	}
	if !found {
		return nil, errors.New("no JSON object found in output")
	}
	return last, nil
}

// stateAndID extracts the id from a parsed resource state.
func stateAndID(state map[string]any, raw string) (string, error) {
	id, _ := state["id"].(string)
	if id == "" {
		return "", fmt.Errorf("operation output contains no id (output: %s)", truncate(raw, 2000))
	}
	return id, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…(truncated)"
}
