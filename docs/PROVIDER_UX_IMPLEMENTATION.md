# Provider UX Implementation Guide

This guide describes the provider onboarding UX for DevOps and platform
engineering teams. The goal is simple: platform engineers add a provider once;
application teams consume curated composites without learning Pulumi provider
tokens, plugin versions or credential names.

## Target UX

Platform teams should be able to declare provider support with one object:

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoProvider
metadata:
  name: digitalocean
spec:
  package: digitalocean@4.73.0
  credentialsSecretRef:
    name: provider-credentials
  credentialKeys:
    - DIGITALOCEAN_TOKEN
  pluginCache:
    mode: Writable
```

Then raw resources can reference the provider:

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoResource
metadata:
  name: web-node
spec:
  providerRef:
    name: digitalocean
  type: digitalocean:index/droplet:Droplet
  properties:
    name: web-node
    region: nyc3
    image: ubuntu-24-04-x64
    size: s-1vcpu-1gb
```

App teams should usually consume a `DoComposite` instead of raw
`DoResource`s. Composites hide provider tokens, package versions, references
and provider-specific defaults.

## What Teams Need To Know

Platform engineers own:

- Provider package and version, for example `digitalocean@4.73.0`.
- Credential key names and Secret location.
- Which resources are approved for use.
- Which examples create billable resources.
- Which outputs are sensitive-adjacent and should not stay in status long term.
- Upgrade rules for provider versions and composite definitions.

Application teams own:

- Composite parameters such as region, environment, size class and retention.
- Deletion intent: delete the cloud resource or orphan it.
- Cost and ownership labels required by the platform.

Application teams should not need to know:

- Pulumi plugin installation details.
- Raw provider tokens such as `aws:s3/bucketV2:BucketV2`.
- Provider-specific dependency wiring.
- Credential environment variable names.

## Implementation Phases

### Phase 1: Writable Plugin Cache

Implement the cache before adding `DoProvider`. This removes the current need
to rebuild the runner image whenever a new provider or plugin version appears.

Keep the runner root filesystem read-only. Mount only these writable paths:

- `/tmp` as `emptyDir` for input files and the file backend.
- `/var/lib/doplane/pulumi-home` as the shared plugin cache.

Set:

```sh
PULUMI_HOME=/var/lib/doplane/pulumi-home
PULUMI_BACKEND_URL=file:///tmp
HOME=/tmp
PULUMI_SKIP_UPDATE_CHECK=true
```

The cache stores provider plugins under `$PULUMI_HOME/plugins`. It must not
store cloud credentials. Keep credentials in environment variables from the
`provider-credentials` Secret.

Any Terraform Provider support is intentionally postponed. The investigation
is parked in `docs/ANY_TERRAFORM_PROVIDER_NOT_IMPLEMENTING.md`.

Recommended Helm values:

```yaml
pluginCache:
  enabled: true
  create: true
  existingClaim: ""
  storageClassName: ""
  accessModes:
    - ReadWriteMany
  size: 5Gi
  mountPath: /var/lib/doplane/pulumi-home
```

Use `ReadWriteMany` for the first implementation. It lets concurrent runner
Jobs on different nodes share the same cache. A later optimization can support
`ReadWriteOnce` with node affinity or a persistent runner service.

Runner Job changes:

1. Add `PluginCachePVC` and `PluginCacheMountPath` fields to `JobRunner`.
2. Mount the PVC at `PluginCacheMountPath`.
3. Set `PULUMI_HOME` to that mount path.
4. Keep `ReadOnlyRootFilesystem: true`.
5. Set pod `fsGroup: 65532` so the non-root runner can write the PVC.
6. Add resource requests for plugin download spikes; provider archives can be
   large.

Before every schema fetch or `pulumi do` call, ensure the required plugin is
available:

```sh
pulumi plugin install resource <name> <version>
```

Derive `<name>` and `<version>` from `spec.package`. If the package omits a
version, keep the current registry behavior but emit a warning condition that
the provider is not pinned.

Add a per-plugin filesystem lock to avoid concurrent downloads corrupting the
cache or wasting bandwidth. Use an atomic directory lock on the PVC:

```sh
lock="$PULUMI_HOME/.locks/<name>@<version>.lock"
until mkdir "$lock" 2>/dev/null; do sleep 2; done
trap 'rmdir "$lock"' EXIT
pulumi plugin install resource <name> <version>
```

This avoids requiring runner pods to talk to the Kubernetes API. If a pod dies
while holding a lock, the next implementation should treat old lock
directories as stale after the Job timeout.

Failure UX:

- Missing network access: `Synced=False / PluginInstallFailed`.
- Missing writable cache permissions: `Synced=False / PluginCacheNotWritable`.
- Unpinned package: `Synced=True` with a Warning event, or reject it later by
  policy.
- Unknown package: `Synced=False / SchemaFetchFailed`.

Security notes:

- Do not mount the PVC into the manager.
- Do not put credentials or generated PCL input files in the cache.
- Keep the runner service account token disabled.
- Treat the cache as executable content. Restrict write access to runner pods.
- For air-gapped clusters, populate the PVC from a trusted image or artifact
  mirror before allowing reconciles.

### Phase 2: Provider Profile

Add a cluster-scoped `DoProvider` CRD:

```go
type DoProviderSpec struct {
    Package string `json:"package"`
    CredentialsSecretRef LocalSecretReference `json:"credentialsSecretRef,omitempty"`
    CredentialKeys []string `json:"credentialKeys,omitempty"`
    AllowedResources []string `json:"allowedResources,omitempty"`
    PluginCache PluginCachePolicy `json:"pluginCache,omitempty"`
}
```

Controller behavior:

- Fetch and validate the package schema.
- Install the plugin into the writable cache.
- Check that the configured Secret exists.
- Check that required credential keys exist.
- Record status conditions: `Ready`, `SchemaFetched`, `PluginReady`,
  `CredentialsReady`.

Expose status:

```yaml
status:
  package:
    name: digitalocean
    version: 4.73.0
  plugin:
    ready: true
    cachePath: /var/lib/doplane/pulumi-home
  conditions:
    - type: Ready
      status: "True"
```

### Phase 3: `providerRef` On `DoResource`

Add `spec.providerRef` to `DoResource`. Keep `spec.package` for backward
compatibility.

Resolution rules:

1. If `providerRef` is set, resolve package and credentials from the
   referenced `DoProvider`.
2. If both `providerRef` and `package` are set, require them to match or reject
   the resource.
3. If neither is set, keep existing package inference.

This lets raw resources become shorter and gives platform teams one place to
enforce provider versions.

### Phase 4: Cataloged Composites

Create platform-owned `DoCompositeDefinition`s for approved workflows:

- `secure-s3-bucket`
- `digitalocean-web-node`
- `static-site`
- `postgres-database`
- `kubernetes-workload-identity`

Each composite should:

- Pin provider versions through `providerRef`.
- Hide resource tokens.
- Set cost and ownership tags.
- Set conservative defaults.
- Mark billable resources clearly in comments and docs.
- Expose only safe parameters.

### Phase 5: Generated Help

Add a command or controller status helper that reads provider schemas and
prints:

- Required inputs for a resource token.
- Optional inputs.
- Output paths usable in `spec.references`.
- Example `DoResource` YAML.
- Known billable resource warnings from a curated table.

This can start as a developer script and later become a CLI.

## Code Touch Points

- `api/v1alpha1/`: add `DoProvider`, `providerRef`, status types and markers.
- `internal/pulumido/job.go`: mount plugin cache PVC, set `PULUMI_HOME`, run
  plugin install with locking.
- `internal/pulumido/runner.go`: add typed errors for plugin install failures.
- `internal/pulumido/schema.go`: keep schema cache keyed by full package
  version.
- `internal/controller/`: add `DoProvider` reconciler and provider resolution.
- `deploy/kustomize/`: add PVC manifests and manager args/env for cache
  settings.
- `deploy/doplane/`: mirror the PVC settings in Helm values and
  templates.
- `Dockerfile.runner`: keep Pulumi CLI and `jq`; preinstalled plugins become
  optional bootstrap optimizations.
- `examples/`: add provider-profile examples and composites per provider.

## Tests

Unit tests:

- Package parsing: `aws@7.34.0`, `digitalocean@4.73.0`, unpinned package.
- Plugin install script generation and lock path escaping.
- Provider resolution from `providerRef`.
- Error classification for plugin install failures.

Envtest:

- `DoProvider` becomes Ready when schema, plugin and credentials are ready.
- `DoResource` using `providerRef` gets the resolved package.
- Missing Secret key produces `CredentialsReady=False`.
- Plugin install failure blocks cloud mutation.

Kubernetes integration:

- Two resources with the same new provider start concurrently and share one
  cached plugin.
- A second reconcile reuses the cached plugin without downloading.
- Runner pods keep `readOnlyRootFilesystem: true`.
- PVC permissions work for UID/GID 65532.

## Migration Plan

1. Add writable plugin cache support behind `pluginCache.enabled=false`.
2. Enable it in kind and CI smoke tests.
3. Stop requiring every sample provider to be baked into `Dockerfile.runner`.
4. Add `DoProvider`.
5. Add `providerRef`.
6. Move examples and composites to `providerRef`.
7. Keep baked plugins for common providers as a cold-start optimization only.

## Definition Of Done

- A new provider can be used by changing YAML, not rebuilding the runner image.
- First use downloads the pinned plugin into the shared cache.
- Later uses reuse the cached plugin.
- Runner pods still have a read-only root filesystem.
- Provider credentials remain Secret-backed env vars.
- Users get clear conditions when plugin install, schema fetch or credential
  checks fail.
