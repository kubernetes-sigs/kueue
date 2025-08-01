---
title: "MultiKueue"
date: 2024-11-11
weight: 8
description: >
  Multi-cluster job dispatching with Kueue.
---

{{< feature-state state="beta" for_version="v0.9" >}}

{{% alert title="Note" color="primary" %}}
`MultiKueue` is currently a beta feature and is enabled by default.

You can disable it by editing the `MultiKueue` feature gate. Refer to the
[Installation guide](/docs/installation/#change-the-feature-gates-configuration)
for instructions on configuring feature gates.
{{% /alert %}}

A MultiKueue setup is composed of a manager cluster and at least one worker cluster.

## Cluster Roles

### Manager Cluster

The manager cluster is responsible for:

- Establishing and maintaining connections with worker clusters.
- Creating and monitoring remote objects (Workloads or Jobs) while keeping the local ones in sync.

The **MultiKueue Admission Check Controller** runs in the manager cluster.
It maintains the `Active` status of AdmissionChecks managed by MultiKueue.

The quota set for the flavors of a ClusterQueue determines how many jobs are eligible for dispatching
at a given time. Ideally, the quota in the manager cluster should equal the total quota available in all worker clusters:

- If the manager’s quota is **significantly lower**, worker clusters may remain underutilized.
- If the manager’s quota is **significantly higher**, it may dispatch and monitor workloads
  that are unlikely to be admitted in the worker clusters.

### Worker Cluster

The worker cluster acts like a standalone Kueue cluster.
The **MultiKueue Admission Check Controller**, running in the manager cluster,
creates and deletes Workloads and Jobs in the worker clusters as needed.

## Job Flow

To enable multi-cluster dispatching, you need to assign a Job to a ClusterQueue configured with a MultiKueue `AdmissionCheck`.

The dispatching flow works as follows:

1. When the Job's Workload obtains a `QuotaReservation` in the manager cluster,
   the dispatcher determines the worker clusters where the Workload should be created.
   Depending on the configured dispatching algorithm (e.g., AllAtOnce, Incremental, or a custom approach),
   the Workload may be created in all configured worker clusters or in a subset of them.
2. When a worker cluster admits one of these remote Workloads:
   - The manager deletes the Workloads from the other clusters.
   - The manager creates a copy of the Job in the selected worker cluster and labels it
     with `kueue.x-k8s.io/prebuilt-workload-name` to link it to the admitted Workload.
3. The manager monitors the remote Workload and Job, and synchronizes their status with
   the corresponding local objects.
4. Once the remote Workload is marked `Finished`:
   - The manager performs a final status sync.
   - It then deletes the corresponding objects from the worker cluster.

## Workload Dispatching

{{% alert title="Note" color="primary" %}}
The MultiKueue Dispatcher mechanism is available since Kueue v0.13.
{{% /alert %}}
Provides a flexible way to control how workloads are distributed across worker clusters
by allowing users to choose between built-in dispatching algorithms or implement custom ones.
This mechanism ensures efficient workload placement while minimizing resource contention and preemptions across clusters.

The `status.nominatedClusterNames` field lists the worker clusters currently being considered for scheduling the Workload,
as determined by the dispatching algorithm, and is updated while the Workload is pending admission.

The `status.clusterName` field specifies the worker cluster where the Workload has been successfully admitted.
Once the field is set, it becomes immutable and the `status.nominatedClusterNames` field is reset,
and it is no longer possible to set it. 

This ensures that the Workload's cluster assignment is finalized and prevents further nomination of clusters.

### AllAtOnce (Default Mode):
In this mode, the Workload is copied to all available worker clusters as soon as it obtains a QuotaReservation in the manager cluster.
This approach ensures the fastest possible admission by allowing all clusters to compete for the Workload simultaneously.

### Incremental:
This mode introduces a gradual dispatching strategy where clusters are nominated in rounds.
Initially, all worker clusters are sorted in dictionary order, and a subset of up to 3 clusters is selected from the sorted list.
The Workload is copied only to these nominated clusters.
If none of the nominated clusters admit the Workload within a fixed duration (5 minutes),
an additional up to 3 clusters are incrementally added in subsequent rounds, following the same dictionary order.

### External (Custom implementation):
In this mode, the selection of worker clusters is delegated to an external controller.
The external controller is responsible for setting the `.status.nominatedClusterNames` field in the Workload to specify the clusters where it should be copied.

The MultiKueue Workload Controller synchronizes the Workload with the nominated clusters.

Known Limitation:
{{% alert title="Warning" color="primary" %}}
For the external controller to patch the `.status.nominatedClusterNames` field, it must use the kueue-admission field manager.
This is required because the kueue-admission field manager is responsible for managing updates to the `.status.nominatedClusterNames` field.
Without this, the Kueue is not able to admit the MultiKueue workloads.
{{% /alert %}}

## Supported Job Types

MultiKueue supports a wide variety of workloads. You can learn how to:

- [Dispatch a Kueue managed Deployment](docs/tasks/run/multikueue/deployment).
- [Dispatch a Kueue managed batch/Job](docs/tasks/run/multikueue/job).
- [Dispatch a Kueue managed JobSet](docs/tasks/run/multikueue/jobset).
- [Dispatch a Kueue managed Kubeflow Jobs](docs/tasks/run/multikueue/kubeflow).
- [Dispatch a Kueue managed KubeRay workloads](docs/tasks/run/multikueue/kuberay).
- [Dispatch a Kueue managed MPIJob](docs/tasks/run/multikueue/mpijob).
- [Dispatch a Kueue managed AppWrapper](docs/tasks/run/multikueue/appwrapper).
- [Dispatch a Kueue managed plain Pod](docs/tasks/run/multikueue/plain_pods).

## Submitting Jobs

In a [properly configured MultiKueue environment](/docs/tasks/manage/setup_multikueue),
you can submit any supported Job to the **manager cluster**, targeting a ClusterQueue configured for MultiKueue.

Kueue handles delegation to the appropriate worker cluster without requiring any additional changes to your job specification.

## What’s Next?

- [Set up a MultiKueue environment](/docs/tasks/manage/setup_multikueue/)
- [Run Jobs in a MultiKueue environment](/docs/tasks/run/multikueue)
