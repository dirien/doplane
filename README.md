# pulumi-do-operator

A Kubernetes operator that manages individual cloud resources through the new
[`pulumi do`](https://www.pulumi.com/docs/) CLI — Crossplane-style, but with
zero Pulumi programs, stacks, or state files. The desired state lives in a
`DoResource` custom resource, the observed cloud state is written back into
its `status` and therefore persisted in etcd.

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
      managed-by: pulumi-do-operator
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
subresource — etcd is the state store, exactly like Crossplane's managed
resources.

### Isolated runner Jobs

The manager never executes provider plugins itself. Every `pulumi do`
operation (and every registry schema fetch) is spawned as a dedicated
**Kubernetes Job** using a separate runner image:

- manager image: distroless, no Pulumi CLI, no cloud credentials;
- runner image (`Dockerfile.runner`): `pulumi` CLI + pre-installed provider
  plugins + `jq`, hardened pod (non-root, read-only root filesystem, no
  capabilities, seccomp `RuntimeDefault`), own resource limits,
  `activeDeadlineSeconds` and TTL cleanup;
- cloud credentials come from the optional `provider-credentials` Secret and
  are mounted **only into runner pods**;
- schema fetches pipe `pulumi package get-schema` through `jq` so only the
  requested resource's schema lands in the pod log (the full AWS schema is
  ~56 MB — beyond kubelet log rotation limits);
- runner pods run with `PULUMI_BACKEND_URL=file:///tmp`, keeping the CLI
  fully offline with respect to Pulumi Cloud.

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

## Getting started (kind)

```sh
kind create cluster --name pdo

make docker-build docker-build-runner IMG=pulumi-do-operator:dev RUNNER_IMG=pulumi-do-runner:dev
kind load docker-image pulumi-do-operator:dev pulumi-do-runner:dev --name pdo

make install deploy IMG=pulumi-do-operator:dev

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
  Crossplane solves this with pre-create external-name bookkeeping — a good
  future improvement.
- `spec.properties` are treated as non-secret; support for resolving values
  from Secrets would be the next step toward production use.
- `status.outputs` stores what `pulumi do` prints. Secret-flagged outputs
  are masked by the CLI (the operator never passes `--show-secrets`), but
  treat status as sensitive-adjacent: anything a provider returns unflagged
  lands in etcd verbatim. Connection-secret support
  (`writeConnectionSecretToRef`-style) is future work.

## License

Apache-2.0
