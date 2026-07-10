---
title: Build from source
description: Contributor flow — build the manager and runner images from a checkout and deploy them to a test cluster.
---

# Build from source

<div class="agent-contract">
  <p><strong>Agent goal:</strong> build both images from the working tree and deploy them to a disposable cluster. This is the contributor flow; users installing doplane should follow <a href="/doplane/guide/getting-started">Getting started</a> instead.</p>
</div>

Use this path when you change doplane's code and want to run the result: released images from GHCR cannot contain your local changes.

## Prerequisites

- Docker or another container runtime supported by the Makefile
- Go 1.24 or later
- `kind` (a disposable local cluster keeps experiments away from real workloads)
- `kubectl`, GNU Make

## 1. Create a test cluster

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

The manager image watches Kubernetes objects; the runner image executes provider operations in short-lived Jobs. Do not replace the runner image with the manager image: the manager is distroless and contains neither `pulumi` nor provider plugins.

On a remote test cluster, push the images to a registry the cluster can pull from and use those references as `IMG` and `RUNNER_IMG`.

## 3. Install CRDs and deploy

```sh
make install deploy \
  IMG=doplane:dev \
  RUNNER_IMG=doplane-runner:dev

kubectl -n doplane-system rollout status deployment/doplane-controller-manager \
  --timeout=2m
```

This is the kustomize delivery path (`deploy/kustomize/`). The Helm chart (`deploy/doplane/`) must stay behaviorally equivalent — when you change one, mirror the other. Render both and inspect:

```sh
make helm-lint helm-template
bin/kustomize build deploy/kustomize/default
```

## 4. Verify with an example

```sh
kubectl apply -f examples/01-simple-random-pet.yaml
kubectl wait doresource/pet-simple --for=condition=Ready --timeout=2m
```

The `examples/` levels 1–6 and 9–11 need no cloud credentials. Repository conventions, test commands, and invariants live in [`AGENTS.md`](https://github.com/dirien/doplane/blob/main/AGENTS.md) — read it before editing code.

## Releasing

Tags matching `v*` trigger the release workflow: it builds multi-arch manager and runner images, pushes them to `ghcr.io/dirien/doplane` and `ghcr.io/dirien/doplane-runner`, publishes the chart to `oci://ghcr.io/dirien/charts/doplane`, and creates the GitHub release.

## Clean up

```sh
kind delete cluster --name doplane
```
