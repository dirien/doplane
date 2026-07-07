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
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// workspace is the prepared execution environment of one operation: an
// isolated working directory with a writable PULUMI_HOME (seeded from the
// image's baked plugins), an isolated file backend, and the extra process
// environment every pulumi invocation gets.
type workspace struct {
	root string
	env  []string
}

func (w *workspace) projectDir() string { return filepath.Join(w.root, "project") }

// prepare builds the workspace. bakedPlugins is the image's read-only plugin
// directory ("" to skip seeding).
func prepare(root, bakedPlugins string) (*workspace, error) {
	home := filepath.Join(root, "pulumi-home")
	for _, dir := range []string{filepath.Join(home, "plugins"), filepath.Join(root, "state"), filepath.Join(root, "project")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("preparing workspace: %w", err)
		}
	}
	if bakedPlugins != "" {
		if err := seedPlugins(bakedPlugins, filepath.Join(home, "plugins")); err != nil {
			return nil, err
		}
	}
	env := []string{
		"PULUMI_HOME=" + home,
		"HOME=" + root,
		"PULUMI_SKIP_UPDATE_CHECK=true",
		"PULUMI_CONFIG_PASSPHRASE=",
		"PULUMI_BACKEND_URL=file://" + filepath.Join(root, "state"),
	}
	// The file backend needs an identity for stack metadata; non-root
	// container users often have no passwd entry.
	if os.Getenv("USER") == "" {
		env = append(env, "USER=doplane")
	}
	// Components that target Kubernetes receive their kubeconfig as Secret
	// content; materialize it as a file.
	if kc := os.Getenv("KUBECONFIG_CONTENT"); kc != "" {
		kcPath := filepath.Join(root, "kubeconfig")
		if err := os.WriteFile(kcPath, []byte(kc), 0o600); err != nil {
			return nil, fmt.Errorf("writing kubeconfig: %w", err)
		}
		env = append(env, "KUBECONFIG="+kcPath)
	}
	return &workspace{root: root, env: env}, nil
}

// seedPlugins makes the image's baked plugins visible inside the writable
// plugin cache. Plugin directories must be real directories (pulumi's
// discovery ignores symlinked dirs) so each baked plugin gets a real
// directory with file-level symlinks; baked ".lock" marker files are
// recreated as real writable files.
func seedPlugins(baked, dest string) error {
	entries, err := os.ReadDir(baked)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading baked plugins: %w", err)
	}
	for _, e := range entries {
		src := filepath.Join(baked, e.Name())
		dst := filepath.Join(dest, e.Name())
		switch {
		case e.IsDir():
			if err := os.MkdirAll(dst, 0o750); err != nil {
				return err
			}
			inner, err := os.ReadDir(src)
			if err != nil {
				return err
			}
			for _, f := range inner {
				if err := os.Symlink(filepath.Join(src, f.Name()), filepath.Join(dst, f.Name())); err != nil && !os.IsExist(err) {
					return err
				}
			}
		case strings.HasSuffix(e.Name(), ".lock"):
			if err := os.WriteFile(dst, nil, 0o600); err != nil {
				return err
			}
		default:
			if err := os.Symlink(src, dst); err != nil && !os.IsExist(err) {
				return err
			}
		}
	}
	return nil
}
