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
   a copy of that Workload is created in all configured worker clusters.
2. When a worker cluster admits one of these remote Workloads:
   - The manager deletes the Workloads from the other clusters.
   - The manager creates a copy of the Job in the selected worker cluster and labels it
     with `kueue.x-k8s.io/prebuilt-workload-name` to link it to the admitted Workload.
3. The manager monitors the remote Workload and Job, and synchronizes their status with
   the corresponding local objects.
4. Once the remote Workload is marked `Finished`:
   - The manager performs a final status sync.
   - It then deletes the corresponding objects from the worker cluster.

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
