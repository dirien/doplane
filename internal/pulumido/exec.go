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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ExecRunner executes `pulumi do` with a local pulumi binary. It is intended
// for development (`make run`); in-cluster the JobRunner is used instead so
// provider plugins run isolated from the manager.
type ExecRunner struct {
	// PulumiBin is the pulumi executable; defaults to "pulumi" on PATH.
	PulumiBin string
	// Timeout bounds a single CLI invocation.
	Timeout time.Duration
}

var _ Runner = (*ExecRunner)(nil)

func (r *ExecRunner) bin() string {
	if r.PulumiBin != "" {
		return r.PulumiBin
	}
	return "pulumi"
}

func (r *ExecRunner) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 10 * time.Minute
}

// Create implements Runner.
func (r *ExecRunner) Create(ctx context.Context, token, pkg string, props map[string]any) (string, map[string]any, error) {
	out, err := r.runWithInput(ctx, token, pkg, "create", "", props)
	if err != nil {
		return "", nil, err
	}
	state, err := lastJSONObject(out)
	if err != nil {
		return "", nil, fmt.Errorf("parsing create output: %w (output: %s)", err, truncate(out, 2000))
	}
	id, err := stateAndID(state, out)
	if err != nil {
		return "", nil, err
	}
	return id, state, nil
}

// Patch implements Runner.
func (r *ExecRunner) Patch(ctx context.Context, token, pkg, id string, props map[string]any) (map[string]any, error) {
	out, err := r.runWithInput(ctx, token, pkg, "patch", id, props)
	if err != nil {
		return nil, err
	}
	state, err := lastJSONObject(out)
	if err != nil {
		return nil, fmt.Errorf("parsing patch output: %w (output: %s)", err, truncate(out, 2000))
	}
	return state, nil
}

// Read implements Runner.
func (r *ExecRunner) Read(ctx context.Context, token, pkg, id string) (map[string]any, error) {
	out, err := r.run(ctx, doArgs(token, pkg, "read", id, ""))
	if err != nil {
		return nil, err
	}
	state, err := lastJSONObject(out)
	if err != nil {
		return nil, fmt.Errorf("parsing read output: %w (output: %s)", err, truncate(out, 2000))
	}
	return state, nil
}

// Delete implements Runner.
func (r *ExecRunner) Delete(ctx context.Context, token, pkg, id string) error {
	_, err := r.run(ctx, doArgs(token, pkg, "delete", id, ""))
	return err
}

// FetchSchema implements Runner by running `pulumi package get-schema`.
func (r *ExecRunner) FetchSchema(ctx context.Context, pkg, _ string) (*PackageSchema, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	// #nosec G204 -- structured argv, no shell; pkg comes from the validated CR spec.
	cmd := exec.CommandContext(ctx, r.bin(), "package", "get-schema", pkg)
	cmd.Dir = os.TempDir()
	killProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pulumi package get-schema %s: %w: %s", pkg, err, truncate(stderr.String(), 2000))
	}
	var s PackageSchema
	if err := json.Unmarshal(stdout.Bytes(), &s); err != nil {
		return nil, fmt.Errorf("parsing schema for %s: %w", pkg, err)
	}
	return &s, nil
}

func (r *ExecRunner) runWithInput(ctx context.Context, token, pkg, verb, id string, props map[string]any) (string, error) {
	inputFile := ""
	if len(props) > 0 {
		pcl, err := MarshalPCL(props)
		if err != nil {
			return "", err
		}
		f, err := os.CreateTemp("", "doresource-*.pp")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.Remove(f.Name()) }()
		if _, err := f.WriteString(pcl); err != nil {
			_ = f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		inputFile = f.Name()
	}
	return r.run(ctx, doArgs(token, pkg, verb, id, inputFile))
}

func (r *ExecRunner) run(ctx context.Context, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	// #nosec G204 -- structured argv built by doArgs, no shell involved.
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	cmd.Dir = os.TempDir()
	killProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		combined := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		base := fmt.Errorf("pulumi %s: %w: %s", strings.Join(args, " "), err, truncate(combined, 4000))
		if sentinel := classifyText(providerErrorText(combined, "")); sentinel != nil {
			return stdout.String(), fmt.Errorf("%w: %w", sentinel, base)
		}
		return stdout.String(), base
	}
	return stdout.String(), nil
}

// killProcessGroup makes the command run in its own process group and kills
// the whole group on context cancellation: pulumi spawns provider plugin
// subprocesses that would otherwise outlive a timeout and keep mutating
// cloud state after failure was reported.
func killProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 10 * time.Second
}
