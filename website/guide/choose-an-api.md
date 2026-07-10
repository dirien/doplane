---
title: Choose an API
description: Select DoResource, provider profiles, typed resources, or composites for a task.
---

# Choose an API

<div class="agent-contract">
  <p><strong>Agent goal:</strong> select the smallest doplane API that preserves platform ownership. Prefer a curated API when application teams should not control provider tokens, versions, or credentials.</p>
</div>

## Decision table

| Task | API | Owner | What the consumer sees |
| --- | --- | --- | --- |
| Explore one provider resource | `DoResource` with `spec.package` | platform engineer | Pulumi token and raw properties |
| Pin a provider and restrict resource types | `DoProvider` + `providerRef` | platform team | provider profile name |
| Let a tenant own its provider pin and Secret | `DoProviderConfig` + `providerRef.kind` | tenant team | namespaced provider profile |
| Expose one provider type as Kubernetes-native | `DoProvider.spec.typedResources` | platform team | generated kind and `spec.forProvider` |
| Bundle a dependency graph | `DoCompositeDefinition` + `DoComposite` | platform team | definition name and parameters |
| Expose a graph as a typed platform API | `DoCompositeDefinition.spec.api` | platform team | generated kind with a validated spec |

## Raw resource

Use a raw resource for exploration or low-level platform work:

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoResource
metadata:
  name: pet
spec:
  type: random:index/randomPet:RandomPet
  package: random@4.21.0
  properties:
    length: 2
```

Pin `spec.package`. An unpinned package can resolve differently across reconciles and emits a warning event.

## Provider-backed resource

Declare the package and allow-list once:

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoProvider
metadata:
  name: random
spec:
  package: random@4.21.0
  allowedResources:
    - index/randomPet
---
apiVersion: do.pulumi.com/v1alpha1
kind: DoResource
metadata:
  name: pet
spec:
  type: random:index/randomPet:RandomPet
  providerRef:
    name: random
  properties:
    length: 2
```

Wait for the provider profile before creating consumers:

```sh
kubectl wait doprovider/random --for=condition=Ready --timeout=2m
```

`DoProvider` is cluster-scoped. Use `kind: DoProviderConfig` in `providerRef` for a same-namespace, tenant-owned profile.

## Typed provider resource

Add a full token to `DoProvider.spec.typedResources`. doplane fetches the provider schema and creates a CRD in `typed.do.pulumi.com`. The generated object exposes `spec.forProvider`, `kubectl explain`, printer columns, and per-kind RBAC. Its controller creates an owned `DoResource` mirror.

Use this API when consumers need one provider resource but should not type a Pulumi token. Check [`examples/12-typed-private-registry-component-provider.yaml`](https://github.com/dirien/doplane/blob/main/examples/12-typed-private-registry-component-provider.yaml) for the complete registration flow.

## Composite

A `DoCompositeDefinition` renders a graph of ordinary `DoResource` children. `${resources.*}` expressions compile into references, so ordering and propagation remain visible to the graph engine.

The typed platform kind is the intended product: add `spec.api` and doplane serves the definition as a real, versioned Kubernetes API (for example `websites.platform.acme.com/v1`). Publishing, group allowlisting, the parameter contract, version bumps, and retirement have their own chapter: [Platform APIs](/guide/platform-apis). Use a generic `DoComposite` directly for debugging or while the API is still evolving.

## Constraints to preserve

- Keep raw provider properties out of application-facing composites.
- Put package versions and credential references in provider profiles.
- Use `allowedResources` to enforce the approved surface, not as documentation only.
- Treat generated typed CRDs as controller-owned. Change the source profile or definition instead of patching generated CRDs.
- Test a composite definition with a credential-free provider before introducing billable resources.
