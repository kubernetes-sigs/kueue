# WAS Job Preemption Examples

These examples demonstrate Workload-Aware Scheduling (WAS) preemption
using plain `batch/v1` Jobs (as opposed to the upstream JobSet examples).

## Files

| File | Description |
|------|-------------|
| `low-priority.yaml` | PriorityClass with value `1` |
| `high-priority.yaml` | PriorityClass with value `100000` |
| `low-priority-job.yaml` | Workload + PodGroup + Job (low priority, 4 pods × 6 CPU) |
| `high-priority-job.yaml` | Workload + PodGroup + Job (high priority, 4 pods × 6 CPU) |

## Usage

```bash
# 1. Create priority classes (one-time)
kubectl apply -f low-priority.yaml -f high-priority.yaml

# 2. Deploy low-priority job (fills the cluster)
kubectl apply -f low-priority-job.yaml

# 3. Wait for all LP pods to be Running
kubectl get pods -l job-name=lp-job -w

# 4. Deploy high-priority job (triggers preemption)
kubectl apply -f high-priority-job.yaml

# 5. Watch the preemption happen
kubectl get pods -o wide -w
kubectl get events --sort-by=.lastTimestamp

# Cleanup
kubectl delete -f high-priority-job.yaml -f low-priority-job.yaml --ignore-not-found
```

## Findings

See the "What's Missing" section below.

### What Works
- **Workload-aware preemption is triggered correctly**: The scheduler preempts
  the entire low-priority PodGroup as a unit (`disruptionMode: all`), not
  individual pods. Events show:
  ```
  Normal  Preempted  pod/lp-job-xxx  Preempted by podgroup <uuid> on node cluster
  ```
- **Gang scheduling works**: HP pods wait until all 4 can be scheduled together.
  Events show `minCount (4) cannot be satisfied` while waiting.
- **Pod-level preemption is correctly blocked for WAS**: LP pods show
  `preemption: not eligible due to workload aware preemption enabled`.

### What's Missing / Issues for Kueue Integration

1. **No workload-level lifecycle management**: After LP pods are preempted, the
   Job controller recreates them (it sees `failed: 4` < `backoffLimit: 10`).
   The new LP pods go to `Pending` and are stuck because the HP job occupies the
   cluster. There's nothing to tell the Job controller "your workload was
   preempted, stop recreating pods until resources are available again."

2. **PodGroup status doesn't reflect preemption**: After all LP pods are killed,
   the PodGroup still shows `PodGroupInitiallyScheduled: True`. There's no
   `Preempted` condition or status update. The Workload object has no status at
   all.

3. **No re-queuing / back-off at the workload level**: The preempted LP workload
   doesn't go back into a "waiting" state. The PodGroup stays in `Scheduled`
   status. Kueue would need to either:
   - Watch for preemption events and suspend the Job (similar to how Kueue
     suspends Jobs today), OR
   - Have the WAS scheduler set a condition on the Workload/PodGroup that
     Kueue can react to.

4. **Manual Workload/PodGroup creation**: Users must manually create the
   `Workload` and `PodGroup` objects and wire them to the Job via
   `schedulingGroup.podGroupName`. There's no controller that does this
   automatically for plain Jobs (unlike JobSet which presumably has integration).

5. **No ownership/GC relationship**: The Workload and PodGroup are not owned by
   the Job. Deleting the Job does NOT garbage-collect the Workload/PodGroup
   (we had to delete all three explicitly).
