<!-- Managed by agent: keep sections and order; edit content, not structure. Last updated: 2026-07-07 -->

# AGENTS.md — workflows

<!-- AGENTS-GENERATED:START overview -->
## Overview
Four workflows. Code gates run on push + pull request; documentation also
supports manual deployment:
| Workflow | Runs | Notes |
|----------|------|-------|
| `lint.yml` | golangci-lint (strict `.golangci.yml`) | zero-tolerance gate |
| `test.yml` | `make test` (unit + envtest) | envtest binaries downloaded by the Makefile |
| `test-e2e.yml` | `make test-e2e` | installs latest kind, creates a throwaway cluster |
| `docs.yml` | `npm ci && npm run docs:build` in `website/` | deploys GitHub Pages from `main`; PRs build without deploying |
<!-- AGENTS-GENERATED:END overview -->

## Setup & environment
- Runners: `ubuntu-latest`; Go from `go.mod` via `go-version-file`
- E2E installs the latest kind release and creates a throwaway cluster
- Documentation uses Node 24 and the lockfile in `website/`

## Build & tests
- Same make targets as local dev: `make lint`, `make test`, `make test-e2e`
- Reproduce documentation builds with `cd website && npm ci && npm run docs:build`
- Reproduce CI failures locally with those targets before pushing

## Code style & conventions
- Workflow names Title Case; job ids kebab-case; minimal `permissions:` block
- Go version comes from `go.mod` — never pin it separately in a workflow

## Security & safety
- No `secrets: inherit` in reusable workflows — pass secrets explicitly
- E2E runs without cloud credentials (kind + credential-free providers); keep
  it that way so forks can run CI
- Prefer pinning third-party actions by SHA when adding workflows

## PR/commit checklist
- [ ] Workflow syntax valid (`actionlint` or GitHub UI)
- [ ] Minimal permissions; no secrets in logs
- [ ] Local `make` target matches what CI runs

## Patterns to Follow
> Copy the shape of the existing three workflows; they are intentionally minimal.

## When stuck
- GitHub Actions docs: https://docs.github.com/en/actions
- Test workflow logic locally via the make targets, not by pushing commits
