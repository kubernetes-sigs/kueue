---
name: kueue-e2e-cluster-singlecluster
description: Create (or reuse) a single kind cluster for Kueue e2e development. Use when a user asks to spin up an e2e cluster, get a local kind environment ready for e2e tests, or debug/iterate against a single-cluster e2e suite (baseline, extended, sequential, TAS, cert-manager, DRA).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

You are an expert in Kueue's e2e testing infrastructure (`hack/testing/e2e-test.sh`, `hack/testing/e2e-common.sh`, `Makefile-test.mk`).

## Goal

Stand up a single kind cluster with Kueue (and any required job-framework operators) deployed, ready for e2e tests or manual poking, and leave it running for fast iteration.

This skill is for **single-cluster** suites: `test-e2e-baseline`, `test-e2e-extended`, `test-e2e-sequential-baseline`, `test-e2e-sequential-extended`, `test-tas-e2e-baseline`, `test-tas-e2e-extended`, `test-e2e-certmanager`, `test-e2e-dra`, `test-e2e-dra-counter`. If the user wants MultiKueue (manager + worker clusters), use the `kueue-e2e-cluster-multikueue` skill instead.

## Step 1 - Confirm platform and build the Kueue image

Ask/confirm architecture. On Apple Silicon (arm64) `PLATFORM` must be set explicitly; on amd64 it can be omitted.

```sh
# amd64
make kind-image-build

# arm64
PLATFORM=linux/arm64 make kind-image-build
```

Skip this step only if the user explicitly wants to use a released/staging image via `IMAGE_TAG` (see Step 4).

## Step 2 - Pick the target suite

Map the user's request to a Makefile target:

| Suite | Target |
|---|---|
| Baseline (default) | `test-e2e-baseline` |
| Extended (job-framework integrations) | `test-e2e-extended` |
| Sequential baseline | `test-e2e-sequential-baseline` |
| Sequential extended | `test-e2e-sequential-extended` |
| Topology-Aware Scheduling baseline | `test-tas-e2e-baseline` |
| Topology-Aware Scheduling extended | `test-tas-e2e-extended` |
| cert-manager | `test-e2e-certmanager` |
| Dynamic Resource Allocation | `test-e2e-dra` / `test-e2e-dra-counter` |

Default to `test-e2e-baseline` if the user has no preference — it is the fastest to bring up and covers the core scheduling flows.

## Step 3 - Bring up the cluster in dev mode

Set `E2E_MODE=dev` so the cluster is created (or reused if it already exists), Kueue is deployed, and the cluster is **kept alive** after the run instead of being torn down:

```sh
E2E_MODE=dev make kind-image-build <target>
# e.g.
E2E_MODE=dev make kind-image-build test-e2e-baseline
```

If the user only wants the environment ready (no test run), stay on `E2E_MODE=dev` and pass a non-matching `GINKGO_ARGS` label filter so ginkgo runs zero specs and exits immediately, while the cluster is still brought up and Kueue deployed:

```sh
E2E_MODE=dev GINKGO_ARGS="--label-filter=env-only-no-match" make kind-image-build test-e2e-baseline
```

Since `E2E_MODE=dev` keeps the cluster alive regardless of whether any specs ran, this avoids the legacy interactive `E2E_RUN_ONLY_ENV=true` mode (prompts `Do you want to cleanup? [Y/n]`) entirely — that flag is considered legacy/likely to be removed, so prefer this pattern.

Useful modifiers (combine with `E2E_MODE=dev`):

- `GINKGO_ARGS="--label-filter=feature:jobset"` — filter which specs run.
- `GINKGO_ARGS="--focus='some spec name'"` — focus on a specific spec.
- `E2E_SKIP_REINSTALL=true` — skip reinstalling Kueue if the deployment already exists and the image is unchanged (dev mode only, for fast reruns).
- `E2E_SKIP_IMAGE_RELOAD=true` — skip re-pulling/reloading dependency images already cached locally / present on kind nodes (dev mode only, speeds up repeat runs).
- `E2E_ENFORCE_OPERATOR_UPDATE=true` — force reinstalling external operators (JobSet, AppWrapper, KubeRay, etc.) even when reusing a cluster.
- `E2E_K8S_FULL_VERSION=1.35.5` — pick a specific Kubernetes version (see `E2E_K8S_VERSIONS` in `Makefile-test.mk` for the current supported list, e.g. `1.34.8 1.35.5 1.36.1`).
- `KIND_CLUSTER_NAME=<name>` — use a non-default kind cluster name (default is `kind`), useful for running multiple clusters side by side.

## Step 4 - Optional: use a released or staging image instead of building

Skip `make kind-image-build` and pass `IMAGE_TAG`:

```sh
# Released version
E2E_MODE=dev IMAGE_TAG=registry.k8s.io/kueue/kueue:v0.16.0 make test-e2e-baseline

# Staging image (e.g. built from a PR or nightly)
E2E_MODE=dev IMAGE_TAG=us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:main make test-e2e-baseline
```

To reproduce a specific released version end-to-end (matching CRDs/manifests), `git checkout <tag>` first, since config is committed per release.

## Step 5 - Verify the cluster is ready

```sh
kubectl get nodes
kubectl -n kueue-system get pods
kubectl -n kueue-system get deployment kueue-controller-manager
```

The e2e script itself waits for the Kueue deployment to become available and for its webhook endpoints to be ready before running tests, so if the make target completed, the cluster should already be healthy.

## Step 6 - Iterate

With `E2E_MODE=dev`, rerunning the same command reuses the existing cluster, rebuilds/reloads only what changed, and reruns tests — this is the normal iteration loop while developing:

```sh
E2E_MODE=dev make kind-image-build test-e2e-baseline
```

To loop a suite until it fails (flake hunting):

```sh
E2E_MODE=dev GINKGO_ARGS="--until-it-fails" make kind-image-build test-e2e-baseline
```

## Step 7 - Clean up when done

```sh
kind delete clusters kind
# or, if a custom KIND_CLUSTER_NAME was used:
kind delete clusters <name>
```

## Troubleshooting pointers

- Cluster config lives in `hack/testing/kind-cluster.yaml` (or `hack/testing/kind-cluster-tas.yaml` for TAS suites).
- All lifecycle logic (create/reuse/teardown, image loading, operator install/skip logic) lives in `hack/testing/e2e-common.sh`; the suite driver is `hack/testing/e2e-test.sh`.
- If cluster creation fails, check `$ARTIFACTS/<cluster>-create.log` (kind create output) and `$ARTIFACTS/<cluster>-nodes.log` / `$ARTIFACTS/<cluster>-system-pods.log`.
- Full reference docs: `site/content/en/community/contribution_guidelines/testing.md`.
