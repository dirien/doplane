---
title: Security and tenancy
description: Place credentials and secret inputs correctly and choose the runner namespace model.
---

# Security and tenancy

<div class="agent-contract">
  <p><strong>Agent goal:</strong> keep credentials out of the manager and values out of manifests. Treat status and component checkpoints as sensitive-adjacent even when logs are redacted.</p>
</div>

## Execution boundary

The manager image has no Pulumi CLI, provider plugins, or cloud credentials. Each runner Job:

- runs as non-root with a read-only root filesystem;
- drops Linux capabilities and uses `RuntimeDefault` seccomp;
- disables service-account token mounting;
- receives one typed operation document;
- mounts or injects credentials only for that operation;
- writes temporary backend and input files under an isolated writable path.

Do not move provider execution into the manager to reduce Job overhead. The proposed warm runner keeps a separate trust boundary; see `docs/RUNNER_SERVICE_DESIGN.md`.

## Runner namespace modes

| Mode | Job namespace | Credential ownership | Use when |
| --- | --- | --- | --- |
| `operator` (default) | operator namespace | one shared Secret | one platform identity serves the cluster |
| `resource` | resource namespace | tenant namespace Secret | each tenant owns an isolated cloud identity |

Configure per-resource runners with Helm:

```sh
helm upgrade --install doplane oci://ghcr.io/dirien/charts/doplane \
  --namespace doplane-system \
  --create-namespace \
  --set runner.namespaceMode=resource
```

In `resource` mode, namespace deletion can outlive the place where a delete Job would run. doplane falls back to the operator namespace for cleanup. Keep a narrowly scoped cleanup identity there if tenant resources must be deleted during namespace teardown.

## Watch scope

`watchNamespaces` limits namespaced reconciliation. The Helm chart replaces broad manager RBAC with Roles in the selected namespaces while retaining the minimum cluster access required for cluster-scoped definitions.

```sh
helm upgrade --install doplane oci://ghcr.io/dirien/charts/doplane \
  --namespace doplane-system \
  --set 'watchNamespaces={team-a,team-b}' \
  --set runner.namespaceMode=resource
```

Watch scope and credential placement solve different problems. Configure both for a multi-tenant installation.

## Secret inputs

Use `spec.valuesFrom` to inject a Secret key into one property:

```yaml
spec:
  properties:
    length: 2
  valuesFrom:
    - toPath: keepers.rotation
      secretKeyRef:
        name: pet-naming
        key: rotation
```

Only a placeholder and path-to-environment mapping travel through the object and Job spec. The kubelet injects the value into the runner, which substitutes it immediately before the provider call. Progress logs and error messages redact the value.

Never inject a secret into an identity-forming property (a name, prefix, or separator): the provider-assigned ID would embed the value, and doplane refuses to record it (`SecretInputInID`) — after the external resource may already have been created.

The Secret must exist where the runner Job executes. `valuesFrom` is unsupported for component resources because their checkpoint would persist the input.

## Outputs and connection Secrets

Provider outputs are not universally secret. `status.outputs` preserves structured provider data so selected values can round-trip into a connection Secret. A provider that echoes an input may therefore put that value in status.

Use `writeConnectionSecretToRef` and `connectionDetails` to publish selected outputs, but do not assume this removes them from status:

```yaml
spec:
  writeConnectionSecretToRef:
    name: database-connection
  connectionDetails:
    - name: endpoint
      fromFieldPath: status.outputs.endpoint
    - name: resourceID
      fromFieldPath: status.id
```

Encrypt etcd at rest, restrict access to doplane objects and Secrets, and avoid returning secret material from provider resources where possible.

## Plugin cache

The optional shared PVC stores executable provider plugins. It must never store credentials, generated programs, or backend state. Limit write access to runner pods and populate air-gapped caches from a trusted artifact source.
