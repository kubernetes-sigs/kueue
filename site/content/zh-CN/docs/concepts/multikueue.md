---
title: "MultiKueue"
date: 2024-11-11
weight: 8
description: >
  Kueue multi cluster job dispatching.
---

{{< feature-state state="beta" for_version="v0.9" >}}

{{% alert title="Note" color="primary" %}}
`MultiKueue` is currently a beta feature and is enabled by default.

You can disable it by editing the `MultiKueue` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.
{{% /alert %}}


A MultiKueue setup is composed of a manager cluster and at least one worker cluster.

## Cluster Roles
### Manager Cluster

The manager's main responsibilities are:
- Establish and maintain the connection with the worker clusters.
- Create and monitor remote objects (workloads or jobs) while keeping the local ones in sync.

The MultiKueue Admission Check Controller runs in the manager cluster and will also maintain the `Active` status of the Admission Checks controlled by `multikueue`.

The quota set for the flavors of a ClusterQueue using MultiKueue controls how many jobs are subject for dispatching at a given point in time.
Ideally, the quota in the manager cluster should be equal to the total quotas in the worker clusters.
If significantly lower, the worker clusters will be under utilized.
If significantly higher, the manager will dispatch and monitor workloads in the worker clusters that don't have a chance to be admitted.

### Worker Cluster

The worker cluster acts like a standalone Kueue cluster.
The workloads and jobs are created and deleted by the MultiKueue Admission Check Controller running in the manager cluster.

## Job Flow

For a job to be subject to multi cluster dispatching, you need to assign it to a ClusterQueue that uses a MultiKueue AdmissionCheck. The Multikueue system works as follows:
- When the job's Workload gets a QuotaReservation in the manager cluster, a copy of that Workload will be created in all the configured worker clusters.
- When one of the worker clusters admits the remote workload sent to it:
  - The manager removes all the other remote Workloads.
  - The manager creates a copy of the job in the selected worker cluster, configured to use the quota reserved by the admitted Workload by setting the job's `kueue.x-k8s.io/prebuilt-workload-name` label.
- The manager monitors the remote objects, workload and job, and syncs any changes in their status into the local objects.
- When the remote workload is marked as `Finished`:
  - The manager does a last sync for the objects status.
  - The manager removes the objects from the worker cluster.

## Supported jobs

MultiKueue supports a variety of workloads.
You can learn how to:
- [Dispatch a Kueue managed Deployment](docs/tasks/run/multikueue/deployment).
- [Dispatch a Kueue managed batch/Job](docs/tasks/run/multikueue/job).
- [Dispatch a Kueue managed JobSet](docs/tasks/run/multikueue/jobset).
- [Dispatch a Kueue managed Kubeflow Jobs](docs/tasks/run/multikueue/kubeflow).
- [Dispatch a Kueue managed KubeRay workloads](docs/tasks/run/multikueue/kuberay).
- [Dispatch a Kueue managed MPIJob](docs/tasks/run/multikueue/mpijob).
- [Dispatch a Kueue managed AppWrapper](docs/tasks/run/multikueue/appwrapper).
- [Dispatch a Kueue managed plain Pod](docs/tasks/run/multikueue/plain_pods).

## Submitting Jobs
In a [configured MultiKueue environment](/docs/tasks/manage/setup_multikueue), you can submit any MultiKueue supported job to the Manager cluster, targeting a ClusterQueue configured for Multikueue.
Kueue delegates the job to the configured worker clusters without any additional configuration changes.

## What’s next? 
- Learn how to [setup a MultiKueue environment](/docs/tasks/manage/setup_multikueue/)
- Learn how to [run jobs](/docs/tasks/run/multikueue) in MultiKueue environment.
