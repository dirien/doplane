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
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePluginRef(t *testing.T) {
	tests := []struct {
		pkg     string
		name    string
		version string
		ok      bool
	}{
		{"aws@7.34.0", "aws", "7.34.0", true},
		{"digitalocean@4.73.0", "digitalocean", "4.73.0", true},
		{"aws", "", "", false},                         // unpinned: keep on-demand behavior
		{"", "", "", false},                            // no package at all
		{"aws@", "", "", false},                        // empty version
		{"private/org/name", "", "", false},            // registry ref
		{"https://github.com/org/repo", "", "", false}, // git ref
	}
	for _, tt := range tests {
		name, version, ok := ParsePluginRef(tt.pkg)
		if name != tt.name || version != tt.version || ok != tt.ok {
			t.Errorf("ParsePluginRef(%q) = %q, %q, %t; want %q, %q, %t",
				tt.pkg, name, version, ok, tt.name, tt.version, tt.ok)
		}
	}
}

func TestEnsurePluginSkipsInstalled(t *testing.T) {
	cache := t.TempDir()
	if err := os.MkdirAll(pluginDir(cache, "random", "4.16.0"), 0o750); err != nil {
		t.Fatal(err)
	}
	// PulumiBin "false" fails if invoked — proving the installed plugin
	// short-circuits before any download.
	r := &Runner{PulumiBin: "false", Progress: io.Discard}
	if err := r.ensurePlugin(context.Background(), cache, "random", "4.16.0"); err != nil {
		t.Fatalf("installed plugin must not reinstall: %v", err)
	}
}

func TestEnsurePluginPartialInstallReinstalls(t *testing.T) {
	cache := t.TempDir()
	dir := pluginDir(cache, "random", "4.16.0")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+".partial", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Runner{PulumiBin: "true", Progress: io.Discard}
	if err := r.ensurePlugin(context.Background(), cache, "random", "4.16.0"); err != nil {
		t.Fatalf("partial install must trigger a reinstall attempt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, ".locks")); err != nil {
		t.Errorf("install must go through the lock directory: %v", err)
	}
}

func TestEnsurePluginBreaksStaleLock(t *testing.T) {
	cache := t.TempDir()
	lock := filepath.Join(cache, ".locks", "random@4.16.0.lock")
	if err := os.MkdirAll(lock, 0o750); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleLockAge)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	r := &Runner{PulumiBin: "true", Progress: io.Discard}
	done := make(chan error, 1)
	go func() { done <- r.ensurePlugin(context.Background(), cache, "random", "4.16.0") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stale lock must be broken, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ensurePlugin hung on a stale lock")
	}
}

func TestEnsurePluginHeldLockHonorsContext(t *testing.T) {
	cache := t.TempDir()
	lock := filepath.Join(cache, ".locks", "random@4.16.0.lock")
	if err := os.MkdirAll(lock, 0o750); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := &Runner{PulumiBin: "true", Progress: io.Discard}
	err := r.ensurePlugin(ctx, cache, "random", "4.16.0")
	var ce *cacheError
	if !errors.As(err, &ce) || ce.code != CodePluginInstall {
		t.Fatalf("held lock + cancelled context must fail with %s: %v", CodePluginInstall, err)
	}
}

func TestEnsurePluginUnwritableCache(t *testing.T) {
	cache := t.TempDir()
	if err := os.Chmod(cache, 0o500); err != nil { //nolint:gosec // read-only directory is the fixture under test
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cache, 0o750) }) //nolint:gosec // restore writability so TempDir cleanup works
	r := &Runner{PulumiBin: "true", Progress: io.Discard}
	err := r.ensurePlugin(context.Background(), cache, "random", "4.16.0")
	var ce *cacheError
	if !errors.As(err, &ce) || ce.code != CodePluginCacheNotWritable {
		t.Fatalf("read-only cache must fail with %s: %v", CodePluginCacheNotWritable, err)
	}
}
