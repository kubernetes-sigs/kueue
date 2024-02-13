---
title: "MultiKueue Admission Check Controller"
date: 2024-02-08
weight: 2
description: >
  An admission check controller providing the manager side MultiKueue functionality.
---

The MultiKueue Admission Check Controller is an Admission Check Controller designed to provide the manager side functionality for multi cluster job dispatching.

You can enable it by setting the `MultiKueue` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

{{% alert title="Warning" color="warning" %}}
MultiKueue is currently an alpha feature and disabled by default.
{{% /alert %}}

## MultiKueue Overview

A MultiKueue setup is composed of a manager cluster and at least one worker cluster.

### Cluster Roles
#### Manager Cluster

The manager's main attributes are:
- Establish and maintain the connection with the worker clusters.
- Create and monitor remote objects (workloads or jobs) while keeping the local ones in sync.

The MultiKueue Admission Check Controller runs in the manager cluster and will also maintain the `Active` status of the Admission Checks controlled by `multikueue`.

The ResourceQuota set on a ClusterQueue using MultiKueue controls how many jobs are subject for dispatching at a given point in time, ideally it should be less then the total quotas in the worker clusters since otherwise will cause the monitoring of workloads that don't have a chance to be admitted. 

#### Worker Cluster

The worker cluster acts like an usual standalone Kueue cluster in which the Kueue the workloads and jobs are managed by the MultiKueue Admission Check Controller running in the manager cluster.

### Job Flow

If the ClusterQueue a job is assigned to in the manager cluster uses a MultiKueue AdmissionCheck it will be the subject of dispatching, as a result:

- When it's workload gets a QuotaReservation in the manager, a copy of that workload will be created in all the configured worker clusters.
- When one of the remote workloads is admitted in a worker cluster:
  - All the other remote workloads are removed.
  - A copy of the job is created in the selected worker cluster having the remote workload set as its prebuilt-workload.
  - The AdmissionCheckState of the local workload is set as `Ready` resulting in its Admission in the manager cluster.
- The remote objects , workload and job, are monitored and the changes in their status are synced back in the manager objects.
- When the remote workload is marked as `Finished`:
  - A last sync is done for the objects status.
  - The remote objects are removed from the worker cluster.


## Supported jobs
### batch/Job
Known Limitations:
- Since unsuspending a Job in the manager cluster will lead to its local execution, the AdmissionCheckStates are kept `Pending` during the remote job execution.
- Since updating the status of a local Job could conflict with the Job's main controller, the Job status is not synced during the job execution, the final status of the remote Job is copied when the remote workload is marked as `Finished`.

There is an ongoing effort to overcome these limitations by adding the possibility to disable the reconciliation of some jobs by the main `batch/Job` controller. Details in `kubernetes/enhancements` [KEP-4368](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/4368-support-managed-by-label-for-batch-jobs#readme).

### JobSet
Known Limitations:
- Since unsuspending a JobSet in the manager cluster will lead to its local execution and updating the status of a local JobSet could conflict with its main controller, only the JobSet CRDs should be installed in the manager cluster.
An approach similar to the one described for [`batch/Job`](#batchjob) is taken into account to overcome this. 

## Parameters

An AdmissionCheck controlled by `multikueue` should use a `kueue.x-k8s.io/v1alpha1` `MultiKueueConfig` parameters object, details of its structure can be found in [Kueue Alpha API reference section](/docs/reference/kueue-alpha.v1alpha1/#kueue-x-k8s-io-v1alpha1-MultiKueueConfig)

## Example

### Setup

#### Worker Cluster

When a workload is dispatched from the manager cluster to a worker cluster it is expected that its namespace and LocalQueue to be already created, hence the worker cluster configuration should mirror the one of the manager cluster in terms of namespaces and LocalQueues.

#### Manager Cluster

{{< include "/examples/multikueue/multikueue-setup.yaml" "yaml" >}}

For the example provided, having the worker1 cluster kubeconfig stored in a file called `worker1.kubeconfig`, the 
`worker1-secret` secret ca by created with:

```bash
 kubectl create secret generic worker1-secret -n kueue-system --from-file=kubeconfig=worker1.kubeconfig
```
