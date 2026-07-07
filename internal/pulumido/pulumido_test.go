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
	"errors"
	"testing"

	"github.com/dirien/doplane/internal/runnerops"
)

func TestResultErr(t *testing.T) {
	if err := resultErr(runnerops.Result{OK: true}); err != nil {
		t.Fatalf("success must map to nil, got %v", err)
	}

	err := resultErr(runnerops.Result{OK: false, Code: runnerops.CodeNotFound, Message: "gone"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("NotFound code must map to ErrNotFound: %v", err)
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != runnerops.CodeNotFound {
		t.Errorf("coded error must be preserved: %v", err)
	}

	err = resultErr(runnerops.Result{OK: false, Code: runnerops.CodeReadNotSupported, Message: "no import"})
	if !errors.Is(err, ErrReadNotSupported) {
		t.Errorf("ReadNotSupported code must map to sentinel: %v", err)
	}

	err = resultErr(runnerops.Result{OK: false, Code: runnerops.CodeRegistryAuthMissing, Message: "set the token"})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrReadNotSupported) {
		t.Errorf("other codes must not map to sentinels: %v", err)
	}
	if !errors.As(err, &coded) || coded.Code != runnerops.CodeRegistryAuthMissing {
		t.Errorf("coded error expected: %v", err)
	}
}

func TestDecodeEnvelope(t *testing.T) {
	out := "Downloading provider\nsome progress\n" +
		`{"ok":true,"id":"urn:pulumi:dev::doplane::t::res","outputs":{"dns":"svc:8080"},"engineState":{"version":3}}` + "\n"
	res, err := decodeEnvelope(out)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.ID != "urn:pulumi:dev::doplane::t::res" || res.Outputs["dns"] != "svc:8080" {
		t.Errorf("envelope: %+v", res)
	}

	if _, err := decodeEnvelope(`{"id":"not-an-envelope"}`); err == nil {
		t.Error("non-envelope JSON must be rejected")
	}
	if _, err := decodeEnvelope("no json at all"); err == nil {
		t.Error("missing envelope must error")
	}
}

func TestClassifyInfraFailure(t *testing.T) {
	if err := classifyInfraFailure("error: api error NoSuchBucket: does not exist", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if err := classifyInfraFailure("This will delete \"my-404-bucket\"", ""); err != nil {
		t.Errorf("command echoes must not classify: %v", err)
	}
	if err := classifyInfraFailure("", "error: Resource Import Not Implemented"); !errors.Is(err, ErrReadNotSupported) {
		t.Errorf("fail message classification: %v", err)
	}
}

func TestEngineStateJSON(t *testing.T) {
	if got, err := engineStateJSON(nil); err != nil || got != nil {
		t.Errorf("empty state: %v %v", got, err)
	}
	if _, err := engineStateJSON(make([]byte, runnerops.MaxEngineStateBytes+1)); err == nil {
		t.Error("oversized state must be rejected")
	}
}
