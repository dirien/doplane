---
title: Conditions and failures
description: Interpret Ready and Synced conditions and choose the next diagnostic action.
---

# Conditions and failures

<div class="agent-contract">
  <p><strong>Agent goal:</strong> classify the failing stage from the condition reason before editing code or retrying. Preserve the first actionable provider or validation message in any handoff.</p>
</div>

## Condition model

| Condition | True means | False means |
| --- | --- | --- |
| `Ready` | the external resource exists and current state is recorded | the resource is absent, waiting, deleting, or not yet observed |
| `Synced` | the latest intended provider operation succeeded | validation, resolution, execution, or persistence failed |

Provider profiles also expose `SchemaFetched`, `PluginReady`, and `CredentialsReady`. A profile reaches `Ready=True` only when all required stages are usable.

## Failure groups

| Reason or group | Stage | First checks | Mutation ran? |
| --- | --- | --- | --- |
| `WaitingForDependency` | reference resolution | source name, namespace, readiness, field path | no |
| cycle-related reason | graph validation | all references among reported objects | no |
| `ValidationFailed` | provider schema validation | required, unknown, and primitive-typed properties | no |
| `ProviderPackageMismatch` | provider resolution | `spec.package` versus profile package | no |
| `ResourceNotAllowed` | provider policy | full token and profile `allowedResources` | no |
| `RegistryAuthMissing` | component registry | runner Secret and `PULUMI_ACCESS_TOKEN` | no |
| `RegistryResolveFailed` | component registry | package reference, token scope, network | no |
| plugin install failure | runner preparation | pin, cache permissions, egress, disk | no provider call |
| `CreateFailed`, `UpdateFailed`, `ReadFailed`, or `DeleteFailed` | execution | condition message, runner Job, image pull, provider output | possibly; inspect the retained Job before retrying |
| `EngineFailed` | component engine | runner result and checkpoint handling | possibly |
| `UpdateNotSupported` | provider capability | provider read/patch support | attempted |
| `ReplacementRequired` | lifecycle safety | immutable inputs, protect, generation | patch attempted; replacement did not run |
| delete blocked | dependency safety | references and `DoUsage` objects | no delete |

The exact reason set evolves with controller code. Treat this table as routing guidance and the object condition message as the authoritative detail.

## Diagnostic sequence

```sh
kubectl get doresource <name> -o jsonpath='{range .status.conditions[*]}{.type}{"="}{.status}{" reason="}{.reason}{"\n"}{.message}{"\n\n"}{end}'
kubectl describe doresource <name>
kubectl get jobs --all-namespaces \
  -l do.pulumi.com/owner-name=<name> \
  --sort-by=.metadata.creationTimestamp
```

If labels differ for the current controller version, find recent Jobs by timestamp and owner references. Read runner logs only after checking whether the Job may still be active.

## Retry rules

- Fix terminal input or policy failures before retrying; repeated reconciliation cannot change them.
- Change `crossplane.io/reconcile-requested-at` to wake a settled transient failure.
- Never delete a completed mutation Job merely to force another create, patch, or delete. Its result may be the only proof that the cloud call already ran.
- Never remove a finalizer as a routine recovery step.
- Approve replacement only after reviewing the current generation and blast radius.

## Proving recovery

Recovery requires all relevant evidence:

1. `Synced=True` for the corrected generation.
2. `Ready=True` when the external resource should exist.
3. `status.observedGeneration` equals `metadata.generation`.
4. The provider ID and expected outputs are present.
5. Dependents have propagated any changed output.
