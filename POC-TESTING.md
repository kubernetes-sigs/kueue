# POC manual testing — RayService upgrade quota gate

Branch: `poc/rayservice-upgrade-quota-gate`
Issue: kubernetes-sigs/kueue#11102

## Prerequisites

- kind cluster running (`kubectl config current-context` → `kind-kind`).
- KubeRay operator deployed with PR ray-project/kuberay#4841 applied. The POC was verified against the image `kuberay/operator:test-suspend`.
- Local kuberay checkout at `/Users/kaihsunchen/Desktop/kuberay` (the vendored `rayservice_types.go` mirrors it).

## Build and deploy Kueue with this branch

```bash
# 1. install CRDs
make install

# 2. build and load image into kind
make kind-image-build
kind load docker-image us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:main

# 3. deploy
make deploy

# 4. pin imagePullPolicy and image tag so kind serves the freshly built copy
kubectl patch deployment -n kueue-system kueue-controller-manager \
  -p '{"spec":{"template":{"spec":{"containers":[{"name":"manager","imagePullPolicy":"IfNotPresent"}]}}}}'
kubectl set image -n kueue-system deployment/kueue-controller-manager \
  manager=us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:main

# 5. enable the elastic-jobs feature gate
kubectl patch deployment -n kueue-system kueue-controller-manager --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--feature-gates=ElasticJobsViaWorkloadSlices=true"}]'
kubectl wait -n kueue-system --for=condition=Available --timeout=120s deployment/kueue-controller-manager
```

When you re-build Kueue mid-test, the tag is reused, so kind keeps the old image under the version-numbered tag. Always re-run `kind load docker-image` and either `kubectl rollout restart` or `kubectl set image` to ensure the new digest is picked up — confirm by inspecting `imageID` on the running pod.

## Happy path — quota gates the pending RayCluster

ClusterQueue has room for exactly one head pod (1 CPU / 2 GiB). The upgrade should keep the active RayCluster running and leave the pending RayCluster suspended.

```bash
# queues
kubectl apply -f poc-queues.yaml

# RayService — already labelled with queue-name and annotated kueue.x-k8s.io/elastic-job: "true"
kubectl apply -f ray-service-sample.yaml

# wait for KubeRay to promote the first cluster to Active
until [ -n "$(kubectl get rayservice test-rayservice -o jsonpath='{.status.activeServiceStatus.rayClusterName}')" ]; do sleep 6; done
```

Pre-upgrade expectation:

```
NAME                    SUSPEND   VERSION   STATE
test-rayservice-XXXXX   false     2.48.0    ready
```

The RayService should also have `spec.rayClusterConfig.suspend: true` (persistent template gate) and `spec.suspend` empty/false (top-level released after admission).

Trigger a zero-downtime upgrade by bumping rayVersion:

```bash
kubectl patch rayservice test-rayservice --type=merge \
  -p '{"spec":{"rayClusterConfig":{"rayVersion":"2.46.0"}}}'
```

After ~10 s, inspect state:

```bash
kubectl get rayclusters -o custom-columns=NAME:.metadata.name,SUSPEND:.spec.suspend,VERSION:.spec.rayVersion,STATE:.status.state
kubectl get pods -l ray.io/cluster
kubectl get workloads --sort-by=.metadata.creationTimestamp
kubectl get clusterqueue cluster-queue -o jsonpath='{.status.flavorsUsage}{"\n"}'
```

Pass criteria:

| Field | Expected |
|---|---|
| Active RayCluster | `Suspend=false`, `STATE=ready`, head pod Running |
| Pending RayCluster | `Suspend=true`, `STATE=suspended`, no head pod |
| Workloads | Two workloads; older admitted (head count 1), newer Pending (head count 2) |
| ClusterQueue usage | `cpu: 1`, `memory: 2Gi` — only the active is reserved |

Release the gate by adding capacity:

```bash
kubectl patch clusterqueue cluster-queue --type=merge -p '{"spec":{"resourceGroups":[{"coveredResources":["cpu","memory"],"flavors":[{"name":"default","resources":[{"name":"cpu","nominalQuota":"2"},{"name":"memory","nominalQuota":"4Gi"}]}]}]}}'
```

The newer workload should be admitted, `unsuspendAdmittedChildren` patches the pending RayCluster's `Spec.Suspend=false`, KubeRay promotes it once Serve apps are ready, and the older slice is finished.

`ClusterQueue.status.admittedWorkloads` should settle to `1` after the transition. `EnsureWorkloadSlices`' case 2 path (`pkg/workloadslicing/workloadslicing.go:229-241`) finishes the old slice as `WorkloadFinishedReasonOutOfSync` as soon as the new slice is admitted as its replacement, and finished workloads are not counted. The count may briefly read `2` while both slices are alive.

Tear down:

```bash
kubectl delete rayservice test-rayservice
kubectl delete -f poc-queues.yaml
```

## Limitation 1 — heterogeneous PodSpec on the same group name

`PodSets` merges per-group counts across children and keeps the first child's template, so resource-changing upgrades on the same group name are mis-accounted. To reproduce:

```bash
# 1. enlarge quota so active can run with the bigger resource ask we'll set
kubectl apply -f poc-queues.yaml
kubectl patch clusterqueue cluster-queue --type=merge -p '{"spec":{"resourceGroups":[{"coveredResources":["cpu","memory"],"flavors":[{"name":"default","resources":[{"name":"cpu","nominalQuota":"4"},{"name":"memory","nominalQuota":"8Gi"}]}]}]}}'

# 2. apply the sample (head = 1 CPU / 2 GiB)
kubectl apply -f ray-service-sample.yaml
until [ -n "$(kubectl get rayservice test-rayservice -o jsonpath='{.status.activeServiceStatus.rayClusterName}')" ]; do sleep 6; done

# 3. trigger an upgrade that DOUBLES the head's resource request (2 CPU / 4 GiB)
kubectl patch rayservice test-rayservice --type=merge -p '{
  "spec":{"rayClusterConfig":{
    "rayVersion":"2.46.0",
    "headGroupSpec":{"rayStartParams":{},"template":{"spec":{"containers":[{"name":"ray-head","image":"rayproject/ray:2.46.0","resources":{"requests":{"cpu":"2","memory":"4Gi"},"limits":{"cpu":"2","memory":"4Gi"}}}]}}}
  }}
}'
```

Observation:

```bash
kubectl get rayclusters -o custom-columns=NAME:.metadata.name,VERSION:.spec.rayVersion
kubectl get workloads --sort-by=.metadata.creationTimestamp -o jsonpath='{range .items[*]}{.metadata.name}{" head-count="}{.spec.podSets[0].count}{" cpu="}{.spec.podSets[0].template.spec.containers[0].resources.requests.cpu}{"\n"}{end}'
kubectl get clusterqueue cluster-queue -o jsonpath='{.status.flavorsUsage}{"\n"}'
```

The new workload slice will show `head count=2` with `cpu=1` taken from the OLD active's template — not the actual mix of 1 CPU (old) + 2 CPU (new). Real demand is 3 CPU but the slice reserves 2 CPU. Once admitted, both head pods run on under-reserved quota. Conversely, downgrading resources over-reserves.

Workaround (POC-only): make resource-changing upgrades go through a manual two-step (scale active down, change spec while paused, re-admit) until the heterogeneous case is handled properly.

## Limitation 2 — user tampering with the persistent template gate

There's no webhook validation guarding `Spec.RayClusterSpec.Suspend`. A user can manually disable the gate, and then KubeRay creates the upgrade's pending cluster un-suspended, bypassing the quota gate.

```bash
# happy-path setup
kubectl apply -f poc-queues.yaml
kubectl apply -f ray-service-sample.yaml
until [ -n "$(kubectl get rayservice test-rayservice -o jsonpath='{.status.activeServiceStatus.rayClusterName}')" ]; do sleep 6; done

# disable the template gate manually
kubectl patch rayservice test-rayservice --type=merge \
  -p '{"spec":{"rayClusterConfig":{"suspend":false}}}'

# trigger an upgrade
kubectl patch rayservice test-rayservice --type=merge \
  -p '{"spec":{"rayClusterConfig":{"rayVersion":"2.46.0"}}}'

# inspect: the new pending RayCluster has Suspend=false at creation,
# its head pod starts running before Kueue admits the upgrade workload slice
kubectl get rayclusters -o custom-columns=NAME:.metadata.name,SUSPEND:.spec.suspend,STATE:.status.state
kubectl get pods -l ray.io/cluster
```

Expectation: the pending head pod is `Running` even though the upgrade workload slice is `Pending`. ClusterQueue usage stays at 1 CPU on Kueue's books while two head pods actually consume the node — quota over-commitment.

A proper webhook would reject this update (or re-patch the gate back to true). Until then, treat the template-level `suspend` as Kueue-owned.

## Limitation 3 — MultiKueue (not yet wired)

`pkg/controller/jobs/rayservice/rayservice_multikueue_adapter.go` hasn't been updated for the new suspend semantics, so management cluster → worker cluster sync won't propagate `Spec.Suspend` correctly and won't keep `RayClusterSpec.Suspend=true` on the worker side. Skip the MultiKueue happy path in manual testing for now.
