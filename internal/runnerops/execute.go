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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Runner executes operations. The zero value works inside the runner image;
// dev mode (ExecRunner) overrides the fields.
type Runner struct {
	// PulumiBin is the pulumi executable; defaults to "pulumi" on PATH.
	PulumiBin string
	// BakedPlugins is the image's read-only plugin dir seeded into each
	// workspace ("" disables seeding).
	BakedPlugins string
	// PluginCache is the shared writable plugin cache (a PVC mount
	// in-cluster; "" disables it). Pinned plugins are installed there once
	// under a per-plugin lock and seeded into every operation's workspace.
	PluginCache string
	// Progress receives pulumi's human output (pod logs / dev console).
	// Defaults to os.Stderr, keeping stdout free for the result envelope.
	Progress io.Writer
	// LookupEnv resolves secret input environment variables; defaults to
	// os.LookupEnv (the Job path). Dev mode injects resolved values here
	// instead of polluting the process environment.
	LookupEnv func(string) (string, bool)
}

func (r *Runner) bin() string {
	if r.PulumiBin != "" {
		return r.PulumiBin
	}
	return "pulumi"
}

func (r *Runner) progress() io.Writer {
	if r.Progress != nil {
		return r.Progress
	}
	return os.Stderr
}

// Execute runs one operation to a decision. Infrastructure-level problems
// (workspace setup) come back as errors; operation outcomes — success or
// failure — come back in the Result.
func (r *Runner) Execute(ctx context.Context, op Op) Result {
	work, err := os.MkdirTemp("", "doplane-op-*")
	if err != nil {
		return failure(CodeOperationFailed, "creating workspace: %v", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	// A pinned plain package installs into the shared cache first (once,
	// under lock) so the operation — and every later one — reuses it.
	// Unpinned, git and registry references keep the on-demand path.
	cachePlugins := ""
	if r.PluginCache != "" {
		if name, version, ok := ParsePluginRef(op.Package); ok {
			if err := r.ensurePlugin(ctx, r.PluginCache, name, version); err != nil {
				var ce *cacheError
				if errors.As(err, &ce) {
					return failure(ce.code, "%v", err)
				}
				return failure(CodePluginInstall, "%v", err)
			}
		}
		cachePlugins = filepath.Join(r.PluginCache, "plugins")
	}

	ws, err := prepare(work, r.BakedPlugins, cachePlugins)
	if err != nil {
		return failure(CodeOperationFailed, "%v", err)
	}

	// Component engine checkpoints persist their inputs verbatim (they end
	// up in status.engineState), so secret inputs cannot be kept out of
	// etcd for engine verbs — refuse rather than leak.
	if len(op.SecretInputs) > 0 && (op.Verb == VerbEngineUp || op.Verb == VerbEngineDestroy) {
		return failure(CodeInvalidSpec, "secret inputs are not supported for component resources")
	}

	// Substitute secret input values (delivered out of band as env vars)
	// into the properties, and redact them from every output channel:
	// streamed progress, error messages and the recorded state.
	secrets, err := applySecretInputs(&op, r.lookupEnv())
	if err != nil {
		return failure(CodeSecretInputMissing, "%v", err)
	}
	run := *r
	var redactor *redactingWriter
	if len(secrets) > 0 {
		redactor = newRedactingWriter(r.progress(), secrets)
		run.Progress = redactor
	}

	var res Result
	switch op.Verb {
	case VerbCreate, VerbPatch, VerbRead, VerbDelete:
		res = run.executeDo(ctx, ws, op)
	case VerbSchema:
		res = run.executeSchema(ctx, ws, op)
	case VerbEngineUp, VerbEngineDestroy:
		res = run.executeEngine(ctx, ws, op)
	default:
		res = failure(CodeInvalidSpec, "unknown verb %q", op.Verb)
	}
	if redactor != nil {
		_ = redactor.Flush()
	}
	redactResult(&res, secrets)
	return res
}

func (r *Runner) lookupEnv() func(string) (string, bool) {
	if r.LookupEnv != nil {
		return r.LookupEnv
	}
	return os.LookupEnv
}

// executeDo runs one stateless `pulumi do` verb.
func (r *Runner) executeDo(ctx context.Context, ws *workspace, op Op) Result {
	args := []string{"do", op.Token}
	if op.Package != "" {
		args = append(args, "--package", op.Package)
	}
	args = append(args, op.Verb)
	if op.ID != "" {
		args = append(args, op.ID)
	}
	if op.Verb != VerbRead {
		args = append(args, "--yes")
	}
	if len(op.Properties) > 0 {
		pcl, err := MarshalPCL(op.Properties)
		if err != nil {
			return failure(CodeInvalidSpec, "rendering inputs: %v", err)
		}
		inputFile := filepath.Join(ws.root, "input.pp")
		if err := os.WriteFile(inputFile, []byte(pcl), 0o600); err != nil {
			return failure(CodeOperationFailed, "writing input file: %v", err)
		}
		args = append(args, "--input-file", inputFile)
	}
	args = append(args, "--stateless", "--non-interactive", "--color", "never")

	stdout, runErr := r.run(ctx, ws, args...)
	if runErr != nil {
		return classifyDoFailure(runErr, stdout)
	}
	if op.Verb == VerbDelete {
		return Result{OK: true, ID: op.ID}
	}
	state, err := LastJSONObject(stdout)
	if err != nil {
		return failure(CodeOutputParse, "parsing %s output: %v (output: %s)", op.Verb, err, Truncate(stdout, 1500))
	}
	id, _ := state["id"].(string)
	if id == "" && op.Verb == VerbCreate {
		return failure(CodeOutputParse, "create output contains no id (output: %s)", Truncate(stdout, 1500))
	}
	if id == "" {
		id = op.ID
	}
	return Result{OK: true, ID: id, State: state}
}

// executeSchema fetches the (trimmed) provider schema for op.Token.
func (r *Runner) executeSchema(ctx context.Context, ws *workspace, op Op) Result {
	kind, apiPath := ClassifyPackageRef(op.Package)
	var raw []byte
	if kind == PkgKindRegistry {
		client, err := newRegistryClient()
		if err != nil {
			return failure(CodeRegistryAuthMissing, "%v", err)
		}
		pkg, err := client.resolve(ctx, apiPath)
		if err != nil {
			return failure(CodeRegistryResolve, "%v", err)
		}
		raw, err = client.fetchSchema(ctx, pkg)
		if err != nil {
			return failure(CodeSchemaFetch, "%v", err)
		}
	} else {
		ref := op.Package
		if ref == "" {
			ref = PackageForToken(op.Token)
		}
		stdout, err := r.run(ctx, ws, "package", "get-schema", ref)
		if err != nil {
			return failure(CodeSchemaFetch, "pulumi package get-schema %s: %v", ref, err)
		}
		raw = []byte(stdout)
	}
	trimmed, err := trimSchema(raw, op.Token)
	if err != nil {
		return failure(CodeSchemaFetch, "%v", err)
	}
	return Result{OK: true, Schema: trimmed}
}

// trimSchema reduces a full provider schema to metadata plus the single
// requested resource, bounding what travels through pod logs and etcd.
func trimSchema(raw []byte, token string) ([]byte, error) {
	var full struct {
		Name      string                     `json:"name"`
		Version   string                     `json:"version"`
		Resources map[string]json.RawMessage `json:"resources"`
	}
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, fmt.Errorf("parsing provider schema: %w", err)
	}
	trimmed := map[string]any{
		"name":      full.Name,
		"version":   full.Version,
		"resources": map[string]json.RawMessage{},
	}
	if res, ok := full.Resources[token]; ok {
		trimmed["resources"] = map[string]json.RawMessage{token: res}
	}
	return json.Marshal(trimmed)
}

// executeEngine runs one ephemeral-engine operation (component construct /
// destroy) in the workspace.
func (r *Runner) executeEngine(ctx context.Context, ws *workspace, op Op) Result {
	// Resolve the package to a CLI-consumable source.
	pkgSrc := op.Package
	if kind, apiPath := ClassifyPackageRef(op.Package); kind == PkgKindRegistry {
		client, err := newRegistryClient()
		if err != nil {
			return failure(CodeRegistryAuthMissing, "%v", err)
		}
		pkg, err := client.resolve(ctx, apiPath)
		if err != nil {
			return failure(CodeRegistryResolve, "%v", err)
		}
		if pkgSrc, err = pkg.gitSource(); err != nil {
			return failure(CodeRegistryResolve, "%v", err)
		}
	}

	// One-resource program; `pulumi package add` appends the packages
	// section itself.
	program, err := engineProgram(op.Token, op.Properties)
	if err != nil {
		return failure(CodeInvalidSpec, "rendering program: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.projectDir(), "Pulumi.yaml"), []byte(program), 0o600); err != nil {
		return failure(CodeEngineFailed, "writing project: %v", err)
	}

	steps := [][]string{
		{"package", "add", pkgSrc},
		{"stack", "init", "dev", "--non-interactive"},
	}
	if len(op.EngineState) > 0 {
		prev := filepath.Join(ws.root, "prev-state.json")
		if err := os.WriteFile(prev, op.EngineState, 0o600); err != nil {
			return failure(CodeEngineFailed, "writing prior state: %v", err)
		}
		steps = append(steps, []string{"stack", "import", "--file", prev})
	}
	if op.Verb == VerbEngineDestroy {
		steps = append(steps, []string{"destroy", "--yes", "--non-interactive"})
	} else {
		steps = append(steps, []string{"up", "--yes", "--non-interactive", "--skip-preview"})
	}
	for _, step := range steps {
		if _, err := r.runIn(ctx, ws, ws.projectDir(), step...); err != nil {
			return failure(CodeEngineFailed, "pulumi %s: %v", strings.Join(step[:min(2, len(step))], " "), err)
		}
	}
	if op.Verb == VerbEngineDestroy {
		return Result{OK: true, ID: op.ID}
	}

	checkpoint := filepath.Join(ws.root, "checkpoint.json")
	if _, err := r.runIn(ctx, ws, ws.projectDir(), "stack", "export", "--file", checkpoint); err != nil {
		return failure(CodeEngineFailed, "pulumi stack export: %v", err)
	}
	return engineResultFromCheckpoint(checkpoint, op.Token)
}

// engineResultFromCheckpoint extracts the component's URN and outputs from
// the exported checkpoint.
func engineResultFromCheckpoint(path, token string) Result {
	raw, err := os.ReadFile(path) // #nosec G304 -- path is inside the op's private temp workspace
	if err != nil {
		return failure(CodeEngineFailed, "reading checkpoint: %v", err)
	}
	if len(raw) > MaxEngineStateBytes {
		return failure(CodeEngineFailed,
			"engine state is %d bytes, exceeding the %d byte limit supported for component resources", len(raw), MaxEngineStateBytes)
	}
	var checkpoint struct {
		Deployment struct {
			Resources []struct {
				URN     string         `json:"urn"`
				Type    string         `json:"type"`
				Outputs map[string]any `json:"outputs"`
			} `json:"resources"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return failure(CodeOutputParse, "parsing checkpoint: %v", err)
	}
	for _, res := range checkpoint.Deployment.Resources {
		if res.Type == token {
			return Result{OK: true, ID: res.URN, Outputs: res.Outputs, EngineState: raw}
		}
	}
	return failure(CodeOutputParse, "component %s not found in checkpoint", token)
}

// run executes pulumi with argv semantics from the workspace root.
func (r *Runner) run(ctx context.Context, ws *workspace, args ...string) (string, error) {
	return r.runIn(ctx, ws, ws.root, args...)
}

func (r *Runner) runIn(ctx context.Context, ws *workspace, dir string, args ...string) (string, error) {
	return r.runEnv(ctx, dir, ws.env, args...)
}

func (r *Runner) runEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	// #nosec G204 -- structured argv, no shell; inputs come from the validated CR spec.
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 10 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&stdout, r.progress())
	cmd.Stderr = io.MultiWriter(&stderr, r.progress())
	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		return stdout.String(), fmt.Errorf("%w: %s", err, Truncate(combined, 4000))
	}
	return stdout.String(), nil
}

// classifyDoFailure turns a failed `pulumi do` invocation into a typed
// result, matching only the provider's own error lines.
func classifyDoFailure(runErr error, stdout string) Result {
	text := ProviderErrorText(runErr.Error()+"\n"+stdout, "")
	switch {
	case strings.Contains(text, "not support import") || strings.Contains(text, "import not implemented"):
		return failure(CodeReadNotSupported, "%v", runErr)
	case strings.Contains(text, "not found") || strings.Contains(text, "notfound") ||
		strings.Contains(text, "nosuchbucket") || strings.Contains(text, "does not exist") ||
		strings.Contains(text, "status code: 404") || strings.Contains(text, "statuscode: 404"):
		return failure(CodeNotFound, "%v", runErr)
	}
	return failure(CodeOperationFailed, "%v", runErr)
}
