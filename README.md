# doplane

A Kubernetes operator that manages individual cloud resources through the new
[`pulumi do`](https://www.pulumi.com/docs/) CLI. Pulumi runs under the hood,
so the whole Pulumi ecosystem — every provider, plus component resources
authored in any Pulumi language — is available as Kubernetes-native APIs,
with no separate state backend to operate. The desired state lives in a
`DoResource` custom resource, the observed cloud state is written back into
its `status` and therefore persisted in etcd.

**[Read the documentation](https://dirien.github.io/doplane/)** — install it
on any Kubernetes cluster (the quickstart walks through a local kind
cluster), choose an API, or load the agent-oriented Markdown guides through
[`llms.txt`](https://dirien.github.io/doplane/llms.txt).

```yaml
apiVersion: do.pulumi.com/v1alpha1
kind: DoResource
metadata:
  name: my-bucket
spec:
  type: aws:s3/bucketV2:BucketV2   # any Pulumi type token
  package: aws@7.34.0              # pin the provider (optional)
  deletionPolicy: Delete           # or Orphan
  properties:                      # validated against the registry schema
    bucket: my-bucket-name
    tags:
      managed-by: doplane
```

After reconciliation:

```yaml
status:
  id: my-bucket-name
  outputs:                         # full provider state, stored in etcd
    arn: arn:aws:s3:::my-bucket-name
    region: us-west-2
    ...
  observedGeneration: 1
  conditions:
  - type: Synced                   # last provider operation succeeded
    status: "True"
  - type: Ready                    # external resource exists
    status: "True"
```

## How it works

`pulumi do --stateless` gives direct CRUD access (`create`, `read`, `patch`,
`delete`) to any resource of any Pulumi provider, without a Pulumi program or
state file. This operator turns that into a declarative control loop:

| CR event | Operator action |
|---|---|
| created | validate `spec.properties` against the provider's JSON schema from the Pulumi registry, then `pulumi do <type> create` |
| spec changed | `pulumi do <type> patch <id>` |
| periodic resync (10m) | `pulumi do <type> read <id>` — refreshed into `status.outputs`; a vanished resource is recreated |
| deleted | finalizer runs `pulumi do <type> delete <id>` (unless `deletionPolicy: Orphan`) |

The resulting resource state (id + all outputs) is stored on the CR's status
subresource — etcd is the state store.

### Isolated runner Jobs

The manager never executes provider plugins itself. Every `pulumi do`
operation (and every registry schema fetch) is spawned as a dedicated
**Kubernetes Job** using a separate runner image:

- manager image: distroless, no Pulumi CLI, no cloud credentials;
- runner image (`Dockerfile.runner`): the **`doplane-runner`** binary + `pulumi`
  CLI + language toolchains + pre-installed provider plugins, hardened pod
  (non-root, read-only root filesystem, no capabilities, no service-account
  token, seccomp `RuntimeDefault`), own resource limits,
  `activeDeadlineSeconds` and TTL cleanup;
- each Job runs exactly one typed `doplane-runner` invocation: the operation
  arrives as one JSON document (`DOPLANE_OP`), the outcome returns as one JSON
  envelope with a machine-readable failure code — no shell scripts, no log
  scraping;
- Jobs get deterministic names derived from the owning object and operation,
  so interrupted operations are **adopted** on retry instead of re-run;
- cloud credentials come from the optional `provider-credentials` Secret and
  are mounted **only into runner pods**;
- schema fetches are trimmed to the single requested resource before they
  travel through pod logs (the full AWS schema is ~56 MB — beyond kubelet
  log rotation limits); private-registry schemas come straight from the
  registry API without compiling anything;
- operations use an isolated per-op `file://` backend, keeping engine state
  out of Pulumi Cloud.

For local development (`make run`), the controller falls back to executing
the local `pulumi` binary (`--runner-mode=exec`).

### Schema validation from the Pulumi registry

Before touching the cloud, `spec.properties` is validated against the
provider's JSON schema (`resources[<token>].inputProperties` /
`requiredInputs`), fetched from the Pulumi registry via
`pulumi package get-schema <package>` and cached per package. Unknown
properties, missing required inputs and primitive type mismatches surface as
a terminal `Synced=False / ValidationFailed` condition plus a Warning event —
no runner Job is spawned for invalid specs.

## Components from the Pulumi Cloud private registry

Component resources (marked `isComponent` in their schema) cannot be driven
by stateless `pulumi do` — the operator orchestrates them through an
**ephemeral engine** inside the runner Job: a generated one-resource program,
`pulumi up` against a throwaway `file://` backend, and the exported
checkpoint persisted in `status.engineState` (etcd stays the state store);
updates and deletes re-import that checkpoint. `spec.package` accepts
private-registry references, resolved through the registry API:

```yaml
spec:
  type: web-app:index:WebAppComponent
  package: private/ediri/web-app   # or org/name@version, or a git source
  properties:
    replicas: 2
```

Requires `PULUMI_ACCESS_TOKEN` in the `provider-credentials` Secret (and
`KUBECONFIG_CONTENT` for components that target Kubernetes) — see
`hack/sync-creds.sh`. Every operation runs as one typed `doplane-runner`
invocation in the Job; failures surface as machine-readable condition
reasons (`RegistryAuthMissing`, `RegistryResolveFailed`, `EngineFailed`, …).

## Dependencies between resources

`spec.references` wires values from other DoResources into a resource's
properties — with readiness gating, automatic re-patch when upstream values
change, reverse-order blocking teardown and cycle detection:

```yaml
spec:
  type: aws:s3/bucketPolicy:BucketPolicy
  references:
    - toPath: bucket
      from: {name: demo-bucket, fieldPath: status.outputs.bucket}
    - toPath: policy
      from: {name: demo-bucket, fieldPath: status.outputs.arn}
      template: '{"Statement":[{"Resource":["${value}","${value}/*"], ...}]}'
```

## Composites

Platform teams define a graph once (`DoCompositeDefinition`, cluster-scoped);
app teams instantiate it with one object (`DoComposite`). Every rendered
child is a normal DoResource you can `kubectl get` individually; sibling
expressions compile into references so the graph engine handles ordering.
See `examples/` for a walkthrough from a single resource to a
cross-provider composite DAG.

## Provider onboarding

Platform teams declare a provider once with a cluster-scoped `DoProvider`;
app teams reference it instead of memorizing plugin versions and credential
names (see `docs/PROVIDER_UX_IMPLEMENTATION.md` for the full design):

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
  allowedResources:
    - index/droplet
    - index/vpc
```

The controller validates the profile (schema fetch, plugin availability,
Secret + keys) into `Ready`/`SchemaFetched`/`PluginReady`/`CredentialsReady`
conditions. A `DoResource` (or composite child) then uses
`spec.providerRef: {name: digitalocean}` — package and credentials resolve
from the profile, a conflicting `spec.package` is rejected
(`ProviderPackageMismatch`), and tokens outside `allowedResources` fail with
`ResourceNotAllowed`.

**Tenant-owned profiles**: the namespaced `DoProviderConfig` is the twin of
the cluster-scoped `DoProvider` — same spec, owned by the team. Resources
select it with `providerRef: {name: x, kind: DoProviderConfig}` (resolved
in the resource's namespace). Its credentials Secret is validated in the
namespace runner Jobs actually load it from: the config's own namespace
with `runner.namespaceMode: resource` (full tenant ownership — teams pin
versions and rotate credentials without platform involvement), the
operator's runner namespace in the default `operator` mode.

### Writable plugin cache

With `pluginCache.enabled=true` (Helm) or the
`deploy/kustomize/plugin-cache` overlay, runner Jobs mount a shared PVC:
pinned plugins install there once — under a per-plugin lock, at most one
download cluster-wide — and every later operation reuses them. New providers
become a YAML change instead of a runner image rebuild; baked plugins remain
a cold-start optimization. Unpinned packages (no `@version`) keep on-demand
resolution and surface a `ProviderNotPinned` warning event.

Generated help for any resource type — required/optional inputs, output
paths for `spec.references`, example YAML:

```sh
./hack/provider-help.sh digitalocean@4.73.0 digitalocean:index/droplet:Droplet
```

### Typed APIs (no Pulumi tokens for app teams)

`DoProvider.spec.typedResources` turns listed tokens into generated,
schema-validated CRDs in the `typed.do.pulumi.com` group — app teams apply
`kind: BucketV2` with `spec.forProvider`, get `kubectl explain`, printer
columns and per-kind RBAC; each typed object is translated into an owned
DoResource. Component resources work the same way: if the Pulumi package
schema marks the token as `isComponent`, the generated kind is still backed
by a DoResource, but the resource controller uses the component engine path
and stores the checkpoint on the mirror's `status.engineState`.

Likewise, a `DoCompositeDefinition` with `spec.api` serves a
platform API (`kind: StaticSite`) whose spec is the composite's
parameters, optionally validated by `spec.api.parametersSchema`.

## Day-2 operations

- **Lifecycle annotations** (Crossplane-compatible):
  `crossplane.io/external-name` adopts an existing external resource and
  is persisted right after every create (closing the create/status crash
  window); `crossplane.io/paused: "true"` suspends all cloud calls;
  `crossplane.io/poll-interval: 1m` overrides the 10m drift read;
  bumping `crossplane.io/reconcile-requested-at` wakes a settled resource.
- **Deletion protection**: a `DoUsage` object blocks teardown of a
  DoResource in use outside the reference graph ("database is used by the
  payments namespace") until the usage is deleted.
- **Replacement safety**: updates that hit an immutable input stop with
  `ReplacementRequired` until approved
  (`do.pulumi.com/approve-replacement: "<generation>"`) or the resource is
  explicitly `spec.protect: false`; approved swaps run
  create-before-delete where identity allows, delete-before-create
  otherwise.
- **Composite revisions**: every definition edit snapshots an immutable
  `DoCompositeDefinitionRevision`; composites follow
  `spec.updatePolicy: Automatic|Manual` or pin `spec.revisionRef`, so
  fleet-wide template edits never roll out silently.
- **Warm runner path**: designed (not yet built) for high fan-out updates
  — see `docs/RUNNER_SERVICE_DESIGN.md`.

## Secrets in and out

**Secret inputs** (`spec.valuesFrom`) inject Secret values into properties
without the value ever being stored anywhere visible: only a placeholder
and a path→env-var mapping travel through the controller, the object and
the Job spec; the kubelet injects the value into the runner pod (from the
Secret in the Job's namespace), the runner substitutes it just before the
provider call, and the leak-prone channels — streamed logs and error
messages — are redacted. Structured `status.outputs`/state are left intact so
a provider that echoes an input round-trips the real value into a connection
Secret rather than `"(redacted)"`; treat status as sensitive-adjacent (see
below). Rotating the Secret re-patches the resource (its resourceVersion is
folded into both the applied hash and the runner Job name, so a rotation
cannot adopt a stale-value completed Job). Two hard limits:
component resources are rejected (their engine checkpoint would persist
the value), and identity-forming properties (names, prefixes) must not
come from `valuesFrom` — a provider-assigned id embedding the value cannot
be redacted, so the operation stops terminally with `SecretInputInID`.

**Connection secrets** (`spec.writeConnectionSecretToRef` +
`spec.connectionDetails`) publish selected outputs and static values into a
same-namespace Secret owned by the resource (garbage-collected with it):

```yaml
spec:
  writeConnectionSecretToRef:
    name: db-conn
  connectionDetails:
    - name: endpoint
      fromFieldPath: status.outputs.endpoint
    - name: username
      value: app
  valuesFrom:
    - toPath: password
      secretKeyRef:
        name: db-auth
        key: password
```

## Multitenancy

Two independent knobs pick the tenancy shape; both default to the simple
cluster-wide setup:

- **Watch scope** — `--watch-namespaces` (env `WATCH_NAMESPACES`,
  Helm `watchNamespaces`): empty reconciles the whole cluster; a
  comma-separated list scopes the operator to those namespaces. In the
  scoped shape the Helm chart replaces the manager ClusterRole with a Role
  per watched namespace (plus a minimal ClusterRole for the cluster-scoped
  `DoCompositeDefinition`).
- **Runner namespace mode** — `--runner-namespace-mode` (env
  `RUNNER_NAMESPACE_MODE`, Helm `runner.namespaceMode`):
  - `operator` (default): every runner Job executes in the operator's
    namespace using its shared `provider-credentials` Secret — one set of
    cloud credentials for the whole cluster.
  - `resource`: each runner Job executes in the owning DoResource's
    namespace and picks up the `provider-credentials` Secret **of that
    namespace** — per-tenant cloud credentials, with the namespace as the
    isolation boundary. Namespaces without the Secret still work for
    credential-free providers (the Secret reference is optional).

    When a tenant **namespace is deleted**, Kubernetes stops accepting new
    Jobs there while DoResource finalizers still have external resources to
    tear down. Delete operations then fall back to the operator's namespace
    and its `provider-credentials` Secret, so namespace deletion cannot
    wedge on a terminating namespace. In `resource` mode, keep credentials
    in the operator namespace that are able to delete tenant resources
    (they are used only for this cleanup path).

References (`spec.references`) always resolve within the resource's own
namespace, so tenants cannot read each other's outputs. Registry schemas
are cached per package across all tenants — schema metadata (not
credentials) is shared; actual registry/cloud operations always run with
the Job's own credentials.

```sh
# cluster-wide operator, per-namespace tenant credentials
helm install doplane deploy/doplane -n doplane-system \
  --set runner.namespaceMode=resource

# operator scoped to two team namespaces
helm install doplane deploy/doplane -n doplane-system \
  --set 'watchNamespaces={team-a,team-b}' \
  --set runner.namespaceMode=resource
```

## Getting started (kind)

```sh
kind create cluster --name doplane

make docker-build docker-build-runner IMG=doplane:dev RUNNER_IMG=doplane-runner:dev
kind load docker-image doplane:dev doplane-runner:dev --name doplane

make install deploy IMG=doplane:dev

# AWS credentials for runner pods, from the Pulumi ESC environment
# pulumi-idp/auth (only AWS_* keys are copied). Re-run when the short-lived
# credentials expire; running pods pick the new Secret up on their next Job.
./hack/sync-creds.sh

kubectl apply -f deploy/kustomize/samples/do_v1alpha1_doresource.yaml      # random pet, no cloud creds needed
kubectl apply -f deploy/kustomize/samples/do_v1alpha1_doresource_s3.yaml   # AWS S3 bucket
kubectl get doresources -w
```

## Development

```sh
make test     # unit + envtest suite
make lint     # golangci-lint, strict config, zero tolerance
make run      # run the manager locally in exec mode (uses your pulumi login/env)
```

## Design notes & limitations

- **pulumi >= 3.250 required** in the runner image: `pulumi do` CRUD needs
  `--stateless` there (the engine-driven stateful mode is not implemented
  yet). Stateless is precisely what this operator wants — status is the
  state.
- The reconciler reads the primary object through the **live API reader**
  (not the informer cache) and persists status with conflict retries:
  Job-backed reconciles run for tens of seconds, and acting on a stale
  object would double-create cloud resources.
- Providers without read/import support (e.g. `random`) cannot be drift-
  checked or patched; updates surface as `UpdateNotSupported` and reads are
  skipped gracefully.
- There is a small crash window between a successful create and the status
  write; for resources with client-chosen names the retry fails loudly
  (AlreadyExists), for server-named resources it can leak one resource.
  Pre-create external-name bookkeeping would close this — a good future
  improvement.
- Secret property values belong in `spec.valuesFrom`, not `spec.properties`
  (which is stored verbatim in etcd and echoed by the CLI).
- `status.outputs` stores what `pulumi do` prints. Secret-flagged outputs
  are masked by the CLI (the operator never passes `--show-secrets`), and
  `valuesFrom` values are redacted from logs and error messages — but NOT
  from `status.outputs`/state (so connection secrets round-trip real values).
  Treat status as sensitive-adjacent: a `valuesFrom` value a provider echoes
  into its outputs, and anything else a provider returns unflagged, lands in
  etcd verbatim. Use `writeConnectionSecretToRef` to publish selected outputs
  into a Secret instead of reading them from status.

## License

Apache-2.0
