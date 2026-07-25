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

See [Invoking `e2e-test.sh` directly (without `make`)](https://kueue.sigs.k8s.io/community/contribution_guidelines/testing/#invoking-e2e-testsh-directly-without-make) for background on the general-purpose env vars (`ARTIFACTS`, `IMAGE_TAG`, `E2E_USE_HELM`, `GINKGO_ARGS`, `E2E_MODE`, tty-less cleanup prompt behavior). The WAS-specific invocation is:

```bash
E2E_KIND_VERSION=kindest/node:was-main \
E2E_MODE=dev \
E2E_RUN_ONLY_ENV=true \
WAS_ENABLED=1 \
KIND_CLUSTER_NAME=was-test \
KIND_CLUSTER_FILE=kind-cluster.yaml \
E2E_TARGET_FOLDER=singlecluster/was \
ARTIFACTS="$(pwd)/artifacts/was-test" \
IMAGE_TAG=local/kueue:was-test \
E2E_USE_HELM=false \
GINKGO_ARGS="" \
  ./hack/testing/e2e-test.sh
```

Note: only `singlecluster/was` (Job integration) exists today. The former `was/extended`
(JobSet) suite asserted against the upstream `scheduling.k8s.io/v1alpha3` Go types
directly, which would have required vendoring that alpha API into Kueue, so it was
removed. Re-add JobSet coverage once there's a way to verify it without vendoring
the upstream types.

The `singlecluster/was` package lives under `test/e2e/singlecluster/` (rather than a
top-level `test/e2e/was/`) so that `make test-e2e-k8s-main-was` — which targets the
whole `singlecluster` tree with `WAS_ENABLED=true` — actually runs it. Regular,
non-WAS runs (`make test-e2e-baseline`, `make test-e2e-extended`) target only the
`singlecluster/baseline` and `singlecluster/extended` subfolders respectively, so
they never pick up `singlecluster/was`, which requires feature gates
(`GenericWorkload`, `WorkloadWithJob`) and the `scheduling.k8s.io/v1beta1` API that
only a WAS-patched cluster provides.

### WAS-specific environment variables

These are specific to WAS; see the guideline link above for the rest.

| Variable | Value | Why |
|----------|-------|-----|
| `E2E_KIND_VERSION` | `kindest/node:was-main` | Uses pre-built node image; skips `build_kind_node_image` (non-standard name) |
| `WAS_ENABLED` | `1` | Triggers `patch_kind_config_for_was` adding the `GenericWorkload`/`WorkloadWithJob` feature gates and `scheduling.k8s.io/v1beta1` runtime-config |
| `KIND_CLUSTER_NAME` | `was-test` | Cluster name |
| `KIND_CLUSTER_FILE` | `kind-cluster.yaml` | Base kind config that gets patched by `patch_kind_config_for_was` |
| `E2E_TARGET_FOLDER` | `singlecluster/was` | Points at the WAS test folder |

## Run tests

Once the cluster is up, run tests repeatedly without rebuilding:

```bash
KIND_CLUSTER_NAME=was-test go test ./test/e2e/singlecluster/was/... -v -count=1
```

Filter by label:

```bash
# Job integration tests only
KIND_CLUSTER_NAME=was-test go test ./test/e2e/singlecluster/was/... -v -count=1 --ginkgo.label-filter="feature:was-job"
```

## Re-deploy Kueue after code changes

See [Re-deploy Kueue after code changes (dev mode)](https://kueue.sigs.k8s.io/community/contribution_guidelines/testing/#re-deploy-kueue-after-code-changes-dev-mode) in the contributor guideline; substitute `was-test` for `<cluster-name>` and the image tag used above.

## Tear down

```bash
kind delete cluster --name was-test
```

## Troubleshooting

- **`kind create` fails with kubeadm v1beta3 error**: You need `kind` >= v0.32.0. Check with `kind version`.
- **`scheduling.k8s.io/v1beta1` not in `kubectl api-resources`**: The k8s checkout must be at `main` (post WAS beta graduation). Rebuild the node image.
- **No PodGroups created for Jobs**: Jobs must qualify for gang scheduling: `parallelism > 1`, `completionMode: Indexed`, `completions == parallelism`. This is the `WorkloadWithJob` feature gate behavior (KEP-5547). Verify both gates landed on the controller-manager: `kubectl -n kube-system get pod -l component=kube-controller-manager -o jsonpath='{.items[0].spec.containers[0].command}' | tr ' ' '\n' | grep feature-gates` should show `GenericWorkload=true,WorkloadWithJob=true`.
- **`ARTIFACTS: unbound variable` / `IMAGE_TAG: unbound variable` / `E2E_USE_HELM: unbound variable` / `GINKGO_ARGS: unbound variable`**: `e2e-test.sh` has `set -o nounset` and relies on `make` to default these. Set them explicitly when invoking the script directly (see Step 2 above).
