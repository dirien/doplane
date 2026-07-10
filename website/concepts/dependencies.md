---
title: Dependencies and composites
description: Wire resources into DAGs and turn those graphs into platform APIs.
---

# Dependencies and composites

<div class="agent-contract">
  <p><strong>Agent goal:</strong> express ordering as data. Use references or composite expressions; do not add sleeps, imperative apply order, or polling scripts.</p>
</div>

## References form the graph

Each `spec.references` entry copies one field from another `DoResource` in the same namespace into a property:

```yaml
spec:
  type: aws:s3/bucketPolicy:BucketPolicy
  references:
    - toPath: bucket
      from:
        name: assets
        fieldPath: status.outputs.bucket
    - toPath: policy
      from:
        name: assets
        fieldPath: status.outputs.arn
      template: '{"Statement":[{"Resource":["${value}","${value}/*"]}]}'
```

The downstream resource waits with `WaitingForDependency` until the source is ready. When the source value changes, the resolved-property hash changes and the downstream resource patches even if its own generation did not change.

Valid source paths are `status.id` and paths below `status.outputs`. `toPath` supports dotted object fields and existing array indexes such as `rules[0].id`.

## Teardown and cycle rules

- References resolve only within one namespace.
- A referenced resource refuses external deletion while dependents remain.
- Deleting a graph therefore proceeds in reverse dependency order.
- A cycle sets terminal conditions on the participating resources; it never breaks the cycle by guessing an order.
- `DoUsage` can declare a dependency that exists outside this reference graph and blocks deletion until removed.

## Composite expressions

A `DoCompositeDefinition` renders templates into child resources. These expressions are available:

| Expression | Resolves to |
| --- | --- |
| `${params.region}` | an input parameter |
| `${self.name}` | composite name |
| `${self.namespace}` | composite namespace |
| `${resources.bucket.id}` | sibling provider ID |
| `${resources.bucket.outputs.arn}` | sibling output |

Sibling expressions compile into normal `spec.references`; they inherit readiness gating, propagation, cycle detection, and ordered teardown.

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoComposite
metadata:
  name: payments-identity
spec:
  definition: pet-identity
  parameters:
    team: payments
```

Every child remains observable with `kubectl get doresources`. Use the composite's `status.resources`, `status.readyResources`, and `status.revision` for the roll-up view.

## Revision safety

Every definition edit creates an immutable `DoCompositeDefinitionRevision`.

- `updatePolicy: Automatic` follows the latest revision.
- `updatePolicy: Manual` stays on its recorded revision.
- `revisionRef` pins one named revision and overrides the policy.

Choose `Manual` or an explicit revision for production APIs that require staged rollout. A definition edit can patch, replace, create, or delete child cloud resources.

## Verification

For a graph change, verify more than aggregate readiness:

```sh
kubectl get docomposite <name> -o yaml
kubectl get doresources -l do.pulumi.com/composite=<name> -o yaml
kubectl get docompositedefinitionrevisions
```

Confirm child ownership, reference compilation, selected revision, and reverse-order deletion behavior.
