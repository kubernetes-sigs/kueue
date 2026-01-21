This is a temporary document containing information about
[PR 8341 - Make RayJob top level for autoscaling and support MultiKueue](https://github.com/kubernetes-sigs/kueue/pull/8341).

This file will be deleted after the PR is reviewed.

## Code Flow for MultiKueue Scale-Up/Scale-Down

Assume we have a RayJob running in MultiKueue, with worker group `small-group` having 1 worker initially,
then scales up to 5 workers. After running with 5 workers, it scales down to 3 workers.

### Scale-Up Flow (e.g., 1 → 5 workers)

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              WORKER CLUSTER                                      │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  1. Ray autoscaler triggers: need 5 workers (currently 1)                       │
│                                                                                  │
│  2. RayJob controller updates RayCluster spec (replicas: 1 → 5)                 │
│                                                                                  │
│  3. Kueue Job Reconciler runs:                                                  │
│     └── ReconcileGenericJob()                                                   │
│         └── ensureOneWorkload()                                                 │
│             └── [prebuilt path] ensurePrebuiltWorkloadInSync()                  │
│                 ├── EquivalentToWorkload() returns FALSE (5 != 1)               │
│                 ├── workloadSliceEnabled(job) = TRUE                            │
│                 ├── isMultiKueueWorkerCluster(wl) = TRUE                        │
│                 ├── wlPodSetsCounts.HasFewerReplicasThan(jobPodSetsCounts)      │
│                 │   └── 1 < 5 = TRUE → Scale-up detected!                       │
│                 └── reportScaleRequest(wl, {small-group: 5})                    │
│                     └── Sets annotation: scale-request='{"small-group":5}'      │
│                                                                                  │
│  4. Returns "in-sync" → Job continues running with 1 worker                     │
│                                                                                  │
└──────────────────────────────────────┬──────────────────────────────────────────┘
                                       │
                                       │ Workload has scale-request annotation
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              MULTIKUEUE                                          │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  5. wlReconciler.reconcileGroup() detects annotation on worker workload         │
│     ├── Calls propagateScaleRequest() to copy annotation to manager workload   │
│     └── Calls clearScaleRequest() to remove annotation from worker workload    │
│                                                                                  │
└──────────────────────────────────────┬──────────────────────────────────────────┘
                                       │
                                       │ Manager workload now has scale-request annotation
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              MANAGER CLUSTER                                     │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  6. Kueue Job Reconciler runs:                                                  │
│     └── ReconcileGenericJob()                                                   │
│         └── ensureOneWorkload()                                                 │
│             └── [workload slicing path] processScaleRequest()                   │
│                 ├── Finds existing workload with scale-request annotation       │
│                 ├── Parses requested counts: {small-group: 5}                   │
│                 ├── existingCounts = {small-group: 1}                           │
│                 ├── ScaledDown(existing, requested) = FALSE → Scale-up!         │
│                 ├── constructWorkload() → new workload with count=5             │
│                 ├── Sets WorkloadSliceReplacementFor annotation                 │
│                 ├── Creates new workload slice                                  │
│                 └── Clears scale-request annotation from old workload           │
│                                                                                  │
│  7. Scheduler admits new workload slice (quota reservation)                     │
│                                                                                  │
│  8. Old workload slice finished with reason "WorkloadSliceReplaced"             │
│                                                                                  │
└──────────────────────────────────────┬──────────────────────────────────────────┘
                                       │
                                       │ New workload synced to worker
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              MULTIKUEUE                                          │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  9. Syncs new workload to worker cluster                                        │
│  10. SyncJob() updates worker job's prebuilt-workload-name label to new workload│
│      └── CRITICAL: Without this, worker job would reference old workload        │
│                                                                                  │
└──────────────────────────────────────┬──────────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              WORKER CLUSTER                                      │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  11. Job now matches new workload (both have count=5)                           │
│  12. Pods are ungated and scheduled                                             │
│  13. Job runs with 5 workers                                                    │
│                                                                                  │
└─────────────────────────────────────────────────────────────────────────────────┘
```

### Scale-Down Flow (e.g., 5 → 3 workers)

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              WORKER CLUSTER                                      │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  1. Ray autoscaler triggers: reduce to 3 workers (currently 5)                  │
│                                                                                  │
│  2. RayJob controller updates RayCluster spec (replicas: 5 → 3)                 │
│                                                                                  │
│  3. Kueue Job Reconciler runs:                                                  │
│     └── ensurePrebuiltWorkloadInSync()                                          │
│         ├── EquivalentToWorkload() returns FALSE (3 != 5)                       │
│         ├── Scale-up check: 5 < 3 = FALSE                                       │
│         ├── Scale-down check: jobPodSetsCounts.HasFewerReplicasThan(wl)         │
│         │   └── 3 < 5 = TRUE → Scale-down detected!                             │
│         └── reportScaleRequest(wl, {small-group: 3})                            │
│                                                                                  │
│  4. Returns "in-sync" → Job continues running                                   │
│                                                                                  │
└──────────────────────────────────────┬──────────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              MULTIKUEUE → MANAGER CLUSTER                        │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  5. MultiKueue propagates annotation to manager workload                        │
│                                                                                  │
│  6. Manager's processScaleRequest():                                            │
│     ├── Parses requested counts: {small-group: 3}                               │
│     ├── existingCounts = {small-group: 5}                                       │
│     ├── ScaledDown(existing, requested) = TRUE → Scale-down!                    │
│     ├── ApplyPodSetCounts(existingWl, {small-group: 3})                         │
│     ├── Updates existing workload in place (NO new slice)                       │
│     └── Clears scale-request annotation                                         │
│                                                                                  │
└──────────────────────────────────────┬──────────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              MULTIKUEUE → WORKER CLUSTER                         │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  7. MultiKueue syncs updated workload to worker                                 │
│                                                                                  │
│  8. Job and workload now both have count=3                                      │
│                                                                                  │
│  9. Job continues with 3 workers (excess pods terminated)                       │
│                                                                                  │
└─────────────────────────────────────────────────────────────────────────────────┘
```
