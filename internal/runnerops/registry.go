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
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// registryRefRe matches Pulumi Cloud registry references like
// "ediri/ai-model", "private/ediri/ai-model" or either with "@<version>".
var registryRefRe = regexp.MustCompile(`^(?:(private|pulumi|opentofu)/)?([A-Za-z0-9-]+)/([A-Za-z0-9-]+)(?:@(.+))?$`)

// looksLikeGitSource reports whether pkg is a git-style package source
// (resolved by the CLI directly, independent of any backend).
func looksLikeGitSource(pkg string) bool {
	return strings.HasPrefix(pkg, "github.com/") || strings.HasPrefix(pkg, "gitlab.com/") ||
		strings.HasPrefix(pkg, "bitbucket.org/") || strings.HasPrefix(pkg, "git://") ||
		strings.HasPrefix(pkg, "https://") || strings.HasPrefix(pkg, "./") || strings.HasPrefix(pkg, "/")
}

// Package reference kinds returned by ClassifyPackageRef.
const (
	PkgKindPlain    = "plain"    // classic provider ref like "aws@7.34.0"
	PkgKindGit      = "git"      // resolved directly by `pulumi package add`
	PkgKindRegistry = "registry" // Pulumi Cloud registry reference
)

// ClassifyPackageRef splits a spec.package value into a resolution strategy.
// For registry refs the second return value is the API resource path, e.g.
// "private/ediri/ai-model/versions/0.4.0".
func ClassifyPackageRef(pkg string) (kind, apiPath string) {
	if pkg == "" {
		return PkgKindPlain, ""
	}
	if looksLikeGitSource(pkg) {
		return PkgKindGit, ""
	}
	m := registryRefRe.FindStringSubmatch(pkg)
	if m == nil {
		return PkgKindPlain, ""
	}
	source := m[1]
	if source == "" {
		source = "private" // the internal registry is the default for org/name refs
	}
	version := m[4]
	if version == "" {
		version = "latest" // the API only serves versioned paths
	}
	path := fmt.Sprintf("%s/%s/%s/versions/%s", source, m[2], m[3], version)
	return PkgKindRegistry, path
}

// registryPackage is the subset of the registry version API we consume.
type registryPackage struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	SchemaURL         string `json:"schemaURL"`
	PluginDownloadURL string `json:"pluginDownloadURL"`
}

// registryClient talks to the Pulumi Cloud registry API.
type registryClient struct {
	apiURL string
	token  string
	http   *http.Client
}

func newRegistryClient() (*registryClient, error) {
	token := os.Getenv("PULUMI_ACCESS_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("PULUMI_ACCESS_TOKEN is not set; add it to the provider-credentials Secret to use private registry packages")
	}
	api := os.Getenv("PULUMI_API")
	if api == "" {
		api = "https://api.pulumi.com"
	}
	return &registryClient{
		apiURL: strings.TrimRight(api, "/"),
		token:  token,
		http:   &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// resolve fetches the registry metadata for an API path from
// ClassifyPackageRef.
func (c *registryClient) resolve(ctx context.Context, apiPath string) (*registryPackage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.apiURL+"/api/preview/registry/packages/"+apiPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry API request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("registry API returned %s for %s: %s", resp.Status, apiPath, strings.TrimSpace(string(body)))
	}
	var pkg registryPackage
	if err := json.NewDecoder(resp.Body).Decode(&pkg); err != nil {
		return nil, fmt.Errorf("decoding registry response: %w", err)
	}
	return &pkg, nil
}

// gitSource returns the CLI-resolvable source for the package (registry
// packages publish their source as a git:// URL).
func (p *registryPackage) gitSource() (string, error) {
	if p.PluginDownloadURL == "" {
		return "", fmt.Errorf("registry package %s@%s has no resolvable source", p.Name, p.Version)
	}
	return strings.TrimPrefix(p.PluginDownloadURL, "git://"), nil
}

// fetchSchema downloads the package schema from its presigned URL. The
// registry stores schemas gzip-compressed; both encoded and plain responses
// are handled.
func (c *registryClient) fetchSchema(ctx context.Context, p *registryPackage) ([]byte, error) {
	if p.SchemaURL == "" {
		return nil, fmt.Errorf("registry package %s@%s has no schema URL", p.Name, p.Version)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.SchemaURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("schema download failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schema download returned %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(strings.NewReader(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("decompressing schema: %w", err)
		}
		defer func() { _ = zr.Close() }()
		return io.ReadAll(zr)
	}
	return raw, nil
}
