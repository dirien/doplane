# Examples — easy to complicated

Prerequisites: operator deployed (see top-level README). Levels 3-5 need
AWS credentials synced: `./hack/sync-creds.sh`. Level 7 needs
`DIGITALOCEAN_TOKEN` in the `provider-credentials` Secret. Level 8 needs
the registry token and a kubeconfig for the runner:
`INCLUDE_PULUMI_TOKEN=1 INCLUDE_KIND_KUBECONFIG=1 ./hack/sync-creds.sh`.

| Level | File | Shows |
|---|---|---|
| 1 | `01-simple-random-pet.yaml` | one resource, create → status.outputs in etcd |
| 2 | `02-referenced-pets.yaml` | `spec.references`: readiness gating, value templating, blocked teardown — no cloud account needed |
| 3 | `03-bucket-with-policy.yaml` | real AWS dependency: policy gets the bucket ARN spliced into its document |
| 4 | `04-composite-definition.yaml` | platform-owned `DoCompositeDefinition`: 4-resource, 2-provider DAG |
| 5 | `05-composite-instance.yaml` | app-team `DoComposite`: 1 object in, 4 `DoResource` children out — each visible via kubectl |
| 6 | `06-reference-fan-in.yaml` | no-cloud fan-in graph: object paths, array paths and downstream propagation |
| 7 | `07-digitalocean-web-node.yaml` | DigitalOcean composite: VPC, tag, Droplet, firewall, project assignment |
| 8 | `08-private-registry-component.yaml` | COMPONENT from the Pulumi Cloud private registry (`private/ediri/web-app`), orchestrated by an ephemeral engine; checkpoint persisted in `status.engineState` |
| 9 | `09-provider-profile.yaml` | `DoProvider` profile: platform-pinned package, allow-list enforcement, `providerRef` on raw resources — no cloud account needed |
| 10 | `10-cataloged-composite.yaml` | the full provider UX: profile + cataloged composite served as its own typed platform API (`kind: PetIdentity`, schema-validated at admission) — no cloud account needed |
| 11 | `11-secrets-in-and-out.yaml` | `valuesFrom` secret inputs (value kept out of manifests, events and logs; never in identity-forming properties) + `writeConnectionSecretToRef` connection secret — no cloud account needed |
| 12 | `12-typed-private-registry-component-provider.yaml` + `13-typed-private-registry-component.yaml` | component package schema exposed as a generated typed CRD; typed object drives the component engine |

Generated help for any resource type (required/optional inputs, reference
paths, example YAML):

```sh
./hack/provider-help.sh random@4.21.0 random:index/randomPet:RandomPet
```

## Walkthrough

```sh
kubectl apply -f examples/01-simple-random-pet.yaml
kubectl get dores pet-simple -w              # READY True, ID = generated pet name

kubectl apply -f examples/02-referenced-pets.yaml
kubectl get dores                            # pet-dependent waits, then goes Ready
kubectl get dores pet-dependent -o jsonpath='{.status.outputs.id}'
#   -> from-<pet-base id>-...                # templated reference value

# Ordered teardown: the base refuses to die while the dependent exists
kubectl delete dores pet-base --wait=false
kubectl describe dores pet-base | grep Blocked
kubectl delete dores pet-dependent           # then pet-base finishes deleting

kubectl apply -f examples/03-bucket-with-policy.yaml
kubectl get dores -w                         # bucket Ready, then policy Ready

kubectl apply -f examples/04-composite-definition.yaml
kubectl apply -f examples/05-composite-instance.yaml
kubectl get docomposites                     # team-data 4/4 READY
kubectl get doresources                      # team-data-suffix/-bucket/-pab/-policy
kubectl tree docomposite team-data           # (if you have kubectl-tree)

# Update flows through the composition: children are patched in place
kubectl patch docomposite team-data --type merge -p '{"spec":{"parameters":{"env":"staging"}}}'

# Deleting the composite garbage-collects the children; the graph engine
# tears them down in reverse dependency order (policy/pab -> bucket -> suffix).
kubectl delete docomposite team-data

kubectl apply -f examples/06-reference-fan-in.yaml
kubectl get dores -w                         # graph-release waits for fan-in dependencies
kubectl get dores graph-shuffle -o jsonpath='{.status.outputs.results}'

# DigitalOcean example: creates a billable Droplet.
kubectl create secret generic provider-credentials \
  --from-literal=DIGITALOCEAN_TOKEN="$DIGITALOCEAN_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f examples/07-digitalocean-web-node.yaml
kubectl get docomposite do-web-dev -w        # do-web-dev 7/7 READY
kubectl delete docomposite do-web-dev        # delete when done to stop charges

# Provider profiles: package pinned once, resources reference the profile.
kubectl apply -f examples/09-provider-profile.yaml
kubectl get doproviders                      # random READY True
kubectl get dores pet-via-provider -w        # created with the profile's package
kubectl get dores integer-not-allowed        # SYNCED False / ResourceNotAllowed

# Cataloged composite as a typed platform API: app teams apply the
# generated PetIdentity kind with schema-validated parameters only.
# (Apply twice if the PetIdentity CRD is not served yet on the first pass.)
kubectl apply -f examples/10-cataloged-composite.yaml
kubectl get docd pet-identity                # SERVED True
kubectl get petidentities payments-identity -w
kubectl get docomposite payments-identity    # the owned mirror (machinery)

# Typed component API: platform registers the component package, then app
# teams apply the generated WebAppComponent kind.
kubectl apply -f examples/12-typed-private-registry-component-provider.yaml
kubectl wait doprovider typed-web-app --for=condition=Ready --timeout=2m
kubectl wait --for=condition=Established crd/webappcomponents.typed.do.pulumi.com --timeout=2m
kubectl apply -f examples/13-typed-private-registry-component.yaml
kubectl get webappcomponents.typed.do.pulumi.com typed-private-web-app -w
```
