---
title: Discover providers and components
description: Find resource type tokens in the Pulumi Registry, generate example manifests, and consume component packages.
---

# Discover providers and components

<div class="agent-contract">
  <p><strong>Agent goal:</strong> resolve a user's intent ("an S3 bucket", "a DigitalOcean droplet") to an exact type token and pinned package before writing a manifest. Never guess property names — read the schema or the registry docs.</p>
</div>

Everything doplane manages comes from the [Pulumi Registry](https://www.pulumi.com/registry/): 150+ providers for clouds, SaaS APIs, databases, and Kubernetes itself, plus component packages authored in regular programming languages. This page maps a registry entry to a doplane manifest.

## From registry page to type token

A `DoResource` needs two identifiers:

- `spec.type` — the resource's **type token**, shaped `<package>:<module>/<name>:<Name>`, for example `aws:s3/bucketV2:BucketV2` or `digitalocean:index/droplet:Droplet`
- `spec.package` — the provider package, pinned to a version: `aws@7.34.0`

Find both in the registry: pick a provider, open the resource under **API Docs**, and read the token from the page's import section (every resource page ends with an `Import` example that spells it out). The provider's changelog lists versions; pin one instead of tracking latest — an unpinned package can resolve differently across reconciles and emits a warning event.

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
```

## Generate a ready-to-edit example

From a repository checkout, `hack/provider-help.sh` reads the provider schema and prints required inputs, optional inputs, the output paths usable in `spec.references`, and an example manifest for one token:

```sh
./hack/provider-help.sh aws@7.34.0 aws:s3/bucketV2:BucketV2
```

It needs `pulumi` and `jq`. For the raw schema, use the Pulumi CLI directly:

```sh
pulumi package get-schema aws@7.34.0 | jq '.resources["aws:s3/bucketV2:BucketV2"]'
```

Guessing is safe, too: doplane validates `spec.properties` against the provider schema before any cloud call, so a wrong property name ends with a `ValidationFailed` condition naming the offending field — no mutation runs.

## Components

The registry also hosts **component packages**: reusable infrastructure written in TypeScript, Go, Python, C#, or Java and published with a schema. The Pulumi Cloud private registry serves your organization's own components the same way, addressed as `private/<org>/<name>`.

Consume a component like any provider resource:

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoResource
metadata:
  name: web-app
spec:
  type: web-app:index:WebAppComponent
  package: private/ediri/web-app
  properties:
    replicas: 2
```

Private registry access needs a `PULUMI_ACCESS_TOKEN` in the runner's credentials Secret; a missing or under-scoped token surfaces as `RegistryAuthMissing` or `RegistryResolveFailed`. To hide the token and the package pin from application teams, register the component on a `DoProvider` with `typedResources` — doplane generates a dedicated CRD from the component's schema ([choose an API](/guide/choose-an-api#typed-provider-resource)). Components run in an ephemeral engine inside the runner Job; [Control loop and state](/concepts/control-loop#component-resources) explains the checkpoint.

Working examples in the repository: [`examples/08-private-registry-component.yaml`](https://github.com/dirien/doplane/blob/main/examples/08-private-registry-component.yaml), [`examples/12`](https://github.com/dirien/doplane/blob/main/examples/12-typed-private-registry-component-provider.yaml) and [`13`](https://github.com/dirien/doplane/blob/main/examples/13-typed-private-registry-component.yaml).

## Curate what teams may use

Discovery is for platform engineers; application teams should meet a narrower surface. Put the package pin and credentials in a `DoProvider` profile and enforce the approved resource list with `allowedResources` — a token outside the list fails with `ResourceNotAllowed` before any provider call. [Choose an API](/guide/choose-an-api) covers the options.
