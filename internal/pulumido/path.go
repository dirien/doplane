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

import "github.com/dirien/doplane/internal/runnerops"

// The path helpers live in runnerops (the runner substitutes secret input
// values by property path); these aliases keep the established pulumido API
// for the controllers.

// SetPath sets value at a dot path inside props; see runnerops.SetPath.
func SetPath(props map[string]any, path string, value any) error {
	return runnerops.SetPath(props, path, value)
}

// GetPath reads the value at a dot path inside v; see runnerops.GetPath.
func GetPath(v any, path string) (any, bool) {
	return runnerops.GetPath(v, path)
}

// AppendKeySegment appends a map key to a path; see
// runnerops.AppendKeySegment.
func AppendKeySegment(base, key string) string {
	return runnerops.AppendKeySegment(base, key)
}

// RenderScalar renders a resolved value for use inside a string template;
// see runnerops.RenderScalar.
func RenderScalar(v any) string {
	return runnerops.RenderScalar(v)
}
