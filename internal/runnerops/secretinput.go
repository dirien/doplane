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
	"fmt"
	"io"
	"sort"
	"strings"
)

// redactedMark replaces secret input values wherever they would otherwise
// surface: streamed runner output, error messages and recorded state.
const redactedMark = "(redacted)"

// applySecretInputs materializes the op's secret input mapping: each
// property path receives the value of its environment variable (injected
// out of band — as a kubelet-resolved secretKeyRef env in Jobs). It
// returns the resolved values so every output channel can be redacted.
// Error messages carry paths and variable names, never values.
func applySecretInputs(op *Op, lookup func(string) (string, bool)) ([]string, error) {
	if len(op.SecretInputs) == 0 {
		return nil, nil
	}
	if op.Properties == nil {
		op.Properties = map[string]any{}
	}
	paths := make([]string, 0, len(op.SecretInputs))
	for path := range op.SecretInputs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		envName := op.SecretInputs[path]
		value, ok := lookup(envName)
		if !ok || value == "" {
			return nil, fmt.Errorf("secret input for property %q: environment variable %s is not set (Secret missing in the runner Job's namespace?)", path, envName)
		}
		if err := SetPath(op.Properties, path, value); err != nil {
			return nil, fmt.Errorf("secret input for property %q: %w", path, err)
		}
		values = append(values, value)
	}
	return values, nil
}

// redactString removes every secret value occurrence from s.
func redactString(s string, values []string) string {
	for _, v := range values {
		s = strings.ReplaceAll(s, v, redactedMark)
	}
	return s
}

// redactAny walks a decoded JSON value, redacting secret values inside
// every string (including substrings, e.g. connection URLs embedding a
// password).
func redactAny(v any, values []string) any {
	switch t := v.(type) {
	case string:
		return redactString(t, values)
	case []any:
		for i := range t {
			t[i] = redactAny(t[i], values)
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = redactAny(t[k], values)
		}
		return t
	default:
		return v
	}
}

// redactResult removes secret values from everything the envelope carries
// back toward etcd, conditions and events. The ID is deliberately left
// verbatim: later operations need it exactly as the provider issued it.
func redactResult(res *Result, values []string) {
	if len(values) == 0 {
		return
	}
	res.Message = redactString(res.Message, values)
	if res.State != nil {
		res.State, _ = redactAny(res.State, values).(map[string]any)
	}
	if res.Outputs != nil {
		res.Outputs, _ = redactAny(res.Outputs, values).(map[string]any)
	}
}

// redactingWriter strips secret values from streamed output (pod logs /
// dev console) line by line — `pulumi do` echoes its inputs, so raw
// streaming would leak the substituted values.
type redactingWriter struct {
	dst    io.Writer
	values []string
	buf    bytes.Buffer
}

func newRedactingWriter(dst io.Writer, values []string) *redactingWriter {
	return &redactingWriter{dst: dst, values: values}
}

func (w *redactingWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			// Backstop for pathological newline-free output: flush once the
			// buffer is large; a secret split exactly on this boundary
			// could escape, but provider output is line-oriented.
			if w.buf.Len() > 1<<20 {
				return len(p), w.Flush()
			}
			return len(p), nil
		}
		line := string(w.buf.Next(i + 1))
		if _, err := io.WriteString(w.dst, redactString(line, w.values)); err != nil {
			return len(p), err
		}
	}
}

// Flush writes any buffered partial line (redacted). Call once the
// producing process has exited.
func (w *redactingWriter) Flush() error {
	if w.buf.Len() == 0 {
		return nil
	}
	rest := w.buf.String()
	w.buf.Reset()
	_, err := io.WriteString(w.dst, redactString(rest, w.values))
	return err
}
