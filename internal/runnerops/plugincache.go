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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvPluginCache carries the mount path of the shared, writable plugin
// cache (a PVC in-cluster) into the runner process. When set, pinned
// provider plugins are installed into the cache once and reused by every
// later operation, so new providers need YAML changes only — no runner
// image rebuild.
const EnvPluginCache = "DOPLANE_PLUGIN_CACHE"

// staleLockAge is when an install lock left by a dead runner pod is broken.
// Runner Jobs are bounded by activeDeadlineSeconds (default 10m), so any
// older lock cannot have a live owner.
const staleLockAge = 15 * time.Minute

// ParsePluginRef splits a plain package reference "name@version" into the
// plugin coordinates `pulumi plugin install resource` expects. Unpinned,
// git and private-registry references return ok=false: they are not
// cacheable ahead of the operation.
func ParsePluginRef(pkg string) (name, version string, ok bool) {
	if kind, _ := ClassifyPackageRef(pkg); kind != PkgKindPlain {
		return "", "", false
	}
	name, version, found := strings.Cut(pkg, "@")
	if !found || name == "" || version == "" {
		return "", "", false
	}
	return name, version, true
}

// pluginDir is where pulumi materializes an installed resource plugin
// inside a PULUMI_HOME.
func pluginDir(home, name, version string) string {
	return filepath.Join(home, "plugins", fmt.Sprintf("resource-%s-v%s", name, version))
}

// ensurePlugin makes the pinned resource plugin available in the shared
// cache, downloading it at most once cluster-wide: concurrent runners
// serialize on an atomic directory lock on the cache volume (no Kubernetes
// API access needed), and locks left by dead pods are broken after
// staleLockAge.
func (r *Runner) ensurePlugin(ctx context.Context, cache, name, version string) error {
	if installed(pluginDir(cache, name, version)) {
		return nil
	}

	lock := filepath.Join(cache, ".locks", name+"@"+version+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o750); err != nil {
		return &cacheError{code: CodePluginCacheNotWritable, err: fmt.Errorf("preparing plugin cache locks: %w", err)}
	}
	for {
		err := os.Mkdir(lock, 0o750)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return &cacheError{code: CodePluginCacheNotWritable, err: fmt.Errorf("acquiring plugin install lock: %w", err)}
		}
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > staleLockAge {
			// The owner is gone (Jobs cannot outlive their deadline);
			// removal races between waiters are fine — Mkdir stays atomic.
			_ = os.Remove(lock)
			continue
		}
		select {
		case <-ctx.Done():
			return &cacheError{code: CodePluginInstall, err: fmt.Errorf("waiting for plugin install lock: %w", ctx.Err())}
		case <-time.After(2 * time.Second):
		}
	}
	defer func() { _ = os.Remove(lock) }()

	// Another runner may have finished the install while this one waited.
	if installed(pluginDir(cache, name, version)) {
		return nil
	}
	if _, err := r.runEnv(ctx, cache, []string{"PULUMI_HOME=" + cache, "PULUMI_SKIP_UPDATE_CHECK=true"},
		"plugin", "install", "resource", name, version); err != nil {
		return &cacheError{code: CodePluginInstall, err: fmt.Errorf("installing plugin %s@%s: %w", name, version, err)}
	}
	return nil
}

// installed reports whether a plugin directory holds a completed install
// (pulumi marks in-flight downloads with a ".partial" sibling).
func installed(dir string) bool {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	if _, err := os.Stat(dir + ".partial"); err == nil {
		return false
	}
	return true
}

// cacheError carries the typed failure code for plugin cache problems so
// Execute can surface them as condition reasons.
type cacheError struct {
	code string
	err  error
}

func (e *cacheError) Error() string { return e.err.Error() }
func (e *cacheError) Unwrap() error { return e.err }
