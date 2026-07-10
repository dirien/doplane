---
title: Getting started
description: Build doplane, install it on a Kubernetes cluster, and reconcile a credential-free resource.
---

# Getting started

<div class="agent-contract">
  <p><strong>Agent goal:</strong> install doplane on a Kubernetes cluster and prove that one <code>DoResource</code> reaches <code>Ready=True</code>. Stop if an existing cluster or cloud resource could be affected.</p>
</div>

doplane runs on any Kubernetes cluster — managed (EKS, GKE, AKS), on-prem, or local. This walkthrough uses a disposable local [kind](https://kind.sigs.k8s.io/) cluster because it needs nothing but Docker; notes call out the one step that differs on a remote cluster. The example uses the `random` provider, so it needs no cloud account and creates no billable resource.

## Prerequisites

Use a machine with these tools:

- Docker or another container runtime supported by the Makefile
- Go 1.24 or later
- `kubectl` pointing at the cluster you want to use (or `kind` to create one)
- GNU Make

The build creates two images. The manager watches Kubernetes objects; the runner image executes provider operations in short-lived Jobs.

## 1. Clone and pick a cluster

```sh
git clone https://github.com/dirien/doplane.git
cd doplane

kind create cluster --name doplane
```

Already have a cluster? Skip `kind create cluster` — every later step works against whatever `kubectl config current-context` points at. Confirm the context is the intended cluster before continuing (`kind-doplane` if you created one here).

## 2. Build and load both images

```sh
make docker-build docker-build-runner \
  IMG=doplane:dev \
  RUNNER_IMG=doplane-runner:dev

kind load docker-image doplane:dev doplane-runner:dev --name doplane
```

On a remote cluster, replace `kind load` by pushing both images to a registry the cluster can pull from, and use those references as `IMG` and `RUNNER_IMG` in the next step.

Do not replace the runner image with the manager image. The manager is distroless and contains neither `pulumi` nor provider plugins.

## 3. Install CRDs and deploy

```sh
make install deploy \
  IMG=doplane:dev \
  RUNNER_IMG=doplane-runner:dev

kubectl -n doplane-system rollout status deployment/doplane-controller-manager \
  --timeout=2m
```

If your checkout deploys to another namespace, locate the manager with:

```sh
kubectl get deployments --all-namespaces | grep doplane
```

## 4. Reconcile a resource

```sh
kubectl apply -f examples/01-simple-random-pet.yaml
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

## 5. Make a declarative change

```sh
kubectl patch doresource pet-simple --type merge \
  -p '{"spec":{"properties":{"length":3,"prefix":"docs"}}}'
kubectl get doresource pet-simple -w
```

The `random` provider does not support in-place reads or updates, so this patch ends with a terminal `Synced=False` / `UpdateNotSupported` condition. That is provider behavior, not proof that the control loop is broken — reverting the spec settles the resource again. Use a provider resource with read and patch support when testing drift correction.

## Clean up

```sh
kubectl delete doresource pet-simple
kind delete cluster --name doplane   # only if you created it above
```

Deletion waits for the provider delete unless `spec.deletionPolicy` is `Orphan`.

## Next task

Choose the right abstraction before adding a cloud resource: [raw, provider-backed, typed, or composite API](/guide/choose-an-api).
