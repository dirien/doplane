---
title: Test a release end to end
description: Install a released doplane chart on a fresh kind cluster and run every example, first without cloud credentials, then with them.
---

# Test a release end to end

<div class="agent-contract">
  <p><strong>Agent goal:</strong> prove that a released chart works on a clean cluster by running the examples and confirming that every object reaches <code>Ready=True</code> and deletes cleanly. Stop before the credentialed passes unless the cloud accounts named below are meant to be used.</p>
</div>

This is the acceptance run for a release. It uses only published artifacts: the chart from `oci://ghcr.io/dirien/charts/doplane` and the images it pins. Nothing is built locally. The credential-free pass takes about ten minutes; the AWS pass takes closer to an hour if you run all 26 patterns.

The commands assume a checkout of the repository for the example files:

```sh
git clone https://github.com/dirien/doplane.git
cd doplane
```

## Prerequisites

For the credential-free pass:

- Docker and `kind`
- `kubectl`
- Helm 3.8 or later (OCI registry support)

For the credentialed passes, in addition:

- The `pulumi` and `esc` CLIs, logged in to a Pulumi Cloud account that can open the ESC environment `pulumi-idp/auth` (it holds the short-lived AWS credentials the examples use). Any ESC environment that exports `AWS_*` variables works: set `ESC_ENV` when you run the sync script.
- A DigitalOcean API token, for example 07 only.
- Access to the Pulumi Cloud private registry of the `ediri` organization, for examples 08, 12, 13 and AWS example 26. These pull a component package that is not public.

## 1. Start from a clean cluster

Remove any earlier test cluster so cached images and leftover objects cannot mask a problem:

```sh
kind delete cluster --name doplane
kind create cluster --name doplane
kubectl config current-context   # kind-doplane
```

## 2. Install the released chart

Pin the version you are testing. The chart's `appVersion` selects the manager and runner image tags, so one flag pins everything:

```sh
helm install doplane oci://ghcr.io/dirien/charts/doplane \
  --version 0.3.0 \
  --namespace doplane-system \
  --create-namespace

kubectl -n doplane-system rollout status deployment/doplane-controller-manager --timeout=3m
```

The runner image is large (it carries the Pulumi CLI, language toolchains and baked provider plugins), so the first runner Job on a node spends a minute or two pulling it. That happens once per node.

Confirm the install before applying anything:

```sh
helm list -n doplane-system                       # STATUS deployed, APP VERSION 0.3.0
kubectl get crd | grep do.pulumi.com              # seven CRDs
kubectl -n doplane-system get pods                # manager 1/1 Running
kubectl -n doplane-system logs deploy/doplane-controller-manager | head -20
```

The log's first lines name the runner image and the namespace mode (`operator` by default). Runner Jobs run in `doplane-system` in that mode, and that is where every credential Secret in this guide goes.

## 3. Credential-free pass

Seven example files need no cloud account. Apply them in this order:

```sh
kubectl apply -f examples/01-simple-random-pet.yaml
kubectl apply -f examples/02-referenced-pets.yaml
kubectl apply -f examples/06-reference-fan-in.yaml
kubectl apply -f examples/09-provider-profile.yaml
kubectl apply -f examples/11-secrets-in-and-out.yaml
kubectl apply -f examples/10-cataloged-composite.yaml
kubectl apply -f examples/14-platform-api-parameters.yaml
```

Files 10 and 14 each end with an object of a kind the operator generates from the definition in the same file (`PetIdentity`, `ServiceIdentity`). On the first apply the API server rejects that last document with `no matches for kind`. That is expected. Wait for the definitions to be served, then apply the two files again:

```sh
kubectl wait docompositedefinition/pet-identity docompositedefinition/service-identity \
  --for=condition=APIServed --timeout=2m
kubectl apply -f examples/10-cataloged-composite.yaml
kubectl apply -f examples/14-platform-api-parameters.yaml
```

Now watch everything settle. On a laptop this takes under a minute:

```sh
kubectl get doresources -w
```

Stop watching when the list stops changing. This is the state to compare against:

```sh
kubectl get doresources
```

```
NAME                       TYPE                                       ID                              READY   SYNCED   REASON               AGE
graph-env                  random:index/randomPet:RandomPet           env-kite                        True    True     Synced               2m
graph-release              random:index/randomPet:RandomPet           release-api-env-kite-...        True    True     Synced               2m
graph-shuffle              random:index/randomShuffle:RandomShuffle   -                               True    True     Synced               2m
graph-token                random:index/randomString:RandomString     x2lejv42                        True    True     Synced               2m
integer-not-allowed        random:index/randomInteger:RandomInteger                                   False   False    ResourceNotAllowed   2m
payments-identity-pet      random:index/randomPet:RandomPet           payments-exciting-bee           True    True     Synced               1m
payments-identity-suffix   random:index/randomString:RandomString     451mba                          True    True     Synced               1m
payments-prod-pet          random:index/randomPet:RandomPet           payments-prod-needlessly-...    True    True     Synced               1m
payments-prod-token        random:index/randomString:RandomString     grs29co7e2cq                    True    True     Synced               1m
pet-base                   random:index/randomPet:RandomPet           feasible-mollusk                True    True     Synced               2m
pet-dependent              random:index/randomPet:RandomPet           from-feasible-mollusk-eagle     True    True     Synced               2m
pet-secretive              random:index/randomPet:RandomPet           sealed-decent-chipmunk          True    True     Synced               2m
pet-simple                 random:index/randomPet:RandomPet           level1-premium-badger           True    True     Synced               2m
pet-via-provider           random:index/randomPet:RandomPet           level9-wise-pangolin            True    True     Synced               2m
```

Fourteen objects. Thirteen are `Ready=True` and `Synced=True`. The one exception is deliberate: `integer-not-allowed` from example 09 asks for a resource type its `DoProvider` profile does not allow, and `ResourceNotAllowed` is the correct answer. Anything else that is not `True/True` is a failure.

Then check the composites, the typed platform-API objects and the provider profiles:

```sh
kubectl get docomposites                          # payments-identity, payments-prod: READY True
kubectl get petidentities,serviceidentities       # both READY True, SYNCED True
kubectl get doproviders                           # random, random-catalog, random-params: READY True
kubectl get jobs -A -l app.kubernetes.io/managed-by=doplane   # empty, or one short-lived read Job
```

A few things are worth looking at individually because they show behavior the summary table cannot:

```sh
# Example 02: the dependent's prefix was templated from pet-base's id.
kubectl get doresource pet-dependent -o jsonpath='{.status.outputs.id}{"\n"}'

# Example 06: an array output travelled through a reference.
kubectl get doresource graph-shuffle -o jsonpath='{.status.outputs.results}{"\n"}'

# Example 11: the connection Secret was published from status. The input
# value is absent from the spec and from events. The random provider does
# echo it back in status.outputs (its keepers map), which is why the docs
# call status sensitive-adjacent; see Security and tenancy.
kubectl get secret pet-connection -o jsonpath='{.data.petName}' | base64 -d; echo
kubectl get doresource pet-secretive -o jsonpath='{.spec.properties}'; echo        # length and prefix only
kubectl get events --field-selector involvedObject.name=pet-secretive -o json \
  | grep -c super-secret-rotation-token                                              # 0

# Example 14: typed parameters kept their native types on the way in.
kubectl get doresource payments-prod-pet -o jsonpath='{.spec.properties.length}{"\n"}'   # a number, not a string

# Examples 10 and 14: the definitions' outputs reached the typed objects.
kubectl get petidentities                                          # IDENTITY column is filled
kubectl get petidentity payments-identity -o jsonpath='{.status.outputs}{"\n"}'
kubectl get serviceidentity payments-prod -o jsonpath='{.status.outputs.tokenLength}{"\n"}'   # 12, a number
```

### Teardown

Delete in reverse order. A `DoResource` is only gone once its finalizer has deleted the external resource, so `kubectl delete -f` on the raw-resource files returns when the provider deletes are done, and a hang there is a failure. A composite is different: the `DoComposite` (or typed object) disappears at once and its children drain in the background, in reverse dependency order, through garbage collection. After deleting composite files, wait until `kubectl get doresources` is empty before judging the result:

```sh
for f in 14-platform-api-parameters 10-cataloged-composite 11-secrets-in-and-out \
         09-provider-profile 06-reference-fan-in 02-referenced-pets 01-simple-random-pet; do
  kubectl delete -f "examples/$f.yaml" --wait=true --timeout=5m
done
```

During teardown you may see transient `ProviderNotFound` or `DeletionBlocked` events. A multi-document file deletes its definition and provider profile in the same call as the objects that use them; the finalizers order the actual work and the warnings stop once the children are gone. The end state must be empty:

```sh
kubectl get doresources,docomposites,docompositedefinitions,doproviders -A   # No resources found
kubectl get jobs -A -l app.kubernetes.io/managed-by=doplane                  # No resources found
kubectl get crd | grep -c 'typed.do.pulumi.com'                              # 0
```

Random-provider resources have no read support, so their deletes run through the recorded state (see the root README, "Delete without read"). Before 0.2.0 this pass left every random pet stuck in `Terminating`; a repeat of that is the first thing this teardown would reveal.

## 4. Credentialed passes

Each pass adds keys to one Secret, `provider-credentials` in `doplane-system`, which runner Jobs load as environment variables. The sync script replaces that Secret on every run, so add any extra token after syncing (the patch below survives later syncs, because `kubectl apply` only removes keys it wrote itself).

### AWS

Sync the credentials from ESC. The OIDC credentials in `pulumi-idp/auth` expire after about an hour; rerun the script when a Job starts failing with `ExpiredToken` or `InvalidClientTokenId`:

```sh
./hack/sync-creds.sh                     # ESC_ENV=<org>/<env> to use a different environment
kubectl -n doplane-system get secret provider-credentials -o jsonpath='{.data}' | grep -o '"AWS_[A-Z_]*"'
```

Start with the two small examples from the top-level set:

```sh
kubectl apply -f examples/03-bucket-with-policy.yaml
kubectl get doresources -w               # bucket Ready first, then policy Ready

kubectl apply -f examples/04-composite-definition.yaml
kubectl apply -f examples/05-composite-instance.yaml
kubectl get docomposites                 # team-data 4/4 READY
kubectl get doresources                  # team-data-suffix, -bucket, -pab, -policy

kubectl patch docomposite team-data --type merge -p '{"spec":{"parameters":{"env":"staging"}}}'
kubectl get doresources -w               # children patched in place, Ready again

kubectl delete -f examples/05-composite-instance.yaml
kubectl delete -f examples/04-composite-definition.yaml
kubectl delete -f examples/03-bucket-with-policy.yaml
kubectl get doresources                  # No resources found
```

Then the AWS pattern library. Each file defines one pattern and two instances (`dev` and `prod`), so a full run creates 50 composites. Everything is chosen to be free or a few cents, and to delete cleanly. KMS keys go into a seven-day pending deletion, which is normal:

```sh
kubectl apply -f examples/aws/01-secure-bucket.yaml
kubectl get docomposites -w              # bkt-dev, bkt-prod: 6/6 READY

# the whole library
for f in examples/aws/[0-2][0-9]-*.yaml; do kubectl apply -f "$f"; done
kubectl get docomposites                 # 50 composites; watch READY reach n/n on each
```

Skip `examples/aws/26-component-secure-bucket.yaml` in the loop above unless you also completed the private registry setup below; it is a component from that registry.

Let the run settle, then read the failure list. It must be empty:

```sh
kubectl get doresources -A -o json \
  | jq -r '.items[] | select(.status.conditions[]? | select(.type=="Ready" and .status!="True")) | "\(.metadata.name)\t\(.status.conditions[] | select(.type=="Synced") | .reason)"'
```

Teardown drains in reverse dependency order and must end empty:

```sh
for f in examples/aws/[0-2][0-9]-*.yaml; do kubectl delete -f "$f" --wait=true --timeout=10m; done
kubectl get doresources -w               # composites' children drain in the background; wait for an empty list
kubectl get doresources,docomposites -A  # No resources found
```

A resource whose delete keeps failing shows `DeleteFailed` in its events with the provider's message. Most of the time that is an expired credential; resync and the next retry succeeds.

### DigitalOcean

Example 07 defines a web node and creates two instances of it, a raw `DoComposite` and a typed `WebNode` in the platform group `web.ediri.io`. Each instance is a small Droplet, billable while it exists. The platform group has to be on the operator's allowlist first:

```sh
helm upgrade doplane oci://ghcr.io/dirien/charts/doplane --version 0.3.0 \
  --namespace doplane-system --reuse-values \
  --set 'compositeApiGroups={web.ediri.io}'
kubectl -n doplane-system rollout status deployment/doplane-controller-manager --timeout=3m
```

Add the token after the AWS sync so both sets of keys are present. The sync script copies only `AWS_*` variables, but the same ESC environment also exports `DIGITALOCEAN_ACCESS_TOKEN`, a name the provider accepts alongside `DIGITALOCEAN_TOKEN`, so it can be taken from there without printing it:

```sh
pulumi env run ediri/pulumi-idp/auth -- sh -c \
  'kubectl -n doplane-system patch secret provider-credentials --type merge \
     -p "{\"stringData\":{\"DIGITALOCEAN_ACCESS_TOKEN\":\"$DIGITALOCEAN_ACCESS_TOKEN\"}}"'
```

With a token from anywhere else, patch it in directly. Then run the example:

```sh
kubectl -n doplane-system patch secret provider-credentials --type merge \
  -p "{\"stringData\":{\"DIGITALOCEAN_TOKEN\":\"$DIGITALOCEAN_TOKEN\"}}"

kubectl apply -f examples/07-digitalocean-web-node.yaml       # the WebNode document fails until the API is served
kubectl wait docompositedefinition/digitalocean-web-node --for=condition=APIServed --timeout=2m
kubectl apply -f examples/07-digitalocean-web-node.yaml
kubectl get docomposite do-web-dev -w    # 7/7 READY
kubectl get webnodes                     # web-typed READY True, URL column filled
curl -sI "$(kubectl get webnode web-typed -o jsonpath='{.status.outputs.url}')" | head -1   # HTTP/1.1 200 OK once cloud-init finished nginx
kubectl delete -f examples/07-digitalocean-web-node.yaml --wait=true --timeout=10m
kubectl get doresources -w               # children drain in dependency order; stop when the list is empty
```

Both droplets, VPCs, projects, tags and firewalls are gone from the account once the list is empty; `doctl compute droplet list` and `doctl vpcs list` confirm it.

If you have not run the AWS sync, create the Secret with the token alone instead of patching a Secret that does not exist yet:

```sh
kubectl -n doplane-system create secret generic provider-credentials \
  --from-literal=DIGITALOCEAN_TOKEN="$DIGITALOCEAN_TOKEN"
```

The slugs the example uses (`nyc3`, `s-1vcpu-1gb`, `ubuntu-24-04-x64`) were checked against the DigitalOcean API on 2026-09-13. If a later run fails on one of them, `doctl compute region list`, `doctl compute size list` and `doctl compute image get ubuntu-24-04-x64` show the current state.

### Pulumi Cloud private registry components

Examples 08, 12 and 13 (and AWS example 26) resolve a component package from the private registry of the `ediri` organization, compile it in the runner and drive it through the component engine. They need a Pulumi access token, and the kind-targeting ones also need a kubeconfig the runner pod can use from inside the cluster:

```sh
INCLUDE_PULUMI_TOKEN=1 INCLUDE_KIND_KUBECONFIG=1 ./hack/sync-creds.sh
```

The kubeconfig grants runner pods admin on the kind cluster. That is fine for a disposable test cluster and nothing else.

```sh
kubectl apply -f examples/08-private-registry-component.yaml
kubectl get doresource private-web-app -w   # Ready once the component's Deployment and Service exist
kubectl get deploy,svc                      # the component's Deployment and Service, created in this cluster

kubectl apply -f examples/12-typed-private-registry-component-provider.yaml
kubectl wait doprovider typed-web-app --for=condition=Ready --timeout=3m
kubectl wait --for=condition=Established crd/webappcomponents.typed.do.pulumi.com --timeout=2m
kubectl apply -f examples/13-typed-private-registry-component.yaml
kubectl get webappcomponents.typed.do.pulumi.com -w

kubectl apply -f examples/aws/26-component-secure-bucket.yaml
kubectl get docomposites -w
```

The first component run compiles the package inside the runner and can take several minutes; later runs reuse the compiled package only when the plugin cache is enabled in the chart, so expect the same delay per Job otherwise.

Teardown:

```sh
kubectl delete -f examples/aws/26-component-secure-bucket.yaml --wait=true --timeout=10m
kubectl delete -f examples/13-typed-private-registry-component.yaml --wait=true --timeout=10m
kubectl delete -f examples/12-typed-private-registry-component-provider.yaml
kubectl delete -f examples/08-private-registry-component.yaml --wait=true --timeout=10m
kubectl get deploy,svc                   # only the kubernetes Service remains
```

## 5. When something is not Ready

Read the object before reading logs. The `REASON` column is the runner's own failure code and the condition message carries the provider's error text:

```sh
kubectl describe doresource <name>       # conditions and the last events
kubectl get jobs -A -l app.kubernetes.io/managed-by=doplane
kubectl -n doplane-system logs job/<job-name>            # the provider's output for one operation
kubectl -n doplane-system logs deploy/doplane-controller-manager --since=10m
```

[Conditions and failures](/reference/conditions) lists every reason and what to do about it. The ones that come up in this run:

- `SecretInputMissing` on example 11: the input Secret is not in the namespace the runner Job runs in. With the default `runner.namespaceMode=operator` it belongs in `doplane-system`, which is where the example puts it.
- `ResourceNotAllowed` on `integer-not-allowed`: intended, see above.
- `OperationFailed` with `ExpiredToken` in the message: rerun `./hack/sync-creds.sh`.
- `RegistryAuthMissing` or `RegistryResolveFailed`: the Pulumi access token is missing from the Secret or cannot see the `ediri` registry.
- An object stuck in `Terminating`: `kubectl describe` shows either `BlockedByDependents` (delete the dependents first, or wait) or a `DeleteFailed` event with the provider's reason. If the external resource is already gone and the provider keeps failing, `kubectl patch doresource <name> --type merge -p '{"spec":{"deletionPolicy":"Orphan"}}'` releases the finalizer without another provider call.

## 6. Clean up

```sh
helm uninstall doplane --namespace doplane-system
kind delete cluster --name doplane
```

`helm uninstall` leaves the CRDs and any remaining doplane objects in place, which is why the teardown steps above finish with an empty list before this point. Deleting the kind cluster removes everything else, including the credential Secret.
