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

// pdo-runner executes exactly one pulumi-do-operator operation inside a
// runner Job pod. The operation arrives as JSON in the PDO_OP environment
// variable; the outcome leaves as a single JSON envelope on stdout (all
// pulumi progress goes to stderr). The process exits 0 whenever it reached a
// decision — operation failures travel in-band with a typed code — so a
// failed Job always means an infrastructure problem.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dirien/pulumi-do-operator/internal/runnerops"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	raw := os.Getenv("PDO_OP")
	if raw == "" {
		fmt.Fprintln(os.Stderr, "PDO_OP environment variable is required")
		return 3
	}
	var op runnerops.Op
	if err := json.Unmarshal([]byte(raw), &op); err != nil {
		fmt.Fprintf(os.Stderr, "invalid PDO_OP: %v\n", err)
		return 3
	}

	runner := &runnerops.Runner{
		BakedPlugins: os.Getenv("PDO_BAKED_PLUGINS"),
		Progress:     os.Stderr,
	}
	result := runner.Execute(ctx, op)

	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encoding result: %v\n", err)
		return 3
	}
	return 0
}
