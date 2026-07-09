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
	"io"
	"strings"
	"sync"
	"testing"
)

func TestApplySecretInputs(t *testing.T) {
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			v, ok := env[name]
			return v, ok
		}
	}

	t.Run("sets values at their paths and returns them for redaction", func(t *testing.T) {
		op := &Op{
			Properties:   map[string]any{"name": "db"},
			SecretInputs: map[string]string{"password": "DOPLANE_SECRET_0", "auth.token": "DOPLANE_SECRET_1"},
		}
		values, err := applySecretInputs(op, lookup(map[string]string{
			"DOPLANE_SECRET_0": "s3cr3t",
			"DOPLANE_SECRET_1": "t0k3n",
		}))
		if err != nil {
			t.Fatal(err)
		}
		if op.Properties["password"] != "s3cr3t" {
			t.Errorf("password = %v", op.Properties["password"])
		}
		auth, _ := op.Properties["auth"].(map[string]any)
		if auth["token"] != "t0k3n" {
			t.Errorf("auth.token = %v", auth)
		}
		if len(values) != 2 {
			t.Errorf("values = %v", values)
		}
	})

	t.Run("empty values are legitimate (only absence fails)", func(t *testing.T) {
		op := &Op{SecretInputs: map[string]string{"password": "DOPLANE_SECRET_0"}}
		values, err := applySecretInputs(op, lookup(map[string]string{"DOPLANE_SECRET_0": ""}))
		if err != nil {
			t.Fatalf("an empty Secret value must be accepted: %v", err)
		}
		if op.Properties["password"] != "" {
			t.Errorf("password = %v, want empty string", op.Properties["password"])
		}
		if len(values) != 0 {
			t.Errorf("empty values must not join the redaction set (would corrupt all output): %q", values)
		}
	})

	t.Run("missing env var is a typed failure naming the path, never a value", func(t *testing.T) {
		op := &Op{SecretInputs: map[string]string{"password": "DOPLANE_SECRET_0"}}
		_, err := applySecretInputs(op, lookup(nil))
		if err == nil {
			t.Fatal("missing env must error")
		}
		if !strings.Contains(err.Error(), "password") || !strings.Contains(err.Error(), "DOPLANE_SECRET_0") {
			t.Errorf("error must name path and variable: %v", err)
		}
	})
}

func TestRedaction(t *testing.T) {
	values := []string{"s3cr3t"}

	t.Run("the message is scrubbed but structured outputs round-trip verbatim", func(t *testing.T) {
		res := Result{
			Message: "provider said: s3cr3t is invalid",
			State: map[string]any{
				"password": "s3cr3t",
				"url":      "postgres://app:s3cr3t@db:5432/x",
			},
			Outputs: map[string]any{"token": "s3cr3t"},
		}
		redactResult(&res, values)
		// The message reaches conditions/events, so it must be scrubbed.
		if strings.Contains(res.Message, "s3cr3t") {
			t.Errorf("message leaked: %s", res.Message)
		}
		// State/Outputs are deliberately left intact: a provider echoing a
		// valuesFrom input must round-trip so writeConnectionSecretToRef
		// publishes the real credential, not "(redacted)".
		if res.State["password"] != "s3cr3t" {
			t.Errorf("state.password must round-trip verbatim, got %v", res.State["password"])
		}
		if res.State["url"] != "postgres://app:s3cr3t@db:5432/x" {
			t.Errorf("state.url must round-trip verbatim, got %v", res.State["url"])
		}
		if res.Outputs["token"] != "s3cr3t" {
			t.Errorf("outputs.token must round-trip verbatim, got %v", res.Outputs["token"])
		}
	})

	t.Run("streamed output is scrubbed across split writes", func(t *testing.T) {
		var sink strings.Builder
		w := newRedactingWriter(&sink, values)
		// The secret arrives split across two writes within one line.
		_, _ = w.Write([]byte("+ password: s3c"))
		_, _ = w.Write([]byte("r3t\npartial tail: s3cr3t"))
		_ = w.Flush()
		out := sink.String()
		if strings.Contains(out, "s3cr3t") {
			t.Errorf("stream leaked: %q", out)
		}
		if !strings.Contains(out, redactedMark) {
			t.Errorf("expected redaction marks: %q", out)
		}
	})
}

// TestProgressWritersConcurrentRedaction reproduces the runner's real
// stdout+stderr wiring: os/exec copies the two streams on separate goroutines,
// so the redaction writers must neither race on a shared buffer nor let a
// secret split across the streams. Run under -race.
func TestProgressWritersConcurrentRedaction(t *testing.T) {
	const secret = "s3cr3tP@ssw0rd"
	var sink strings.Builder
	r := &Runner{Progress: &sink, redactSecrets: []string{secret}}
	out, errw, flush := r.progressWriters()

	var wg sync.WaitGroup
	writeLoop := func(w io.Writer, tag string) {
		defer wg.Done()
		for range 500 {
			// Split the secret across two writes within one line: a shared
			// line buffer would let the other stream's newline split it so
			// substring redaction misses, leaking the value verbatim.
			_, _ = io.WriteString(w, tag+" "+secret[:5])
			_, _ = io.WriteString(w, secret[5:]+" trailing\n")
		}
	}
	wg.Add(2)
	go writeLoop(out, "stdout")
	go writeLoop(errw, "stderr")
	wg.Wait()
	flush()

	if got := sink.String(); strings.Contains(got, secret) {
		t.Fatalf("secret leaked into streamed output")
	}
	if !strings.Contains(sink.String(), redactedMark) {
		t.Fatalf("expected redaction marks in output")
	}
}

func TestGuardSecretID(t *testing.T) {
	values := []string{"s3cr3t"}

	t.Run("an id embedding a secret is refused, without the value", func(t *testing.T) {
		res := guardSecretID(Result{OK: true, ID: "s3cr3t-happy-cat"}, values)
		if res.OK || res.Code != CodeSecretInputInID {
			t.Fatalf("id with a secret must fail typed: %+v", res)
		}
		if res.ID != "" {
			t.Errorf("the leaking id must not be emitted: %q", res.ID)
		}
		if strings.Contains(res.Message, "s3cr3t") {
			t.Errorf("failure message leaked the value: %s", res.Message)
		}
	})

	t.Run("clean ids pass through untouched", func(t *testing.T) {
		res := guardSecretID(Result{OK: true, ID: "happy-cat"}, values)
		if !res.OK || res.ID != "happy-cat" {
			t.Fatalf("clean id must pass: %+v", res)
		}
		if res := guardSecretID(Result{OK: true, ID: "s3cr3t-ish"}, nil); !res.OK {
			t.Fatal("no secret inputs means no guard")
		}
	})

	t.Run("a short low-entropy value coincidentally in an id is not guarded", func(t *testing.T) {
		// A single-digit valuesFrom value happens to appear in a numeric
		// provider id. Tripping here would discard a valid id and orphan the
		// just-created resource, so a value below the length floor is ignored.
		res := guardSecretID(Result{OK: true, ID: "301234561"}, []string{"1"})
		if !res.OK || res.ID != "301234561" {
			t.Fatalf("short coincidental match must not orphan a created resource: %+v", res)
		}
	})
}

func TestExecuteRejectsSecretInputsForEngineVerbs(t *testing.T) {
	r := &Runner{PulumiBin: "true"}
	res := r.Execute(t.Context(), Op{
		Verb:         VerbEngineUp,
		Token:        "web-app:index:WebAppComponent",
		SecretInputs: map[string]string{"password": "DOPLANE_SECRET_0"},
	})
	if res.OK || res.Code != CodeInvalidSpec {
		t.Fatalf("engine verbs must refuse secret inputs (checkpoint would persist them): %+v", res)
	}
}
