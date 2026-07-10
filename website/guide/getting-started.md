---
title: Getting started
description: Build doplane, install it on kind, and reconcile a credential-free resource.
---

# Getting started

<div class="agent-contract">
  <p><strong>Agent goal:</strong> install doplane on a local kind cluster and prove that one <code>DoResource</code> reaches <code>Ready=True</code>. Stop if an existing cluster or cloud resource could be affected.</p>
</div>

This path uses the `random` provider, so it needs no cloud account and creates no billable resource.

## Prerequisites

Use a machine with these tools:

- Docker or another container runtime supported by the Makefile
- Go 1.24 or later
- `kind`
- `kubectl`
- GNU Make

The build creates two images. The manager watches Kubernetes objects; the runner image executes provider operations in short-lived Jobs.

## 1. Clone and create the cluster

```sh
git clone https://github.com/dirien/doplane.git
cd doplane

kind create cluster --name doplane
```

Confirm that `kubectl config current-context` prints `kind-doplane` before continuing.

## 2. Build and load both images

```sh
make docker-build docker-build-runner \
  IMG=doplane:dev \
  RUNNER_IMG=doplane-runner:dev

kind load docker-image doplane:dev doplane-runner:dev --name doplane
```

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
- a completed runner Job exists in the runner namespace.

Inspect the full object and recent events when a condition stays false:

```sh
kubectl describe doresource pet-simple
kubectl get jobs --all-namespaces
```

## 5. Make a declarative change

```sh
kubectl patch doresource pet-simple --type merge \
  -p '{"spec":{"properties":{"length":3,"prefix":"docs"}}}'
kubectl get doresource pet-simple -w
```

The `random` provider does not support every read or update operation. A terminal `UpdateNotSupported` condition is provider behavior, not proof that the control loop is broken. Use a provider resource with read and patch support when testing drift correction.

## Clean up

```sh
kubectl delete doresource pet-simple
kind delete cluster --name doplane
```

Deletion waits for the provider delete unless `spec.deletionPolicy` is `Orphan`.

## Next task

Choose the right abstraction before adding a cloud resource: [raw, provider-backed, typed, or composite API](/guide/choose-an-api).
