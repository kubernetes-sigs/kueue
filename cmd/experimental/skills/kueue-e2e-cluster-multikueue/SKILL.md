---
name: kueue-e2e-cluster-multikueue
description: Create (or reuse) the manager + worker kind clusters for Kueue MultiKueue e2e development. Use when a user asks to spin up MultiKueue e2e clusters, get a local multi-cluster environment ready, or debug/iterate against a MultiKueue e2e suite (baseline, extended, sequential, DRA).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

You are an expert in Kueue's MultiKueue e2e testing infrastructure (`hack/testing/e2e-multikueue-test.sh`, `hack/testing/e2e-common.sh`, `hack/testing/multikueue/*.kind.yaml`, `Makefile-test.mk`).

## Goal

Stand up three kind clusters — one manager, two workers — with Kueue deployed on all three and MultiKueue wired up, ready for e2e tests or manual poking, and leave them running for fast iteration.

This skill is for **MultiKueue** suites (manager + worker clusters): `test-multikueue-e2e-baseline`, `test-multikueue-e2e-extended` (+ shards), `test-multikueue-e2e-sequential`, `test-e2e-multikueue-dra`. If the user just wants a single cluster (no MultiKueue), use the `kueue-e2e-cluster-singlecluster` skill instead.

## Step 1 - Confirm platform and build the Kueue image

```sh
# amd64
make kind-image-build

# arm64 (Apple Silicon)
PLATFORM=linux/arm64 make kind-image-build
```

Skip this only if using a released/staging image via `IMAGE_TAG` (see Step 4).

## Step 2 - Pick the target suite

| Suite | Target |
|---|---|
| Baseline (default) | `test-multikueue-e2e-baseline` |
| Extended | `test-multikueue-e2e-extended` |
| Extended shard 0 / 1 | `test-multikueue-e2e-extended-shard-0` / `-shard-1` |
| Sequential | `test-multikueue-e2e-sequential` |
| Dynamic Resource Allocation | `test-e2e-multikueue-dra` |

Default to `test-multikueue-e2e-baseline` if the user has no preference.

## Step 3 - Bring up the clusters in dev mode

Set `E2E_MODE=dev` so all three clusters are created (or reused), Kueue is deployed on each, and everything is **kept alive** after the run:

```sh
E2E_MODE=dev make kind-image-build <target>
# e.g.
E2E_MODE=dev make kind-image-build test-multikueue-e2e-baseline
```

Cluster creation for manager/worker1/worker2 runs in parallel; image loading is also parallelized across the three clusters. Kueue deployment (`kueue_deploy`) runs sequentially — manager first, then worker1, then worker2.

If the user only wants the environment ready without running tests, stay on `E2E_MODE=dev` and pass a non-matching `GINKGO_ARGS` label filter so ginkgo runs zero specs (and thus exits immediately without prompting) while the clusters are still brought up and Kueue deployed on all three:

```sh
E2E_MODE=dev GINKGO_ARGS="--label-filter=env-only-no-match" make kind-image-build test-multikueue-e2e-baseline
```

Since `E2E_MODE=dev` keeps clusters alive regardless of whether any specs ran, this avoids the interactive `E2E_RUN_ONLY_ENV=true` prompt entirely (that flag is considered legacy/likely to be removed — prefer this pattern). Tests can then be run manually, e.g.:

```sh
# With default KIND_CLUSTER_NAME=kind (clusters: kind-manager, kind-worker1, kind-worker2):
./bin/ginkgo --json-report ./ginkgo.report -focus "MultiKueue when Creating a multikueue admission check Should run a jobSet on worker if admitted" -r

# With custom KIND_CLUSTER_NAME=<name>:
MANAGER_KIND_CLUSTER_NAME=<name>-manager \
WORKER1_KIND_CLUSTER_NAME=<name>-worker1 \
WORKER2_KIND_CLUSTER_NAME=<name>-worker2 \
./bin/ginkgo --json-report ./ginkgo.report -focus "MultiKueue when Creating a multikueue admission check Should run a jobSet on worker if admitted" -r
```

Useful modifiers (combine with `E2E_MODE=dev`):

- `GINKGO_ARGS="--label-filter=feature:jobset"` — filter specs.
- `E2E_SKIP_REINSTALL=true` — skip reinstalling Kueue on clusters where the deployment already exists (dev mode only).
- `E2E_SKIP_IMAGE_RELOAD=true` — skip re-pulling/reloading dependency images already cached (dev mode only).
- `E2E_ENFORCE_OPERATOR_UPDATE=true` — force reinstalling external operators even when reusing clusters.
- `KIND_CLUSTER_NAME=<name>` — base name; actual clusters become `<name>-manager`, `<name>-worker1`, `<name>-worker2` (default base is `kind`).

## Step 4 - Optional: use a released or staging image instead of building

```sh
E2E_MODE=dev IMAGE_TAG=registry.k8s.io/kueue/kueue:v0.16.0 make test-multikueue-e2e-baseline
E2E_MODE=dev IMAGE_TAG=us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:main make test-multikueue-e2e-baseline
```

## Step 5 - Verify the clusters and contexts

The script exports each cluster's kubeconfig into the default kubeconfig with contexts `kind-<name>-manager`, `kind-<name>-worker1`, `kind-<name>-worker2`:

```sh
kubectl config get-contexts
kubectl --context kind-kind-manager -n kueue-system get pods
kubectl --context kind-kind-worker1 -n kueue-system get pods
kubectl --context kind-kind-worker2 -n kueue-system get pods
```

Each cluster's Kueue deployment must be available with ready webhook endpoints before the script proceeds — if the make target completed, all three should be healthy.

## Step 6 - Iterate

Rerun the same command to reuse the clusters, reload only what changed, and rerun tests:

```sh
E2E_MODE=dev make kind-image-build test-multikueue-e2e-baseline
```

## Step 7 - Clean up when done

```sh
kind delete clusters kind-manager kind-worker1 kind-worker2
# or, with a custom KIND_CLUSTER_NAME=<name>:
kind delete clusters <name>-manager <name>-worker1 <name>-worker2
```

## Troubleshooting pointers

- Manager kind config: `hack/testing/multikueue/manager-cluster.kind.yaml`; worker config (shared by both workers): `hack/testing/multikueue/worker-cluster.kind.yaml`.
- All lifecycle logic lives in `hack/testing/e2e-common.sh`; the MultiKueue-specific driver (parallel cluster creation, per-cluster kubeconfigs, `kueue_deploy` fan-out) is `hack/testing/e2e-multikueue-test.sh`.
- For k8s 1.31 node images, the manager's kind config is patched to enable the `JobManagedBy` feature gate — see `startup()` in `e2e-multikueue-test.sh`.
- Kubeconfigs are written to `$ARTIFACTS/kubeconfig-<cluster-name>`; per-cluster creation logs to `$ARTIFACTS/<cluster>-create.log`.
- Full reference docs, including the dev-environment walkthrough: `site/content/en/docs/tasks/dev/setup_multikueue_development_environment.md` and `site/content/en/community/contribution_guidelines/testing.md`.
