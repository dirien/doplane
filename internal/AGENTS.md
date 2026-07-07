<!-- Managed by agent: keep sections and order; edit content, not structure. Last updated: 2026-07-07 -->

# AGENTS.md — internal

<!-- AGENTS-GENERATED:START overview -->
## Overview
Operator core. `pulumido` executes `pulumi do` CRUD operations (as hardened
Kubernetes Jobs in-cluster, local exec in dev); `controller` holds the two
reconcilers. Controllers depend on pulumido **only** through the
`pulumido.Runner` interface — tests substitute a `fakeRunner`.
<!-- AGENTS-GENERATED:END overview -->

<!-- AGENTS-GENERATED:START filemap -->
## Key Files
| File | Purpose |
|------|---------|
| `pulumido/runner.go` | `Runner` interface, shared arg building, CLI-output parsing (`lastJSONObject`), error sentinels (`ErrNotFound`, `ErrReadNotSupported`, `ErrOutputUnavailable`), provider-error classification (`providerErrorText` + `classifyText` — matches only provider `error:` lines, never echoed commands) |
| `pulumido/job.go` | `JobRunner`: one hardened K8s Job per operation. Deterministic Job names from `WithOwner(ctx, …)` → interrupted operations are **adopted** on retry, never re-run; Jobs deleted only after their result is secured |
| `pulumido/exec.go` | `ExecRunner` for `make run` (dev); kills the whole process group on timeout so provider plugins can't outlive it |
| `pulumido/schema.go` | Registry schema fetch + cache with singleflight; `Validate` checks `spec.properties` against `inputProperties`/`requiredInputs` |
| `pulumido/pcl.go` | JSON → PCL serializer for `pulumi do --input-file` (escapes `${` / `%{`) |
| `pulumido/path.go` | Dot/bracket path parser: `a.b[2]`, quoted `tags["dotted.key"]` segments; `SetPath` / `GetPath` / `AppendKeySegment` |
| `controller/doresource_controller.go` | DoResource reconcile: create/patch/read/delete via Runner; hash-based propagation (`status.appliedHash`); live API reads; `persistStatus` detached from cancellation; `GenerationChangedPredicate` on `For()` |
| `controller/references.go` | Graph engine: reference resolution, readiness gating, dependents lookup, blocking teardown (terminating/cycle tie-breaks), cycle detection |
| `controller/composite_render.go` | Expression compiler: `${params.*}`, `${self.*}`, `${resources.*}` → child DoResources; sibling refs compile into `spec.references`; `$${` escapes; unterminated `${` is a render error |
| `controller/docomposite_controller.go` | Composite expansion, owner-checked pruning, child replacement on immutable type change, status roll-up |
<!-- AGENTS-GENERATED:END filemap -->

<!-- AGENTS-GENERATED:START golden-samples -->
## Golden Samples (follow these patterns)
| Pattern | Reference |
|---------|-----------|
| Reconciler shape (conditions, events, terminal vs retryable failure) | `controller/doresource_controller.go` (`markSynced` / `markSyncFailed`) |
| envtest spec with fake runner, multi-step reconcile flow | `controller/references_test.go` |
| Table-driven unit tests | `pulumido/pulumido_test.go` |
| Adding a Runner capability | extend `pulumido/runner.go`, implement in `pulumido/job.go` **and** `pulumido/exec.go`, extend `fakeRunner` in `controller/doresource_controller_test.go` |
<!-- AGENTS-GENERATED:END golden-samples -->

## Setup & environment
- Install deps: `go mod download`; Go version from `go.mod` (1.24)
- envtest binaries auto-download via `make test` (setup-envtest)
- No environment variables required for unit/envtest runs

## Build & tests
- Build: `go build ./...` · Vet: `go vet ./...`
- Test all: `make test` · One package: `go test -race -count=1 ./internal/pulumido/`
- One spec: `go test -race ./internal/controller/ -run TestName`

## Code style & conventions
- Go idioms; errors wrapped with `%w`, lowercase, no trailing punctuation
- `context.Context` first parameter on anything blocking; respect cancellation
- Interfaces defined where used (`Runner` lives in pulumido, consumed by controller)
- Comments state constraints the code can't show — not narration

## Security & safety
- Never log or store credentials; runner pods get creds via Secret envFrom only
- `spec.properties`/outputs may reach etcd — treat as sensitive-adjacent
- Subprocess calls use structured argv (no shell) in exec mode; shell-quoted single args in Job scripts

## Pitfalls (repo-specific)
- **Never blindly re-run cloud mutations.** `ErrOutputUnavailable` means the
  operation likely succeeded but its result is unreadable — retry retrieval
  (Job adoption handles this), never create again.
- envtest reconciler tests call `Reconcile()` directly (no manager): watches,
  predicates and indexes are **not** active; drive multi-step flows with
  explicit repeated `Reconcile` calls (a fresh object typically needs two —
  finalizer first, then the operation).
- Condition reasons are machine API (`WaitingForDependency`,
  `BlockedByDependents`, `CyclicReference`, `ValidationFailed`,
  `ReplacingChildren`, …) — treat renames as breaking changes.
- Strict golangci-lint: every `//nolint` needs a specific linter and a
  justification comment.

## Quality gates
```bash
make lint             # golangci-lint v2, strict config, zero tolerance
go vet ./...
go test -race -count=1 ./internal/...
make test             # includes envtest (downloads binaries on first run)
```

## PR/commit checklist
- [ ] `make lint` clean and `make test` green (race-mode locally: `go test -race ./internal/...`)
- [ ] New Runner behavior implemented in both runners + `fakeRunner`
- [ ] Condition reasons unchanged (or change called out as breaking)
- [ ] `make manifests generate` run if `api/` types or markers changed

## Patterns to Follow
> Prefer real code in this repo over generic examples — see Golden Samples above.

## When stuck
- Read `docs/DESIGN.md` (decisions + risk register) and `README.md`
- Check `examples/` for end-to-end behavior expectations
- Run `go doc <package>` / https://pkg.go.dev for library questions
