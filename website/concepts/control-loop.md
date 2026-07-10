---
title: Control loop and state
description: Understand reconciliation, runner Jobs, provider schemas, and the etcd state contract.
---

# Control loop and state

<div class="agent-contract">
  <p><strong>Agent goal:</strong> change reconciliation without violating at-most-once mutations, live-object reads, or status durability. Read <code>internal/AGENTS.md</code> before editing controller or runner code.</p>
</div>

## Reconciliation path

```text
DoResource event
  → resolve provider and references
  → fetch and trim the provider schema
  → validate properties
  → create or adopt one deterministic runner Job
  → execute one typed pulumi do operation
  → persist ID, outputs, hash, generation, and conditions
```

The manager never runs provider plugins. It creates a hardened Job for each operation and reads one machine-readable result envelope. Schema fetches use the same isolated path.

| Kubernetes event | Provider operation |
| --- | --- |
| object created | validate, then `create` |
| resolved properties change | `patch` |
| poll interval expires | `read`; recreate if the resource vanished |
| object deleted | `delete`, unless the policy is `Orphan` |

## State contract

The Kubernetes object is the durable record:

- `status.id` identifies the provider resource.
- `status.outputs` stores the last full provider state.
- `status.appliedHash` detects direct and dependency-driven input changes.
- `status.observedGeneration` records the applied spec generation.
- `status.engineState` stores the checkpoint for component resources.
- `status.conditions` reports `Ready` and `Synced` independently.

doplane runs Pulumi statelessly: the Kubernetes object takes the place of a stack, so there is no separate state backend to operate — and none to recover from. Losing the custom resource or etcd state loses doplane's management record. Back up the cluster accordingly.

## Mutation durability

Runner Jobs have deterministic names derived from the owner and operation input. On retry, the controller adopts an existing Job instead of repeating a cloud mutation. Completed mutation Jobs outlive controller restarts long enough for the manager to consume their result; read Jobs can expire quickly. Once the manager has consumed a result, it deletes the Job — a successful reconcile leaves events and status, not Jobs.

Preserve these rules in code changes:

1. Read the primary object through the live API reader before a cloud call.
2. Never repeat a mutation when its result may already exist.
3. Write status after a successful operation with conflict retry and a context that survives reconcile cancellation.
4. Include input Secret resource versions in operation identity so a rotation cannot adopt a stale result.
5. Keep structured result state intact; redact progress and error channels separately.

## Schema validation

Before any provider call, doplane fetches the package schema and validates `spec.properties` against the resource's `inputProperties` and `requiredInputs`. Unknown fields, missing required inputs, and primitive type mismatches end with `Synced=False` and `ValidationFailed`; no mutation Job runs.

Schemas are cached by package. With the writable plugin cache enabled, a pinned plugin installs once under a per-plugin filesystem lock and later Jobs reuse it.

## Component resources

Component resources — reusable infrastructure authored in any Pulumi language — do not expose stateless CRUD. doplane generates a one-resource program inside the Job, runs an ephemeral engine against a temporary `file://` backend, and persists the exported checkpoint in `status.engineState`. Updates and deletes import that checkpoint, so the whole component ecosystem works without a standing backend.

Component input secrets cannot use `spec.valuesFrom`: the engine checkpoint would persist them. Treat `engineState` as sensitive-adjacent data.
