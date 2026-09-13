---
title: Day-2 operations
description: Operate drift reads, pauses, adoption, replacement, revisions, and deletion safely.
---

# Day-2 operations

<div class="agent-contract">
  <p><strong>Agent goal:</strong> diagnose from conditions and events before changing the spec. Cloud mutations require explicit intent; never approve replacement or remove protection as a generic retry.</p>
</div>

## Observe one resource

```sh
kubectl get doresource <name> -o wide
kubectl get doresource <name> -o yaml
kubectl describe doresource <name>
kubectl get jobs --all-namespaces --sort-by=.metadata.creationTimestamp
```

`Ready=True` means the external resource exists. `Synced=True` means the latest provider operation succeeded. Read both; a resource can still exist after a failed update.

## Trigger or tune reconciliation

doplane accepts Crossplane-compatible lifecycle annotations:

```sh
# Wake a settled resource. Change the value for each request.
kubectl annotate doresource <name> \
  crossplane.io/reconcile-requested-at="$(date -u +%s)" --overwrite

# Override the default 10-minute drift interval.
kubectl annotate doresource <name> \
  crossplane.io/poll-interval=1m --overwrite

# Suspend cloud calls.
kubectl annotate doresource <name> \
  crossplane.io/paused=true --overwrite
```

Remove the pause annotation to resume. A paused resource does not correct drift or process spec changes.

## Adopt an existing resource

Set the provider ID before the first create:

```sh
kubectl annotate doresource <name> \
  crossplane.io/external-name='<provider-id>'
```

The next read imports observed state. Confirm that the provider implements read/import for this resource type. A wrong ID must fail visibly; do not fall through to create.

## Handle replacement

Immutable input changes stop at `ReplacementRequired`. The default is protected unless `spec.protect` is explicitly `false`.

Review the diff, dependents, deletion policy, and provider identity rules. Then approve only the current generation:

```sh
generation=$(kubectl get doresource <name> -o jsonpath='{.metadata.generation}')
kubectl annotate doresource <name> \
  do.pulumi.com/approve-replacement="$generation" --overwrite
```

doplane creates before deleting when identity permits; otherwise it deletes before creating. A later generation needs separate approval.

## Delete or orphan

`spec.deletionPolicy` defaults to `Delete`. Set `Orphan` before deletion when the cloud resource must survive:

```sh
kubectl patch doresource <name> --type merge \
  -p '{"spec":{"deletionPolicy":"Orphan"}}'
kubectl delete doresource <name>
```

References and `DoUsage` objects can block teardown. Remove dependents in reverse order; do not strip the finalizer while an external resource still needs deletion.

## Roll out a composite revision

Inspect available revisions and the current pin:

```sh
kubectl get docompositedefinitionrevisions
kubectl get docomposite <name> \
  -o jsonpath='{.status.revision}{"\n"}{.spec.updatePolicy}{"\n"}'
```

For a manual rollout, set `spec.revisionRef.name` to the reviewed revision. Verify each rendered child, not only the composite roll-up.

## Upgrade doplane

The chart ships its CRDs in the `crds/` directory, which Helm installs on the first `helm install` and never touches again. A release that adds CRD fields (0.3.0 added `spec.outputs`, `spec.api.additionalPrinterColumns` and `status.outputs` to the composite kinds) therefore needs the CRDs applied by hand before the manager is upgraded; otherwise the apiserver silently prunes the new fields the new manager writes.

```sh
helm pull oci://ghcr.io/dirien/charts/doplane --version <x.y.z> --untar --untardir /tmp/doplane-<x.y.z>
kubectl apply --server-side --force-conflicts -f /tmp/doplane-<x.y.z>/doplane/crds/

helm upgrade doplane oci://ghcr.io/dirien/charts/doplane --version <x.y.z> \
  --namespace doplane-system --reuse-values

kubectl -n doplane-system rollout status deployment/doplane-controller-manager --timeout=3m
```

`--force-conflicts` is needed because Helm registered itself as the field manager of the CRDs it installed; server-side apply refuses to overwrite another manager's fields without it. CRD changes are additive across releases, so existing objects are unaffected. `--reuse-values` keeps the values you set at install time; add `--set` for anything new, for example a platform group for the allowlist.

Running resources are not touched by the upgrade. Runner Jobs use the new runner image from their next operation on, and a node that has not pulled it yet spends its first Job doing so.

```sh
helm list -n doplane-system                              # CHART and APP VERSION match the release
kubectl explain docompositedefinition.spec.outputs       # present once the CRDs are updated
```

## Escalation data

Capture these facts in an issue or agent handoff:

- object YAML with Secret values removed;
- `Ready` and `Synced` reason/message;
- manager and runner image references;
- package pin and full resource type token;
- phase and logs of any still-present runner Job, with credentials redacted — consumed Jobs are deleted, their errors live in the condition message and events;
- whether the operation was create, read, patch, replace, or delete.
