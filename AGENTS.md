<!-- FOR AI AGENTS - Human readability is a side effect, not a goal -->
<!-- Managed by agent: keep sections and order; edit content, not structure -->
<!-- Last updated: 2026-07-07 | Last verified: 2026-07-07 -->

# AGENTS.md

**Precedence:** the **closest `AGENTS.md`** to the files you're changing wins. Root holds global defaults only.

## Commands (verified 2026-07-07)
> Source: Makefile + CI (github-actions); verified by scripts/verify-commands (see `.agents/command-verification.json`)

<!-- AGENTS-GENERATED:START commands -->
| Task | Command | ~Time |
|------|---------|-------|
| Build | `go build ./...` | ~5s |
| Lint (strict, zero tolerance) | `make lint` | ~30s |
| Format | `make fmt` | ~2s |
| Test all (unit + envtest; downloads envtest binaries on first run) | `make test` | ~60s first / ~15s after |
| Test one package | `go test -race -count=1 ./internal/pulumido/` | ~5s |
| Test one spec/func | `go test -race ./internal/controller/ -run TestName` | ~10s |
| Regenerate CRDs + RBAC + deepcopy (after editing `api/` types or `+kubebuilder:` markers) | `make manifests generate` | ~10s |
| Build images | `make docker-build docker-build-runner IMG=doplane:dev RUNNER_IMG=doplane-runner:dev` | ~2m |
| Deploy to current cluster | `make install deploy IMG=... RUNNER_IMG=...` | ~30s |
| Helm chart lint/render | `make helm-lint` / `make helm-template` | ~10s |
| E2E on kind (creates cluster) | `make test-e2e` | ~10m |
<!-- AGENTS-GENERATED:END commands -->

> If commands fail, verify against the Makefile (`make help`) or ask user to update.

## Response Style
- Answer first, elaborate only if needed. No sycophantic openers ("Great question!", "Absolutely!").
- For yes/no or status questions, lead with the answer.
- Skip preamble. Match response length to task complexity.

## Workflow
1. **Before coding**: Read nearest `AGENTS.md` + check Golden Samples for the area you're touching
2. **After each change**: Run the smallest relevant check (lint → typecheck → single test)
3. **Before committing**: Run full test suite if changes affect >2 files or touch shared code
4. **Before claiming done**: Run verification and **show output as evidence** — never say "try again", "should work now", "tested", "verified", or "all green" without pasted command output in the same turn

## Heuristics (quick decisions)
<!-- AGENTS-GENERATED:START heuristics -->
| When | Do |
|------|-----|
| Editing CRD types (`api/v1alpha1/`) | Run `make manifests generate` — regenerates CRDs (also synced into the Helm chart), RBAC, deepcopy |
| Adding/changing a `+kubebuilder:rbac` marker | `make manifests`; the ClusterRole lands in `deploy/kustomize/rbac/role.yaml` |
| Changing reconcile logic | envtest specs live next to the controllers (`internal/controller/*_test.go`); they call `Reconcile` directly with a `fakeRunner` |
| Changing runner/Job behavior (`internal/pulumido`) | Unit-test locally, then verify live on kind with `examples/` (levels 1–2 need no cloud creds) |
| Bumping a provider plugin version | Update `Dockerfile.runner` ARGs **and** the pinned `package:` versions in `examples/` and `deploy/kustomize/samples/` |
| Understanding the architecture | Read `README.md` (how it works) and `docs/DESIGN.md` (decisions + risk register) before restructuring |
| Adding dependency | Ask first — we minimize deps |
| Running tasks | Check `make help` for available commands |
<!-- AGENTS-GENERATED:END heuristics -->

## CI/Quality Gates
> Platform: github-actions

### Version Matrix
- Go 1.24.0

### Quality Gates (must pass before merge)
- `golangci-lint`

- Linter configs: `.golangci.yml`
<!-- AGENTS-GENERATED:END ci-rules -->

## Boundaries

### Always Do
- Run pre-commit checks before committing
- Add tests for new code paths
- Use conventional commit format: `type(scope): subject`
- Use **atomic commits** (one logical change per commit); preserve signatures, keep bisection useful
- **Show test output as evidence before claiming work is complete** — never say "try again", "should work now", "tested", "verified", or "all green" without pasted command output
- Before any edit, verify `pwd` resolves inside the intended repo worktree — not `.bare/`, not `~/.claude/skills/…`, not `~/.claude/plugins/cache/…` (those are read-only caches that get clobbered on update)
- For upstream dependency fixes: run **full** test suite, not just affected tests
- Force-push only with `--force-with-lease`
- Follow Go 1.24.0 conventions and idioms

### Ask First
- Adding new dependencies
- Modifying CI/CD configuration
- Changing public API signatures
- Running full e2e test suites
- Repo-wide refactoring or rewrites
- Operations that touch >3 repos (produce a dry-run plan first)

### Never Do
- Commit secrets, credentials, or sensitive data
- Modify vendor/, node_modules/, or generated files
- Push directly to main/master branch — open a PR
- Merge a PR before all review threads are resolved
- Squash commits during merge or rebase unless the user explicitly asked
- Edit installed skill/plugin cache paths (`~/.claude/skills/`, `~/.claude/plugins/cache/`, `**/.bare/**`) — always the source worktree
- Reply to review comments with bare "Addressed" or "Fixed" — cite the resolving commit SHA
- Delete migration files or schema changes
- Use `secrets: inherit` in reusable GitHub Actions workflows (pass secrets explicitly)
- Commit go.sum without go.mod changes

## Contributing (for AI agents)
- **Comprehension**: Understand the problem before submitting code. Read the linked issue, understand *why* the change is needed, not just *what* to change.
- **Context**: Every PR must explain the trade-offs considered and link to the issue it addresses. Disclose AI assistance if the project requires it.
- **Continuity**: Respond to review feedback. Drive-by PRs without follow-up will be closed.

<!-- AGENTS-GENERATED:START module-boundaries -->
## Module Boundaries
> Source: go-conventions

### Internal Packages (compiler-enforced)
- `internal/runnerops` — the typed operation layer both runners execute (op/result envelopes, plugin cache, secret substitution + redaction, PCL/paths). Knows nothing about pulumido or controllers.
- `internal/pulumido` — `pulumi do` execution layer (Runner interface, Job/exec runners, schema cache, ctx tags for tenancy/credentials/secret plans). Knows nothing about controllers.
- `internal/controller` — reconcilers (DoResource graph engine, provider profiles, composites + revisions, typed-CRD translation). Talks to pulumido only through the `Runner` interface.
- `api/v1alpha1` — CRD types only; no behavior beyond validation markers.

### Invariants (do not break)
- The cluster is the state store: external-resource state lives in `status` (etcd). Never re-run a cloud mutation when its result might exist — see `pulumido.ErrOutputUnavailable` and the Job-adoption contract in `internal/pulumido/job.go`.
- `spec.type` on DoResource is immutable (CEL); composites replace children on type change instead of patching.
- Reads of the primary object go through the live API reader, and status writes use `context.WithoutCancel` + conflict retry — keep it that way.
<!-- AGENTS-GENERATED:END module-boundaries -->

## Index of scoped AGENTS.md (MUST read when working in these directories)
<!-- AGENTS-GENERATED:START scope-index -->
- `./internal/AGENTS.md` — operator core: pulumido runner layer + reconcilers, testing patterns, repo-specific pitfalls
- `./deploy/AGENTS.md` — kustomize tree + Helm chart, generated-file rules, image plumbing (IMG/RUNNER_IMG)
- `./.github/workflows/AGENTS.md` — the three CI gates and how to reproduce them locally
<!-- AGENTS-GENERATED:END scope-index -->

> **Agents**: When you read or edit files in a listed directory, you **must** load its AGENTS.md first. It contains directory-specific conventions that override this root file.

## When instructions conflict
The nearest `AGENTS.md` wins. Explicit user prompts override files.
- For Go-specific patterns, defer to language idioms and standard library conventions
