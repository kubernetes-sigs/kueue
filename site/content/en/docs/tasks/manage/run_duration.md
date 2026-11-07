---
title: "Limit each admission's run duration"
description: >
  Rotate restartable workloads through a shared ClusterQueue without changing priorities.
weight: 45
---

A long-running Workload can occupy a shared queue's quota indefinitely, preventing
waiting Workloads with the same priority from running.
With the alpha `WorkloadRunDuration` feature gate enabled, you can configure a
per-admission time limit that evicts, drains, and requeues a restartable Workload
with a fresh FIFO turn.

This feature does not change priorities or guarantee fairness between different
priorities.
Under ordinary priority/FIFO ordering, waiting Workloads of the same priority
precede the requeued Workload.
`BestEffortFIFO` can still skip Workloads that do not fit.
Admission Fair Sharing, priority boosting, sticky-head behavior, and preemption
continue to use their existing policies; a duration limit does not override them.

## Configure a ClusterQueue default

Enable `WorkloadRunDuration=true` in the kueue-controller-manager feature gates,
then add the limit to the ClusterQueue:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: shared
spec:
  maximumRunDurationSeconds: 3600
  # Keep your existing resourceGroups, namespaceSelector, and other settings.
```

The default is captured in `Workload.status.admission.runDurationPolicy` when
quota is reserved.
Changes to the ClusterQueue affect subsequent reservations, not current ones.
An unset default means no duration limit.
The timer starts when the Workload becomes `Admitted`, not while admission checks
or topology assignments are pending.
Once started, it continues for the same reservation during readiness or topology
recovery; those transitions do not grant a fresh run.
The persisted `status.runDuration.admittedAt` and `deadline` allow Kueue to resume
the timer after a controller restart.

## Override a Workload

Set `Workload.spec.maximumRunDurationSeconds` directly to override the captured
default with a longer or shorter positive duration.
There is no additional Job label, annotation, UI, or adjustment protocol.
Kubernetes RBAC controls access: any principal permitted to write Workloads can
change the field.
It is not independently restricted to administrators.

For example, change an admitted Workload's limit to two hours:

```shell
kubectl patch workload example -n team-a --type=merge \
  -p '{"spec":{"maximumRunDurationSeconds":7200}}'
```

The limit is the total duration from the original admission start, not a new
duration starting at the edit.
Shortening the limit below time already elapsed requests eviction on the next
reconciliation.
Removing the override restores this admission's captured ClusterQueue default,
including unlimited, rather than a newer ClusterQueue setting.
Edits cannot cancel an eviction or deactivation already requested.
Reservations made before the feature was enabled, or by unsupported integrations,
cannot acquire a live override; configure it before their next admission.

## Drain and readmission

Expiry sets `Evicted=True` with reason `RunDurationExceeded`.
Kueue requests suspension through the job integration and retains quota until the
integration confirms that old Pods have drained.
It then requeues the same active, unfinished Workload and records a fresh queue
turn in `status.requeueState.queueOrderTimestamp`.
Each subsequent admission has a fresh timer and captures the current defaults.
Suspension does not checkpoint application state: jobs must tolerate restarts and
provide their own checkpointing when needed.

The initial in-tree implementation supports Kubernetes batch Jobs.
Integrations registered with Kueue can opt in through the optional
`jobframework.JobWithRunDuration` interface.
Its drain method must account for terminating and old-revision Pods and its
Stop/Run lifecycle must retain and restart the same job and Workload.
Standalone Pods and integrations without this capability are not eligible for
timed admission; Kueue reports the unsupported configuration instead of deleting
them on expiry.
Duration limits are initially unsupported with `ConcurrentAdmission` and
`ElasticJobsViaWorkloadSlices`, and for MultiKueue manager admissions.
An ordinary local Job drain cannot establish that remote worker Pods have stopped.
Untimed MultiKueue manager admissions and supported local admissions on worker
clusters are unaffected.
An incompatible Workload override or ClusterQueue default blocks timed admission
with an observable explanation.
Remove the duration configuration to restore ordinary untimed admission.

The existing `maximumExecutionTimeSeconds` limit remains cumulative across
admissions and deactivates the Workload rather than rotating it.
If both limits expire together, cumulative execution-time deactivation takes
precedence.
Queue stop policies, admission-check eviction, and other eviction reasons retain
their existing behavior.
Disabling `WorkloadRunDuration` suppresses new duration expirations, but does not
cancel drains already requested or prevent their quota release and requeue.
