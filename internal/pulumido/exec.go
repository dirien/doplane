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
	"encoding/json"
	"fmt"
	"time"

	"github.com/dirien/doplane/internal/runnerops"
)

// ExecRunner executes runner operations in-process with a local pulumi
// binary. It is intended for development (`make run`); in-cluster the
// JobRunner is used instead so provider plugins run isolated from the
// manager. Both run the exact same runnerops code.
type ExecRunner struct {
	// PulumiBin is the pulumi executable; defaults to "pulumi" on PATH.
	PulumiBin string
	// Timeout bounds a single operation.
	Timeout time.Duration
	// ResolveSecret resolves a valuesFrom Secret key ((namespace, name,
	// key) → value) — dev mode has no kubelet env injection. Nil rejects
	// operations with secret inputs.
	ResolveSecret func(ctx context.Context, namespace, name, key string) (string, error)
}

var _ Runner = (*ExecRunner)(nil)

func (r *ExecRunner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 10 * time.Minute
}

func (r *ExecRunner) execute(ctx context.Context, op runnerops.Op) (runnerops.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	ops := &runnerops.Runner{PulumiBin: r.PulumiBin}
	if inputs := SecretInputsFromContext(ctx); len(inputs) > 0 {
		if r.ResolveSecret == nil {
			return runnerops.Result{}, &CodedError{Code: runnerops.CodeSecretInputMissing,
				Message: "exec runner has no secret resolver configured"}
		}
		var ordered []SecretInput
		op.SecretInputs, ordered = secretInputsPlan(inputs)
		values := make(map[string]string, len(ordered))
		namespace := NamespaceFromContext(ctx)
		for i, in := range ordered {
			value, err := r.ResolveSecret(ctx, namespace, in.SecretName, in.SecretKey)
			if err != nil {
				return runnerops.Result{}, &CodedError{Code: runnerops.CodeSecretInputMissing,
					Message: fmt.Sprintf("resolving secret input for %q: %v", in.ToPath, err)}
			}
			values[secretEnvName(i)] = value
		}
		ops.LookupEnv = func(name string) (string, bool) {
			v, ok := values[name]
			return v, ok
		}
	}
	res := ops.Execute(ctx, op)
	return res, resultErr(res)
}

// Create implements Runner.
func (r *ExecRunner) Create(ctx context.Context, token, pkg string, props map[string]any) (string, map[string]any, error) {
	res, err := r.execute(ctx, runnerops.Op{Verb: runnerops.VerbCreate, Token: token, Package: pkg, Properties: props})
	if err != nil {
		return "", nil, err
	}
	return res.ID, res.State, nil
}

// Patch implements Runner.
func (r *ExecRunner) Patch(ctx context.Context, token, pkg, id string, props map[string]any) (map[string]any, error) {
	res, err := r.execute(ctx, runnerops.Op{Verb: runnerops.VerbPatch, Token: token, Package: pkg, ID: id, Properties: props})
	if err != nil {
		return nil, err
	}
	return res.State, nil
}

// Read implements Runner.
func (r *ExecRunner) Read(ctx context.Context, token, pkg, id string) (map[string]any, error) {
	res, err := r.execute(ctx, runnerops.Op{Verb: runnerops.VerbRead, Token: token, Package: pkg, ID: id})
	if err != nil {
		return nil, err
	}
	return res.State, nil
}

// Delete implements Runner.
func (r *ExecRunner) Delete(ctx context.Context, token, pkg, id string) error {
	_, err := r.execute(ctx, runnerops.Op{Verb: runnerops.VerbDelete, Token: token, Package: pkg, ID: id})
	return err
}

// FetchSchema implements Runner.
func (r *ExecRunner) FetchSchema(ctx context.Context, pkg, token string) (*PackageSchema, error) {
	res, err := r.execute(ctx, runnerops.Op{Verb: runnerops.VerbSchema, Token: token, Package: pkg})
	if err != nil {
		return nil, err
	}
	var s PackageSchema
	if err := json.Unmarshal(res.Schema, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateComponent implements Runner.
func (r *ExecRunner) CreateComponent(ctx context.Context, token, pkg string, props map[string]any) (string, map[string]any, []byte, error) {
	res, err := r.execute(ctx, runnerops.Op{Verb: runnerops.VerbEngineUp, Token: token, Package: pkg, Properties: props})
	if err != nil {
		return "", nil, nil, err
	}
	return res.ID, res.Outputs, res.EngineState, nil
}

// UpdateComponent implements Runner.
func (r *ExecRunner) UpdateComponent(ctx context.Context, token, pkg string, props map[string]any, engineState []byte) (map[string]any, []byte, error) {
	state, err := engineStateJSON(engineState)
	if err != nil {
		return nil, nil, err
	}
	res, err := r.execute(ctx, runnerops.Op{Verb: runnerops.VerbEngineUp, Token: token, Package: pkg, Properties: props, EngineState: state})
	if err != nil {
		return nil, nil, err
	}
	return res.Outputs, res.EngineState, nil
}

// DeleteComponent implements Runner.
func (r *ExecRunner) DeleteComponent(ctx context.Context, token, pkg string, engineState []byte) error {
	state, err := engineStateJSON(engineState)
	if err != nil {
		return err
	}
	_, err = r.execute(ctx, runnerops.Op{Verb: runnerops.VerbEngineDestroy, Token: token, Package: pkg, EngineState: state})
	return err
}
