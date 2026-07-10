---
title: Repository context for agents
description: Source map, invariants, commands, and change recipes for coding agents.
---

# Repository context for agents

<div class="agent-contract">
  <p><strong>Agent contract:</strong> read the closest <code>AGENTS.md</code> before editing. Preserve user changes, use the smallest relevant test while iterating, and show command output before claiming success.</p>
</div>

This page is a task map, not a substitute for repository instructions. The root [`AGENTS.md`](https://github.com/dirien/doplane/blob/main/AGENTS.md) has precedence rules, exact commands, and contribution boundaries.

## Source map

| Area | Source | Scoped instructions | Primary checks |
| --- | --- | --- | --- |
| API types and validation | `api/v1alpha1/` | root `AGENTS.md` | `make manifests generate`, `make test` |
| Reconciliation and graphs | `internal/controller/` | `internal/AGENTS.md` | focused envtest, then `make test` |
| Job and exec runners | `internal/pulumido/` | `internal/AGENTS.md` | package tests, then `make test` |
| Shared operation envelope | `internal/runnerops/` | `internal/AGENTS.md` | package tests, race detector |
| Kustomize and Helm | `deploy/` | `deploy/AGENTS.md` | `make helm-lint`, `make helm-template` |
| User examples | `examples/` | root `AGENTS.md` | credential-free examples first |
| Documentation site | `website/` | root `AGENTS.md` | `npm ci`, `npm run docs:build` |
| GitHub workflows | `.github/workflows/` | workflow `AGENTS.md` | syntax plus matching local command |

## Non-negotiable invariants

1. Kubernetes status is the external-resource state store.
2. A cloud mutation must run at most once when its prior result may exist.
3. `DoResource.spec.type` is immutable.
4. Primary-object reads before provider calls use the live API reader.
5. Status persistence survives reconcile cancellation and retries conflicts.
6. The manager never receives provider credentials or executes plugins.
7. Secret values stay out of manifests, events, and logs; structured status remains sensitive-adjacent.
8. References remain same-namespace and enforce reverse-order teardown.
9. Kustomize and Helm behavior stay equivalent.

## Commands

```sh
go build ./...
make fmt
make lint
make test
make helm-lint
make helm-template
```

Use a focused command after each change. Run the full relevant gates before committing a multi-file or shared-code change. E2E creates a cluster and takes several minutes; follow repository authorization rules before running it.

## Change recipes

### Add or change a CRD field

1. Edit the Go type and kubebuilder markers in `api/v1alpha1/`.
2. Update controller behavior and focused tests.
3. Run `make manifests generate`; never hand-edit generated CRDs or RBAC.
4. Confirm the Helm CRD copy stays synchronized.
5. Update examples and this site when user behavior changed.

### Change runner behavior

1. Decide whether the operation contract belongs in `runnerops` or the execution transport belongs in `pulumido`.
2. Preserve deterministic operation identity and adoption.
3. Test cancellation, retries, missing output, and redaction.
4. Verify both in-cluster Job mode and local exec mode where applicable.

### Add a provider example

1. Pin the package version.
2. State whether the example creates billable resources.
3. Keep credential names symbolic and out of YAML values.
4. Prefer a provider profile over repeating package and Secret details.
5. Include cleanup and observable readiness commands.

### Change deployment configuration

1. Read `deploy/AGENTS.md`.
2. Mirror the behavior in Kustomize and Helm.
3. Keep manager and runner images separate.
4. Render both forms and inspect the diff.

## Agent-readable entry points

- [`llms.txt`](https://dirien.github.io/doplane/llms.txt) lists the documentation pages and source context.
- [`llms-full.txt`](https://dirien.github.io/doplane/llms-full.txt) concatenates the technical guides for one-request ingestion.
- The **Markdown for agents** menu on each page copies, opens, or downloads the source Markdown.
