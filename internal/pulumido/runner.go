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

// Package pulumido connects the operator to runner operations
// (internal/runnerops): the ExecRunner runs them in-process for development,
// the JobRunner ships them to isolated Kubernetes Jobs running the
// doplane-runner binary. Operation outcomes travel as typed envelopes, so
// failures carry machine-readable codes instead of log heuristics.
package pulumido

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dirien/doplane/internal/runnerops"
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

// PackagePinned reports whether a package reference pins a version. Git and
// private-registry references carry their own pinning scheme; only plain
// references without "name@version" are unpinned (including an empty
// reference, where the package is inferred from the type token).
func PackagePinned(pkg string) bool {
	if kind, _ := runnerops.ClassifyPackageRef(pkg); kind != runnerops.PkgKindPlain {
		return true
	}
	return strings.Contains(pkg, "@")
}

// CodedError carries the runner's typed failure code; the reconciler
// surfaces it directly as the Synced condition reason.
type CodedError struct {
	Code    string
	Message string
}

func (e *CodedError) Error() string { return e.Code + ": " + e.Message }

// Runner abstracts execution of `pulumi do` operations and, for component
// resources, ephemeral-engine operations.
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

	// CreateComponent constructs a component resource through an ephemeral
	// engine, returning the component URN, its outputs and the exported
	// checkpoint (persisted by the caller and required for update/delete).
	CreateComponent(ctx context.Context, token, pkg string, props map[string]any) (string, map[string]any, []byte, error)
	// UpdateComponent re-applies a component with new properties on top of
	// the imported prior checkpoint.
	UpdateComponent(ctx context.Context, token, pkg string, props map[string]any, engineState []byte) (map[string]any, []byte, error)
	// DeleteComponent destroys a component from its imported checkpoint.
	DeleteComponent(ctx context.Context, token, pkg string, engineState []byte) error
}

// resultErr converts a runner result envelope into the sentinel/coded error
// contract the reconciler acts on. A nil return means success.
func resultErr(res runnerops.Result) error {
	if res.OK {
		return nil
	}
	coded := &CodedError{Code: res.Code, Message: res.Message}
	switch res.Code {
	case runnerops.CodeNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, coded)
	case runnerops.CodeReadNotSupported:
		return fmt.Errorf("%w: %w", ErrReadNotSupported, coded)
	default:
		return coded
	}
}

// decodeEnvelope parses the runner's result envelope from mixed pod-log
// output (the envelope is the final JSON object).
func decodeEnvelope(out string) (runnerops.Result, error) {
	obj, err := runnerops.LastJSONObject(out)
	if err != nil {
		return runnerops.Result{}, fmt.Errorf("no result envelope in runner output: %w (output: %s)", err, runnerops.Truncate(out, 2000))
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return runnerops.Result{}, err
	}
	var res runnerops.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return runnerops.Result{}, fmt.Errorf("decoding result envelope: %w", err)
	}
	if _, hasOK := obj["ok"]; !hasOK {
		return runnerops.Result{}, fmt.Errorf("runner output is not a result envelope (output: %s)", runnerops.Truncate(out, 2000))
	}
	return res, nil
}

// engineStateJSON validates and normalizes an engine checkpoint for the op.
func engineStateJSON(state []byte) (json.RawMessage, error) {
	if len(state) == 0 {
		return nil, nil
	}
	if len(state) > runnerops.MaxEngineStateBytes {
		return nil, fmt.Errorf("engine state is %d bytes, exceeding the %d byte limit", len(state), runnerops.MaxEngineStateBytes)
	}
	return json.RawMessage(state), nil
}

// classifyInfraFailure maps Job-infrastructure failure text (no envelope
// available) onto sentinel errors where safely possible.
func classifyInfraFailure(logs, failMsg string) error {
	text := runnerops.ProviderErrorText(logs, failMsg)
	if text == "" {
		return nil
	}
	switch {
	case strings.Contains(text, "not support import") || strings.Contains(text, "import not implemented"):
		return ErrReadNotSupported
	case strings.Contains(text, "not found") || strings.Contains(text, "notfound") ||
		strings.Contains(text, "nosuchbucket") || strings.Contains(text, "does not exist") ||
		strings.Contains(text, "status code: 404") || strings.Contains(text, "statuscode: 404"):
		return ErrNotFound
	}
	return nil
}
