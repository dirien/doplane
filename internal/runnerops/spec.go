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

// Package runnerops implements every operation the operator performs against
// Pulumi — stateless `pulumi do` CRUD, registry schema fetches and
// ephemeral-engine component orchestration — as plain, typed Go. The manager
// runs it in-process in dev mode (ExecRunner); runner Jobs run the same code
// through the pdo-runner binary. The operation spec travels as one JSON
// document and the outcome comes back as one JSON envelope with a typed
// failure code: no shell, no log scraping.
package runnerops

import (
	"encoding/json"
	"fmt"
)

// Verbs accepted by Execute.
const (
	VerbCreate        = "create"
	VerbPatch         = "patch"
	VerbRead          = "read"
	VerbDelete        = "delete"
	VerbSchema        = "schema"
	VerbEngineUp      = "engine-up"
	VerbEngineDestroy = "engine-destroy"
)

// Failure codes carried by Result.Code. They map 1:1 onto condition reasons
// in the operator's status, so `kubectl get` tells the user what actually
// went wrong.
const (
	CodeInvalidSpec         = "InvalidSpec"
	CodeNotFound            = "NotFound"
	CodeReadNotSupported    = "ReadNotSupported"
	CodeRegistryAuthMissing = "RegistryAuthMissing"
	CodeRegistryResolve     = "RegistryResolveFailed"
	CodeSchemaFetch         = "SchemaFetchFailed"
	CodeOperationFailed     = "OperationFailed"
	CodeEngineFailed        = "EngineFailed"
	CodeOutputParse         = "OutputParseFailed"
)

// Op is the single JSON document describing one operation. It reaches the
// pdo-runner binary via the PDO_OP environment variable.
type Op struct {
	Verb        string          `json:"verb"`
	Token       string          `json:"token,omitempty"`
	Package     string          `json:"package,omitempty"`
	ID          string          `json:"id,omitempty"`
	Properties  map[string]any  `json:"properties,omitempty"`
	EngineState json.RawMessage `json:"engineState,omitempty"`
}

// Result is the single JSON envelope emitted for every operation. The
// runner exits 0 whenever it ran to a decision — operation failures travel
// in-band via OK=false and a Code, so Job-level failure is reserved for
// infrastructure problems.
type Result struct {
	OK          bool            `json:"ok"`
	Code        string          `json:"code,omitempty"`
	Message     string          `json:"message,omitempty"`
	ID          string          `json:"id,omitempty"`
	Outputs     map[string]any  `json:"outputs,omitempty"`
	State       map[string]any  `json:"state,omitempty"`
	EngineState json.RawMessage `json:"engineState,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

func failure(code, format string, args ...any) Result {
	return Result{OK: false, Code: code, Message: fmt.Sprintf(format, args...)}
}
