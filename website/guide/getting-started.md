---
title: Getting started
description: Install the released doplane operator on a Kubernetes cluster and reconcile a credential-free resource.
---

# Getting started

<div class="agent-contract">
  <p><strong>Agent goal:</strong> install the released doplane chart on a Kubernetes cluster and prove that one <code>DoResource</code> reaches <code>Ready=True</code>. Stop if an existing cluster or cloud resource could be affected.</p>
</div>

doplane runs on any Kubernetes cluster — managed (EKS, GKE, AKS), on-prem, or local. Released images are published to GHCR, so installation is one Helm command; nothing needs to be built. The example uses the `random` provider, so it needs no cloud account and creates no billable resource.

Working on doplane itself, or want images built from your own checkout? That is a contributor flow: see [Build from source](/guide/build-from-source).

## Prerequisites

- `kubectl` pointing at the cluster you want to use
- Helm 3.8 or later (OCI registry support)
- No cluster yet? `kind create cluster --name doplane` gives you a disposable local one

## 1. Install doplane

```sh
helm install doplane oci://ghcr.io/dirien/charts/doplane \
  --namespace doplane-system \
  --create-namespace

kubectl -n doplane-system rollout status deployment/doplane-controller-manager \
  --timeout=2m
```

The chart installs the CRDs and a manager Deployment pinned to the released manager and runner images. Add `--version <x.y.z>` to install a specific release; the [releases page](https://github.com/dirien/doplane/releases) lists them.

## 2. Reconcile a resource

```sh
kubectl apply -f https://raw.githubusercontent.com/dirien/doplane/main/examples/01-simple-random-pet.yaml
kubectl wait doresource/pet-simple --for=condition=Ready --timeout=2m
kubectl get doresource pet-simple \
  -o jsonpath='{.status.id}{"\n"}{.status.conditions}{"\n"}'
```

The resource has succeeded when:

- `.status.id` contains a generated pet name;
- `Ready=True` reports that the external resource exists;
- `Synced=True` reports that the last provider operation succeeded;
- a `Created` event on the object records the provider call.

Each provider operation runs in a short-lived runner Job that the manager deletes after consuming its result — so on success, no Job remains. Inspect the full object, its events, and any still-running Job when a condition stays false:

```sh
kubectl describe doresource pet-simple
kubectl get jobs --all-namespaces -l app.kubernetes.io/managed-by=doplane
```

## 3. Make a declarative change

```sh
kubectl patch doresource pet-simple --type merge \
  -p '{"spec":{"properties":{"length":3,"prefix":"docs"}}}'
kubectl get doresource pet-simple -w
```

The `random` provider does not support in-place reads or updates, so this patch ends with a terminal `Synced=False` / `UpdateNotSupported` condition. That is provider behavior, not proof that the control loop is broken — reverting the spec settles the resource again. Use a provider resource with read and patch support when testing drift correction.

## 4. Pick your next resource

The pet came from one provider in a much larger catalog. Any resource in the [Pulumi Registry](https://www.pulumi.com/registry/) — and any published component package — can become a manifest like the one you just applied. [Discover providers and components](/guide/discover) shows how to find the type token, pin the package, and generate a ready-to-edit example.

## Clean up

```sh
kubectl delete doresource pet-simple
helm uninstall doplane --namespace doplane-system
kind delete cluster --name doplane   # only if you created it above
```

Deletion waits for the provider delete unless `spec.deletionPolicy` is `Orphan`. `helm uninstall` keeps the CRDs (and therefore any remaining doplane objects); delete them explicitly only when nothing references external resources anymore.

## Next task

Publish your first branded platform API with the same credential-free provider: [Your first platform API](/guide/first-platform-api). Or choose the right abstraction before adding a cloud resource: [raw, provider-backed, typed, or composite API](/guide/choose-an-api).
