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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingRunner counts FetchSchema invocations and blocks until released.
type countingRunner struct {
	calls   atomic.Int32
	release chan struct{}
	err     error
}

func (c *countingRunner) Create(context.Context, string, string, map[string]any) (string, map[string]any, error) {
	return "", nil, errors.New("not implemented")
}
func (c *countingRunner) Patch(context.Context, string, string, string, map[string]any) (map[string]any, error) {
	return nil, errors.New("not implemented")
}
func (c *countingRunner) Read(context.Context, string, string, string) (map[string]any, error) {
	return nil, errors.New("not implemented")
}
func (c *countingRunner) Delete(context.Context, string, string, string) error {
	return errors.New("not implemented")
}

func (c *countingRunner) CreateComponent(context.Context, string, string, map[string]any) (string, map[string]any, []byte, error) {
	return "", nil, nil, errors.New("not implemented")
}

func (c *countingRunner) UpdateComponent(context.Context, string, string, map[string]any, []byte) (map[string]any, []byte, error) {
	return nil, nil, errors.New("not implemented")
}

func (c *countingRunner) DeleteComponent(context.Context, string, string, []byte) error {
	return errors.New("not implemented")
}
func (c *countingRunner) FetchSchema(ctx context.Context, pkg, token string) (*PackageSchema, error) {
	c.calls.Add(1)
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.err != nil {
		return nil, c.err
	}
	return &PackageSchema{Name: pkg}, nil
}

func TestSchemaCacheSingleflight(t *testing.T) {
	runner := &countingRunner{release: make(chan struct{})}
	cache := NewSchemaCache(runner)
	ctx := context.Background()

	const waiters = 8
	var wg sync.WaitGroup
	results := make([]*PackageSchema, waiters)
	for i := range waiters {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := cache.Get(ctx, "aws", "aws:s3/bucketV2:BucketV2")
			if err != nil {
				t.Errorf("waiter %d: %v", i, err)
				return
			}
			results[i] = s
		}(i)
	}
	// Give the goroutines a moment to pile onto the same key.
	time.Sleep(50 * time.Millisecond)
	close(runner.release)
	wg.Wait()

	if got := runner.calls.Load(); got != 1 {
		t.Errorf("want exactly 1 fetch, got %d", got)
	}
	for i, s := range results {
		if s == nil || s.Name != "aws" {
			t.Errorf("waiter %d got %v", i, s)
		}
	}
	// Cached: no further fetches.
	if _, err := cache.Get(ctx, "aws", "aws:s3/bucketV2:BucketV2"); err != nil {
		t.Fatal(err)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Errorf("cache miss after success: %d fetches", got)
	}
}

func TestSchemaCacheErrorNotCached(t *testing.T) {
	runner := &countingRunner{err: errors.New("boom")}
	cache := NewSchemaCache(runner)
	ctx := context.Background()

	if _, err := cache.Get(ctx, "aws", "t"); err == nil {
		t.Fatal("want error")
	}
	runner.err = nil
	if _, err := cache.Get(ctx, "aws", "t"); err != nil {
		t.Fatalf("failed fetch must not be cached: %v", err)
	}
	if got := runner.calls.Load(); got != 2 {
		t.Errorf("want 2 fetches, got %d", got)
	}
}

func TestJobNameDeterminism(t *testing.T) {
	r := &JobRunner{}
	ctxA := WithOwner(context.Background(), "default/bucket-a")
	ctxB := WithOwner(context.Background(), "default/bucket-b")

	n1 := r.jobName(ctxA, "create", `{"verb":"create"}`)
	n2 := r.jobName(ctxA, "create", `{"verb":"create"}`)
	if n1 != n2 {
		t.Errorf("same owner+op must reuse the job name: %q vs %q", n1, n2)
	}
	if r.jobName(ctxB, "create", `{"verb":"create"}`) == n1 {
		t.Error("different owners must not share job names")
	}
	if r.jobName(ctxA, "create", `{"verb":"create","id":"x"}`) == n1 {
		t.Error("different ops must not share job names")
	}
	// No owner: random names for mutations, deterministic for schema.
	anon := context.Background()
	anonCreate1 := r.jobName(anon, "create", "op")
	anonCreate2 := r.jobName(anon, "create", "op")
	if anonCreate1 == anonCreate2 {
		t.Error("anonymous mutations must get unique names")
	}
	schema1 := r.jobName(anon, "schema", "op")
	schema2 := r.jobName(anon, "schema", "op")
	if schema1 != schema2 {
		t.Error("schema fetches are deterministic and shareable")
	}
}
