---
title: Platform APIs
description: Publish a composite definition as a branded, versioned, schema-validated Kubernetes API and run its lifecycle.
---

# Platform APIs

<div class="agent-contract">
  <p><strong>Agent goal:</strong> publish or evolve a typed platform API without stranding its objects. Never rename a served group/kind/plural; that path is a new definition. Read the <code>APIServed</code> condition before editing anything.</p>
</div>

A `DoCompositeDefinition` with `spec.api` is served as a real Kubernetes API: apply the definition and doplane generates a CRD such as `websites.platform.acme.com/v1`. Consumers get admission validation, `kubectl explain`, printer columns, and per-kind RBAC. The typed kind is what a platform team publishes; the `DoComposite` and child `DoResource`s underneath stay visible for debugging.

The platform team owns one definition: resource templates, one parameter schema, and `api: {group, version, kind}`. The app team applies the platform kind, with parameters flat in `spec` and doplane's lifecycle knobs under the reserved `spec.doplane` block. That object is the only thing app teams need to learn.

## Publish an API

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoCompositeDefinition
metadata:
  name: pet-identity
spec:
  api:
    kind: PetIdentity
    # group: platform.example.com   # optional; requires the allowlist below
    # version: v1                   # optional; defaults to v1alpha1
    parametersSchema:
      type: object
      required: [team]
      properties:
        team:
          type: string
          minLength: 1
  resources:
    - name: pet
      type: random:index/randomPet:RandomPet
      providerRef:
        name: random-catalog
      properties:
        length: 2
        prefix: ${params.team}
```

The definition's status reports whether the API is served and, if not, exactly why:

```sh
kubectl get docd pet-identity          # SERVED / REASON printer columns
kubectl wait --for=condition=Established \
  crd/petidentities.typed.do.pulumi.com --timeout=1m
```

The generated CRD appears only after the definition reconciles, so a single file containing both the definition and a typed object needs a second `kubectl apply` on first use.

## Consume it

```yaml
apiVersion: typed.do.pulumi.com/v1alpha1
kind: PetIdentity
metadata:
  name: payments-identity
spec:
  team: payments
  doplane:              # reserved block: doplane's knobs, not a parameter
    updatePolicy: Manual
    revisionRef:
      name: pet-identity-v3
```

The apiserver enforces the parameter schema at admission: a missing `team` or a wrong type is rejected before anything reconciles. `spec.doplane` maps to the mirror composite's `updatePolicy` and `revisionRef`, so typed users get the same lifecycle controls raw `DoComposite`s have. A definition declaring a parameter named `doplane` is rejected (`InvalidSchema`).

## Choose a group

`spec.api.group` brands the API (`platform.acme.com` instead of the default `typed.do.pulumi.com`). Groups are gated by an install-time allowlist because the manager's RBAC must enumerate every group it writes typed objects in:

```sh
helm upgrade doplane oci://ghcr.io/dirien/charts/doplane \
  --namespace doplane-system --reuse-values \
  --set 'compositeApiGroups={platform.acme.com}'
```

One Helm value renders both the `--composite-api-groups` allowlist flag and the per-group RBAC rules. Adding a group takes a values change and an upgrade, which keeps it a deliberate decision rather than something a definition can grant itself. A definition naming an unlisted group gets a terminal `GroupNotAllowed` condition and no CRD is created.

## One parameter contract

`api.parametersSchema` is the single source of the parameter contract, enforced in three places:

| Where | When | Failure surface |
| --- | --- | --- |
| Typed object admission | on `kubectl apply` of the platform kind | apiserver rejection |
| Raw `DoComposite` render | every reconcile | `Synced=False / RenderFailed` on the composite |
| Template `${params.*}` usage | when the definition is applied | `APIServed=False / InvalidSchema` naming the undeclared parameter |

There is no separate required-parameters list and no second schema to keep in sync.

## Evolve an API

Versioning follows the Crossplane model. Generated CRDs always use conversion strategy `None`, so every served version must stay round-trippable: adding an optional parameter is a version bump; a new required parameter is a new API, not a new version.

1. Bump `api.version` (e.g. `v1` → `v2`) and keep the old version served via `api.deprecatedVersions: [v1]`. Dropping a version whose objects still exist is refused with `StoredVersionInUse`.
2. Watch migration progress; the definition counts objects per version:

   ```sh
   kubectl get docd pet-identity -o jsonpath='{.status.apiVersions}'
   # [{"name":"v2","objects":3},{"deprecated":true,"name":"v1","objects":1}]
   ```

   Clients writing the deprecated version receive an apiserver deprecation warning.
3. Humans migrate manifests to the new `apiVersion`. When the old version reports zero objects, remove it from `deprecatedVersions`; doplane prunes the CRD's `status.storedVersions` bookkeeping automatically.

`api.group`, `api.kind`, and `api.plural` are immutable once served, and `spec.api` cannot be removed: a rename would strand every typed object behind the old API. A breaking change ships as a new definition; objects migrate at the leaf, using the `externalName` template field or the `crossplane.io/external-name` annotation to re-adopt existing cloud resources.

## Retire an API

Deleting a definition is blocked by a finalizer while typed objects exist (`APIServed=False / DeletionBlocked` with the object count). Delete the typed objects first; each one tears down its composite and children through the normal graph-ordered teardown. Once no objects remain, doplane deletes the generated CRD and releases the definition. There is deliberately no owner-reference cascade from definition to CRD: a cascade would turn an accidental `kubectl delete docd` into mass deletion of live infrastructure.

## Ownership and collisions

Generated CRDs carry `app.kubernetes.io/managed-by: doplane` and a `do.pulumi.com/owner` annotation naming their source definition. Ownership is checked before every apply, so two definitions can never silently fight over one kind, even across manager restarts. The loser gets a terminal `CRDConflict`; rename its kind or set `spec.api.plural`/`group`. A CRD not managed by doplane is never overwritten.

## Debugging

The machinery stays inspectable:

```sh
kubectl get petidentities payments-identity        # the product
kubectl get docomposite payments-identity          # its owned mirror
kubectl get doresources -l do.pulumi.com/composite=payments-identity
kubectl get docd pet-identity -o jsonpath='{.status.conditions}'
```

The [conditions reference](/reference/conditions) lists every `APIServed` reason and what to check first.
