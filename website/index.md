---
layout: home

hero:
  name: "Cloud resources."
  text: "Kubernetes-native."
  tagline: Manage provider resources through Kubernetes objects. No Pulumi programs, stacks, or state files.
  image:
    src: /logo.svg
    alt: doplane
  actions:
    - theme: brand
      text: Run it on kind
      link: /guide/getting-started
    - theme: alt
      text: View on GitHub
      link: https://github.com/dirien/doplane

features:
  - icon: "◈"
    title: One declarative control loop
    details: Apply a DoResource. doplane validates it, calls the provider, and records observed state in the object's status.
  - icon: "⌁"
    title: Any Pulumi provider
    details: Pin provider packages once, restrict allowed resource types, and give teams Kubernetes-native APIs.
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

doplane reconciles the object with `pulumi do --stateless`, then writes the provider ID, outputs, conditions, and applied generation to `status`. Kubernetes remains the control plane and the state store.

[Start with a credential-free resource →](/guide/getting-started)
