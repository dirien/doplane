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
	"sync"
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
		if !ok {
			// Absence means the Secret/key is missing in the Job's
			// namespace; an empty string is a legitimate Secret value.
			return nil, fmt.Errorf("secret input for property %q: environment variable %s is not set (Secret missing in the runner Job's namespace?)", path, envName)
		}
		if err := SetPath(op.Properties, path, value); err != nil {
			return nil, fmt.Errorf("secret input for property %q: %w", path, err)
		}
		if value != "" {
			// Redacting the empty string would corrupt every output.
			values = append(values, value)
		}
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

// redactResult removes secret values from the human-facing message the
// envelope carries toward conditions and events.
//
// res.State and res.Outputs are deliberately NOT redacted. A provider that
// echoes a valuesFrom input into its outputs must round-trip the real value,
// or writeConnectionSecretToRef would publish the literal "(redacted)" into
// the connection Secret and silently break the consuming workload. Structured
// outputs are already sensitive-adjacent in etcd — provider-generated secrets
// land there unredacted too — so the disclosure boundaries that stay enforced
// are the streamed log (redactingWriter), this message, and guardSecretID
// (which still refuses an id embedding a secret, since an id cannot be
// redacted without breaking later operations that need it verbatim).
func redactResult(res *Result, values []string) {
	if len(values) == 0 {
		return
	}
	res.Message = redactString(res.Message, values)
}

// minGuardedSecretLen is the shortest secret value guardSecretID treats as
// "embedded" in a provider id. Below it a substring match is far more likely
// coincidental (a digit, a short region code, a boolean) than the secret
// truly forming the id, and tripping would discard a valid id and orphan a
// successfully created cloud resource. Genuinely identity-forming secrets
// clear this bar; short values must simply not be routed into identity
// properties.
const minGuardedSecretLen = 6

// guardSecretID refuses to emit a provider-assigned id that embeds a
// secret input value: the id would otherwise reach status.id, events and
// logs verbatim (it cannot be redacted — later operations need it exactly).
// Identity-forming properties (names, prefixes) must not come from
// valuesFrom. The replacement failure carries no id and no value.
func guardSecretID(res Result, values []string) Result {
	if res.ID == "" {
		return res
	}
	guarded := make([]string, 0, len(values))
	for _, v := range values {
		if len(v) >= minGuardedSecretLen {
			guarded = append(guarded, v)
		}
	}
	if len(guarded) == 0 || redactString(res.ID, guarded) == res.ID {
		return res
	}
	return failure(CodeSecretInputInID,
		"the provider-assigned id embeds a secret input value and cannot be recorded; "+
			"identity-forming properties (names, prefixes) must not come from valuesFrom. "+
			"The external resource may have been created and can need manual cleanup")
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

// syncWriter serializes concurrent writes to an underlying writer. The two
// per-stream redactors of one operation share a syncWriter so their whole-line
// writes to the pod log / dev console never interleave mid-line or race — the
// destination (os.Stderr, a buffer, …) need not be safe for concurrent use.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
