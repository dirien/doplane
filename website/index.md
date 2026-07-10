---
layout: home

hero:
  name: "Cloud resources."
  text: "Kubernetes-native."
  tagline: Pulumi under the hood, Kubernetes objects on the surface. The whole Pulumi ecosystem as declarative APIs on any cluster — no separate state service to operate.
  image:
    src: /logo.svg
    alt: doplane
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: View on GitHub
      link: https://github.com/dirien/doplane

features:
  - icon: "◈"
    title: One declarative control loop
    details: Apply a DoResource. doplane validates it, calls the provider, and records observed state in the object's status.
  - icon: "⌁"
    title: The whole Pulumi ecosystem
    details: Use any Pulumi provider, and component resources authored in TypeScript, Go, Python, C# or Java — pinned, allow-listed, and served as Kubernetes-native APIs.
  - icon: "◇"
    title: Composable platform APIs
    details: Turn dependency graphs into reusable composites or generated typed CRDs for application teams.
  - icon: "⊘"
    title: No separate state service
    details: Resource IDs, outputs, and component checkpoints live with the Kubernetes object in etcd.
  - icon: "⎈"
    title: Isolated execution
    details: Each provider operation runs in a hardened, short-lived Kubernetes Job. The manager holds no cloud credentials.
  - icon: "M↓"
    title: Documentation built for agents
    details: Copy or download any page as Markdown, or load llms.txt for a repository-wide task map.
---

<div class="agent-contract">
  <p><strong>Built for two readers:</strong> this page gives people the product shape. The guides give coding agents exact commands, invariants, source paths, and success conditions.</p>
</div>

## The object is the infrastructure record

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoResource
metadata:
  name: assets
spec:
  type: aws:s3/bucketV2:BucketV2
  package: aws@7.34.0
  properties:
    bucket: product-assets
    tags:
      managed-by: doplane
```

doplane runs Pulumi under the hood: it reconciles the object with `pulumi do --stateless`, then writes the provider ID, outputs, conditions, and applied generation to `status`. Kubernetes remains the control plane and the state store.

## Bring your own components

Author a component resource in any Pulumi language, publish it to a registry — including the Pulumi Cloud private registry — and doplane serves it as a typed, Kubernetes-native API:

```yaml
apiVersion: typed.do.pulumi.com/v1alpha1
kind: WebAppComponent
metadata:
  name: storefront
spec:
  forProvider:
    replicas: 2
```

One `DoProvider` with `typedResources` turns the component's schema into a generated CRD with `kubectl explain`, printer columns, and per-kind RBAC. The component code stays code; application teams get a Kubernetes object.

[Start with a credential-free resource →](/guide/getting-started)
