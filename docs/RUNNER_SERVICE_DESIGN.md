# Design: per-provider runner service (warm execution path)

Design for DESIGN.md decision #11: Job-per-operation stays the isolation
default; this document specifies the warm path that keeps large dependency
graph updates from creating hundreds of short-lived pods. Status: designed,
not yet implemented — the Job runner remains the only in-cluster executor.

## Problem

Every `pulumi do` operation today is one Kubernetes Job: pod scheduling,
image pull check, plugin seeding, provider plugin process start — roughly
5–15 s of overhead per operation, and one pod per operation. Propagation
fan-out multiplies this: a shared VPC output change with 500 dependents
means 500 Jobs even though each patch takes the provider milliseconds. The
graph engine (hash propagation) makes this the dominant reconcile cost at
fleet scale.

## Shape

A **per-provider runner Deployment** (`doplane-runner-<provider>`) hosting
the same `runnerops` code behind a small gRPC API, holding the provider
plugin process warm between operations:

```
manager ──(runner selection)──▶ JobRunner            (default, per-op pod)
        └─(provider has service)▶ ServiceRunner ──gRPC──▶ doplane-runner-aws
                                                            └─ warm aws plugin
```

- `service Runner { rpc Execute(Op) returns (Result); }` — the wire types
  are exactly `runnerops.Op`/`runnerops.Result` (the typed envelope that
  already travels through Job env/pod logs), so both runners execute
  identical code and stay behavior-compatible.
- One Deployment per DoProvider that opts in
  (`spec.runner: {mode: Service, replicas: N}`); the DoProvider controller
  owns the Deployment/Service lifecycle the same way it generates typed
  CRDs today.
- The manager-side `ServiceRunner` implements the existing
  `pulumido.Runner` interface; selection happens per operation in the
  provider resolution step (profile says Service and the Deployment is
  Ready → gRPC; otherwise fall back to the JobRunner). Jobs remain the
  fallback and audit path.

## What the warm path must preserve

The Job runner's contracts, restated as service requirements:

- **At-most-once mutations.** Jobs get adoption via deterministic names.
  The service equivalent: `Execute` is keyed by the same owner+op hash
  (idempotency key); the runner keeps a small completed-results LRU so a
  manager retry after a dropped connection re-reads the result instead of
  re-running the mutation. `ErrOutputUnavailable` semantics stay identical.
- **Secret input isolation.** Jobs get kubelet-injected env. The service
  cannot (secrets vary per operation): the manager sends Secret
  *references*; the runner pod resolves them via its own service account,
  scoped by RBAC to named secrets in the runner namespace. Values still
  never transit the manager, and the existing redaction (progress writer,
  result state) applies unchanged.
- **Credential scoping.** One service per provider means one credentials
  Secret per service (envFrom, as Jobs do today). Per-resource-namespace
  credential isolation is incompatible with a shared warm process — tenant
  workloads in resource namespace mode always take the Job path.
- **Pod hardening.** Same non-root/read-only/no-token pod spec as Jobs;
  the plugin cache PVC mounts once and stays warm (no per-op seeding).

## Manager ↔ runner auth

mTLS with certificates minted by the manager (cert-manager optional):
the manager holds the CA; runner Deployments get a serving cert via
Secret; the gRPC client verifies the runner SAN
(`doplane-runner-<provider>.<ns>.svc`). No Kubernetes tokens on either
side of the channel.

## Batching and backpressure

- The service processes operations with a bounded worker pool
  (`replicas × workers`); queue depth is exposed as a metric and in
  `DoProvider.status.runner`.
- Fan-out batching: the manager already serializes per object; the win is
  concurrency across objects. `MaxConcurrentReconciles` stays the global
  knob; the runner's pool is the provider-level knob.
- Overload → gRPC `RESOURCE_EXHAUSTED` → the reconcile requeues with
  backoff (same class as Job-creation failures today).

## Rollout plan

1. Extract `runnerops` service main (`cmd/runner-service`) — same image as
   the Job runner, different entrypoint.
2. `ServiceRunner` implementing `pulumido.Runner` over gRPC with the
   idempotency-key contract and result LRU.
3. DoProvider `spec.runner` + Deployment/Service lifecycle in the provider
   controller; readiness gates selection.
4. Selection in provider resolution with automatic Job fallback; metrics
   for path taken.
5. Soak: run both paths in CI e2e (service for random, Jobs for the rest).

## Why not now

The Job path is correct at current fleet sizes, and every prerequisite the
service needs (typed envelope, shared plugin cache, provider profiles,
redaction) now exists and is service-shaped. The remaining work is pure
plumbing plus the mTLS story — deliberately deferred until reconcile
volume demands it, per the risk-first build order.
