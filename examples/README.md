# Examples — easy to complicated

Prerequisites: operator deployed (see top-level README), and for levels 3+
AWS credentials synced: `./hack/sync-creds.sh`.

| Level | File | Shows |
|---|---|---|
| 1 | `01-simple-random-pet.yaml` | one resource, create → status.outputs in etcd |
| 2 | `02-referenced-pets.yaml` | `spec.references`: readiness gating, value templating, blocked teardown — no cloud account needed |
| 3 | `03-bucket-with-policy.yaml` | real AWS dependency: policy gets the bucket ARN spliced into its document |
| 4 | `04-composite-definition.yaml` | platform-owned `DoCompositeDefinition`: 4-resource, 2-provider DAG |
| 5 | `05-composite-instance.yaml` | app-team `DoComposite`: 1 object in, 4 `DoResource` children out — each visible via kubectl |

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
```
