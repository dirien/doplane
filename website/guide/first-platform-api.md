---
title: "Your first platform API"
description: Publish a branded, schema-validated Kubernetes API from a composite definition and consume it as an app team, using the credential-free random provider.
---

# Your first platform API

<div class="agent-contract">
  <p><strong>Agent goal:</strong> publish one typed platform API in a branded group and prove a consumer object reaches <code>Ready=True</code>. Requires a running doplane install (see Getting started). The group allowlist is a Helm value; never grant it by editing RBAC directly.</p>
</div>

This walkthrough continues from [Getting started](/guide/getting-started): doplane is installed and reconciled one `DoResource`. Now you take the platform-team seat, publish `webidentities.platform.example.com/v1` as a real Kubernetes API, and then switch to the app-team seat and consume it. The `random` provider does all the work, so there is no cloud account and nothing billable.

The full lifecycle of platform APIs (versioning, migration, retirement) is covered in [Platform APIs](/guide/platform-apis); this page is the shortest working path.

## 1. Allow the group

Platform groups are an install-time decision. One Helm value renders both the manager's allowlist flag and the RBAC for the new group:

```sh
helm upgrade doplane oci://ghcr.io/dirien/charts/doplane \
  --namespace doplane-system --reuse-values \
  --set 'compositeApiGroups={platform.example.com}'

kubectl -n doplane-system rollout status deployment/doplane-controller-manager \
  --timeout=2m
```

Skipping this step is a useful experiment: the definition below would sit at `APIServed=False / GroupNotAllowed` and no CRD would be created.

## 2. Publish the API

A provider profile pins the package once, and the definition declares the API next to its resource templates:

```sh
kubectl apply -f - <<'EOF'
apiVersion: do.pulumi.com/v1alpha1
kind: DoProvider
metadata:
  name: random
spec:
  package: random@4.21.0
---
apiVersion: do.pulumi.com/v1alpha1
kind: DoCompositeDefinition
metadata:
  name: web-identity
spec:
  api:
    group: platform.example.com
    kind: WebIdentity
    version: v1
    parametersSchema:
      type: object
      required: [team]
      properties:
        team:
          type: string
          minLength: 1
        petLength:
          type: integer
          minimum: 1
          maximum: 5
          default: 2
  resources:
    - name: pet
      type: random:index/randomPet:RandomPet
      providerRef:
        name: random
      properties:
        length: ${params.petLength}
        prefix: ${params.team}
    - name: token
      type: random:index/randomString:RandomString
      providerRef:
        name: random
      properties:
        length: 12
        special: false
        keepers:
          identity: ${resources.pet.id}
EOF

kubectl get docd web-identity
kubectl wait --for=condition=Established \
  crd/webidentities.platform.example.com --timeout=1m
```

The API is served when:

- `kubectl get docd web-identity` shows `SERVED True` with reason `Served`;
- the `webidentities.platform.example.com` CRD reports `Established`;
- `kubectl api-resources --api-group=platform.example.com` lists the kind.

The `parametersSchema` is the whole contract: it validates consumer objects at admission, and the templates' `${params.*}` references were cross-checked against it when the definition was applied. A typo like `${params.taem}` would have parked the definition at `InvalidSchema` naming the undeclared parameter.

## 3. Consume it as an app team

```sh
kubectl apply -f - <<'EOF'
apiVersion: platform.example.com/v1
kind: WebIdentity
metadata:
  name: checkout-web
spec:
  team: checkout
  petLength: 3
EOF

kubectl wait webidentity/checkout-web --for=condition=Ready --timeout=2m
kubectl get webidentities checkout-web
```

The consumer succeeded when `Ready=True` and `Synced=True`. No Pulumi token, no package version, no provider wiring appears in the object; the schema fills in `petLength: 2` when omitted and rejects bad input before anything reconciles:

```sh
kubectl apply -f - <<'EOF'
apiVersion: platform.example.com/v1
kind: WebIdentity
metadata:
  name: broken
spec:
  petLength: 9
EOF
# The WebIdentity "broken" is invalid:
# * spec.team: Required value
# * spec.petLength: Invalid value: 9: spec.petLength in body should be less than or equal to 5
```

`kubectl explain webidentities.spec --api-version=platform.example.com/v1` documents the parameters, including the reserved `doplane` block that carries doplane's lifecycle knobs (`updatePolicy`, `revisionRef`).

## 4. Look under the hood

The typed object expanded through visible machinery, one Kubernetes object per Pulumi resource:

```sh
kubectl get docomposite checkout-web          # the owned mirror, 2/2 ready
kubectl get doresources                       # checkout-web-pet, checkout-web-token
kubectl get docd web-identity -o jsonpath='{.status.apiVersions}{"\n"}'
```

The pet's external name starts with `checkout-` and has three words, both driven by the parameters. The token's `keepers.identity` referenced the pet, so the graph engine created the pet first.

## Clean up

```sh
kubectl delete webidentity checkout-web
kubectl delete docd web-identity
kubectl delete doprovider random
```

Order matters only in one direction: deleting the definition while `WebIdentity` objects exist is blocked by a finalizer (`APIServed=False / DeletionBlocked`) until the objects are gone; then doplane removes the generated CRD itself.

## Next task

Evolve the API you just published: version bumps, deprecation, per-version object counts, and renames are in [Platform APIs](/guide/platform-apis).
