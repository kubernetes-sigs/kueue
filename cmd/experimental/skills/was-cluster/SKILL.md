---
name: was-cluster
description: Build and manage a kind cluster for Workload-Aware Scheduling (WAS) e2e tests. Use when the user wants to set up, run tests against, or tear down a WAS test cluster built from Kubernetes main.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# WAS Cluster

Sets up a kind cluster from Kubernetes `main` with `GenericWorkload` and `WorkloadWithJob` feature gates enabled, deploys Kueue, and runs WAS e2e tests.

## Prerequisites

- `kind` >= v0.32.0 (required for k8s main which dropped kubeadm v1beta3)
- A local Kubernetes checkout at `main` (the user should tell you where it is, or check `~/Work/kubernetes`)
- Docker running

## Setup: Build the cluster and leave it running

### Step 1: Build the kind node image from k8s main

```bash
kind build node-image /path/to/kubernetes --image kindest/node:was-main
```

This only needs to be done once (or when the k8s checkout is updated). In dev mode, if the image already exists it will be reused.

### Step 2: Stand up the cluster with Kueue deployed, no tests

```bash
E2E_KIND_VERSION=kindest/node:was-main \
E2E_MODE=dev \
E2E_RUN_ONLY_ENV=true \
WAS_ENABLED=1 \
KIND_CLUSTER_NAME=was-test \
KIND_CLUSTER_FILE=kind-cluster.yaml \
E2E_TARGET_FOLDER=was/baseline \
  ./hack/testing/e2e-test.sh
```

When prompted "Do you want to cleanup?", answer `n` to keep the cluster.

### Environment variables explained

| Variable | Value | Why |
|----------|-------|-----|
| `E2E_KIND_VERSION` | `kindest/node:was-main` | Uses pre-built node image; skips `build_kind_node_image` (non-standard name) |
| `E2E_MODE` | `dev` | Reuses existing cluster on re-runs, keeps cluster on exit |
| `E2E_RUN_ONLY_ENV` | `true` | Sets up cluster + Kueue, exits before running tests |
| `WAS_ENABLED` | `1` | Triggers `patch_kind_config_for_was` adding `GenericWorkload` feature gate and `scheduling.k8s.io/v1alpha3` runtime-config |
| `KIND_CLUSTER_NAME` | `was-test` | Cluster name |
| `KIND_CLUSTER_FILE` | `kind-cluster.yaml` | Base kind config that gets patched by `patch_kind_config_for_was` |
| `E2E_TARGET_FOLDER` | `was/baseline` | Points at the WAS test folder |

## Run tests

Once the cluster is up, run tests repeatedly without rebuilding:

```bash
KIND_CLUSTER_NAME=was-test go test ./test/e2e/was/baseline/... -v -count=1
```

Filter by label:

```bash
# Job integration tests only
KIND_CLUSTER_NAME=was-test go test ./test/e2e/was/baseline/... -v -count=1 --ginkgo.label-filter="feature:was-job"

# Preemption tests only
KIND_CLUSTER_NAME=was-test go test ./test/e2e/was/baseline/... -v -count=1 --ginkgo.label-filter="feature:was-preemption"
```

## Re-deploy Kueue after code changes

```bash
# Rebuild, reload, restart
make -e IMAGE_TAG=local/kueue:was-test image-build IMAGE_BUILD_EXTRA_OPTS="--load" PLATFORMS="linux/amd64"
kind load docker-image local/kueue:was-test --name was-test
kubectl -n kueue-system rollout restart deploy/kueue-controller-manager
kubectl -n kueue-system rollout status deploy/kueue-controller-manager --timeout=120s
```

Or re-run the setup command — `E2E_MODE=dev` makes it idempotent.

## Tear down

```bash
kind delete cluster --name was-test
```

## Troubleshooting

- **`kind create` fails with kubeadm v1beta3 error**: You need `kind` >= v0.32.0. Check with `kind version`.
- **`scheduling.k8s.io/v1alpha3` not in `kubectl api-resources`**: The k8s checkout must be at `main` (post v1alpha2→v1alpha3 rename). Rebuild the node image.
- **No PodGroups created for Jobs**: Jobs must qualify for gang scheduling: `parallelism > 1`, `completionMode: Indexed`, `completions == parallelism`. This is the `WorkloadWithJob` feature gate behavior (KEP-5547).
- **PodGroup priority is 0**: No automatic mapping from Kueue's `WorkloadPriorityClass` to upstream PodGroup priority exists yet. See `test/e2e/was/WORKLOAD_AWARE_PREEMPTION_ANALYSIS.md`.
