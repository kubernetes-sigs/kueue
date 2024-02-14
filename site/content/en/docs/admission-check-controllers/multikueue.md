---
title: "MultiKueue Admission Check Controller"
date: 2024-02-08
weight: 2
description: >
  An admission check controller for multi cluster job dispatching.
---

{{% alert title="Warning" color="warning" %}}
_Available in Kueue v0.6.0 and later_
{{% /alert %}}

MultiKueue is an [admission check](/docs/concepts/admission_check/) controller designed to provide the manager side functionality for multi cluster job dispatching.

You can enable it by setting the `MultiKueue` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

{{% alert title="Warning" color="warning" %}}
MultiKueue is currently an alpha feature and disabled by default.
{{% /alert %}}

## MultiKueue Overview

A MultiKueue setup is composed of a manager cluster and at least one worker cluster.

### Cluster Roles
#### Manager Cluster

The manager's main responsibilities are:
- Establish and maintain the connection with the worker clusters.
- Create and monitor remote objects (workloads or jobs) while keeping the local ones in sync.

The MultiKueue Admission Check Controller runs in the manager cluster and will also maintain the `Active` status of the Admission Checks controlled by `multikueue`.

The quota set for the flavors of a ClusterQueue using MultiKueue controls how many jobs are subject for dispatching at a given point in time.
Ideally, the quota in the manager cluster should be equal to the total quotas in the worker clusters.
If significantly lower, the worker clusters will be under utilized.
If significantly higher, the manager will dispatch and monitor workloads in the worker clusters that don't have a chance to be admitted.

#### Worker Cluster

The worker cluster acts like a standalone Kueue cluster.
The workloads and jobs are created and deleted by the MultiKueue Admission Check Controller running in the manager cluster.

### Job Flow

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

### batch/Job
Known Limitations:
- Since unsuspending a Job in the manager cluster will lead to its local execution, the AdmissionCheckStates are kept `Pending` during the remote job execution.
- Since updating the status of a local Job could conflict with the Kubernetes Job controller, the manager does not sync the Job status during the job execution. The manager copies the final status of the remote Job when the remote workload is marked as `Finished`.

There is an ongoing effort to overcome these limitations by adding the possibility to disable the reconciliation of some jobs by the Kubernetes `batch/Job` controller. Details in `kubernetes/enhancements` [KEP-4368](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/4368-support-managed-by-label-for-batch-jobs#readme).

### JobSet
Known Limitations:
- Since unsuspending a JobSet in the manager cluster will lead to its local execution and updating the status of a local JobSet could conflict with its main controller, you should only install the JobSet CRDs, but not the controller.

An approach similar to the one described for [`batch/Job`](#batchjob) is taken into account to overcome this. 

## Parameters

An AdmissionCheck controlled by `multikueue` should use a `kueue.x-k8s.io/v1alpha1` `MultiKueueConfig` parameters object, details of its structure can be found in [Kueue Alpha API reference section](/docs/reference/kueue-alpha.v1alpha1/#kueue-x-k8s-io-v1alpha1-MultiKueueConfig)

## Example

### Setup

#### Worker Cluster

When MultiKueue dispatches a workload from the manager cluster to a worker cluster, it expects that the job's namespace and LocalQueue also exist in the worker cluster.
In other words, you should ensure that the worker cluster configuration mirrors the one of the manager cluster in terms of namespaces and LocalQueues.

#### Manager Cluster

{{< include "/examples/multikueue/multikueue-setup.yaml" "yaml" >}}

For the example provided, having the worker1 cluster kubeconfig stored in a file called `worker1.kubeconfig`, you can create the `worker1-secret` secret by running the following command:

```bash
 kubectl create secret generic worker1-secret -n kueue-system --from-file=kubeconfig=worker1.kubeconfig
```
