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
```
