# Centralized-TAS spike (non-production)

This is a **feasibility spike**, not production code. It proves that a MultiKueue
manager can be globally authoritative for **node-level Topology-Aware Scheduling
(TAS)** across all worker clusters, and that workers can execute the manager's
placement without making any scheduling decision of their own.

The whole spike is inert unless the manager process is started with the
environment variable `KUEUE_CENTRALIZED_TAS_SPIKE=true`. There is no feature
gate and no API change.

## What it demonstrates

1. **Global inventory.** The manager watches every worker's `Node`s and `Pod`s
   through the existing MultiKueue remote client and feeds them into the *same*
   scheduler TAS cache the single-cluster TAS controllers use. Each worker's
   nodes are stamped with `kueue.x-k8s.io/multikueue-cluster=<clusterName>` so
   the manager sees the fleet as one topology with the cluster as the top level.
2. **Manager computes placement.** With the spike on, the manager stops delaying
   TAS for MultiKueue workloads and computes a full node-level assignment using
   the ordinary TAS flavor assigner and cache — reusing all existing TAS logic.
3. **Single-cluster pin + verbatim execution.** The top (cluster) level of the
   assignment selects exactly one worker. The manager projects the assignment
   down to a host-exact placement, stamps it onto the mirrored worker workload's
   `status.admission`, and marks it admitted. The worker performs no scheduling;
   its topology ungater applies the manager's node selectors verbatim.

## Code reuse

The design intentionally reuses existing code rather than copy-pasting:

- Remote inventory ingest calls the same `tasCache` mutators (`SyncNode`,
  `DeleteNodeByName`, `Update`, `DeletePodByKey`) and the shared
  `utiltas.BelongsToNonTASCache` predicate used by the single-cluster
  controllers, over the existing `AddCacheEventHandler` remote-cache path.
- The assignment projection reuses `utiltas.InternalFrom` / `utiltas.V1Beta2From`.
- The spike switch and shared label live in `pkg/util/centralizedtas`.

## Running locally on kind

`MultiKueue` and `TopologyAwareScheduling` are Beta/default-on, so the only
deploy-time change is the `KUEUE_CENTRALIZED_TAS_SPIKE=true` env var on the
manager. A ready-made kustomize overlay does exactly that:

    test/e2e/config/multikueue/centralizedtas   # = default Kueue + spike env var

The repository already ships a kind-based 3-cluster harness
(`hack/testing/e2e-multikueue-test.sh`) that stands up `<name>-manager`,
`<name>-worker1`, `<name>-worker2`, builds/loads the Kueue image, and deploys
Kueue to each. Point it at the spike overlay by exporting
`E2E_CONFIG_FOLDER=multikueue/centralizedtas` before `cluster_kueue_deploy`.

Manual outline once the three clusters are up and Kueue (with the overlay) is
deployed:

```sh
# On the manager: create kubeconfig secrets worker1-secret / worker2-secret
# pointing at the worker clusters (see the standard "Setup MultiKueue" task;
# the address must be reachable from inside the manager cluster).

kubectl --context kind-<name>-manager  apply -f manager.yaml
kubectl --context kind-<name>-worker1  apply -f worker.yaml
kubectl --context kind-<name>-worker2  apply -f worker.yaml

# Submit the workload to the manager.
kubectl --context kind-<name>-manager  apply -f job.yaml
```

Requires a running Docker/container runtime (e.g. `colima start`) for kind.

### Ginkgo e2e suite

```sh
make test-multikueue-e2e-centralizedtas

# or a single case:
GINKGO_ARGS="--focus='should route around a worker'" make test-multikueue-e2e-centralizedtas
```

Tests live in `test/e2e/multikueue/centralizedtas/` and require the
`multikueue/centralizedtas` kustomize overlay (spike env var + `batch/job`-only
integrations + fair-sharing config).

| Test | What it proves |
|------|----------------|
| Manager topology levels before dispatch | Manager assigns `[cluster, hostname]` on admission; no `DelayedTopologyRequest`; worker gets host-exact `nodeSelector`s |
| Non-TAS pod on worker tracked globally | A hog pod on worker1's TAS node is visible to the manager; a 1-CPU job is routed to worker2 instead |
| Inflated central quota blocked by TAS | Manager CQ quota of 1000 CPU does not admit a 12-CPU job when the physical fleet has far less |
| Fair sharing across the global pool | team-a borrows (`WeightedShare > 100`); as jobs finish on both workers, borrowing drops |
| Single-cluster gang invariant | All pods in a gang share one cluster-level topology value; the other worker stays empty |

Further cases worth adding later: cross-cluster topology-correct preemption,
worker-partition requeue after confirmed delete, and multi-workload primary-first
cluster pinning under centralized placement.

### Expected result

- The manager `Workload` gets `QuotaReserved` in `central-cq` with a
  `TopologyAssignment` whose top level values are worker cluster names.
- Exactly one worker receives the mirrored workload, already `Admitted`, with a
  host-exact `TopologyAssignment` (`levels: [kubernetes.io/hostname]`).
- The pods on that worker land on exactly the nodes the manager chose.

## Known open validation risks

- Cross-cluster topology-correct **preemption** is not covered by the spike e2e
  suite yet.
- Worker-partition failover (confirmed-delete requeue) is not automated yet.
