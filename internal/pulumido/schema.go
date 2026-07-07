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
	"sort"
	"sync"

	"github.com/dirien/pulumi-do-operator/internal/runnerops"
)

// PropertySchema is the subset of a Pulumi schema property definition needed
// for input validation.
type PropertySchema struct {
	Type                 string           `json:"type"`
	Ref                  string           `json:"$ref"`
	Items                *PropertySchema  `json:"items"`
	AdditionalProperties *PropertySchema  `json:"additionalProperties"`
	OneOf                []PropertySchema `json:"oneOf"`
}

// ResourceSchema describes one resource type from a provider schema.
type ResourceSchema struct {
	InputProperties map[string]PropertySchema `json:"inputProperties"`
	RequiredInputs  []string                  `json:"requiredInputs"`
	// IsComponent marks component resources: they are orchestrated through
	// an ephemeral engine (Construct) rather than stateless CRUD.
	IsComponent bool `json:"isComponent"`
}

// PackageSchema is the subset of a provider's registry schema we consume.
type PackageSchema struct {
	Name      string                    `json:"name"`
	Version   string                    `json:"version"`
	Resources map[string]ResourceSchema `json:"resources"`
}

// SchemaCache memoizes provider schemas fetched from the Pulumi registry via
// a Runner, keyed by package and resource token. Concurrent misses on the
// same key share one fetch (fetches are heavyweight — a Kubernetes Job in
// cluster mode).
type SchemaCache struct {
	runner Runner

	mu      sync.Mutex
	cache   map[string]*PackageSchema
	pending map[string]*schemaFetch
}

// schemaFetch is one in-flight fetch that waiters can share.
type schemaFetch struct {
	done chan struct{}
	s    *PackageSchema
	err  error
}

// NewSchemaCache returns a ready-to-use cache backed by the given runner.
func NewSchemaCache(runner Runner) *SchemaCache {
	return &SchemaCache{
		runner:  runner,
		cache:   map[string]*PackageSchema{},
		pending: map[string]*schemaFetch{},
	}
}

// PackageForToken derives the package name from a type token like
// "aws:s3/bucketV2:BucketV2".
func PackageForToken(token string) string {
	return runnerops.PackageForToken(token)
}

// Get returns a schema for pkg ("name" or "name@version") that covers token.
// The first caller for a key performs the fetch; concurrent callers wait for
// its result. Failed fetches are not cached — the next caller retries.
func (c *SchemaCache) Get(ctx context.Context, pkg, token string) (*PackageSchema, error) {
	key := pkg + "|" + token

	c.mu.Lock()
	if s, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return s, nil
	}
	if fl, ok := c.pending[key]; ok {
		c.mu.Unlock()
		select {
		case <-fl.done:
			return fl.s, fl.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fl := &schemaFetch{done: make(chan struct{})}
	c.pending[key] = fl
	c.mu.Unlock()

	fl.s, fl.err = c.runner.FetchSchema(ctx, pkg, token)

	c.mu.Lock()
	if fl.err == nil {
		c.cache[key] = fl.s
	}
	delete(c.pending, key)
	c.mu.Unlock()
	close(fl.done)
	return fl.s, fl.err
}

// Validate checks props against the schema of the given resource token.
// It returns a list of human-readable violations; an empty list means valid.
func (s *PackageSchema) Validate(token string, props map[string]any) ([]string, error) {
	res, ok := s.Resources[token]
	if !ok {
		return nil, fmt.Errorf("resource type %q not found in schema of package %s@%s",
			token, s.Name, s.Version)
	}
	var violations []string
	for _, req := range res.RequiredInputs {
		if _, ok := props[req]; !ok {
			violations = append(violations, fmt.Sprintf("required property %q is missing", req))
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ps, ok := res.InputProperties[k]
		if !ok {
			violations = append(violations, fmt.Sprintf("unknown property %q (not an input of %s)", k, token))
			continue
		}
		violations = append(violations, checkType(k, props[k], &ps, 0)...)
	}
	return violations, nil
}

func checkType(path string, v any, ps *PropertySchema, depth int) []string {
	if v == nil || depth > 16 {
		return nil
	}
	// $ref and oneOf point at named object types or unions; accept them
	// without deep validation — the provider performs full checking.
	if ps.Ref != "" || len(ps.OneOf) > 0 {
		return nil
	}
	switch ps.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return []string{fmt.Sprintf("property %q must be a string, got %s", path, jsonKind(v))}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return []string{fmt.Sprintf("property %q must be a boolean, got %s", path, jsonKind(v))}
		}
	case "integer", "number":
		switch v.(type) {
		case json.Number, float64, int64, int:
		default:
			return []string{fmt.Sprintf("property %q must be a %s, got %s", path, ps.Type, jsonKind(v))}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return []string{fmt.Sprintf("property %q must be an array, got %s", path, jsonKind(v))}
		}
		if ps.Items == nil {
			return nil
		}
		var out []string
		for i, e := range arr {
			out = append(out, checkType(fmt.Sprintf("%s[%d]", path, i), e, ps.Items, depth+1)...)
		}
		return out
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("property %q must be an object, got %s", path, jsonKind(v))}
		}
		if ps.AdditionalProperties == nil {
			return nil
		}
		var out []string
		names := make([]string, 0, len(obj))
		for k := range obj {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			out = append(out, checkType(path+"."+k, obj[k], ps.AdditionalProperties, depth+1)...)
		}
		return out
	}
	return nil
}

func jsonKind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}
