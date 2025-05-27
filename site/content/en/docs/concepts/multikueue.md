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

### batch/Job

The batch/Job integration can work in two different ways depending on the state of MultiKueueBatchJobWithManagedBy feature gate, check the [Change the feature gates configuration](/docs/installation/#change-the-feature-gates-configuration) for setup details.

#### MultiKueueBatchJobWithManagedBy disabled

Known Limitations:
- Since unsuspending a Job in the manager cluster will lead to its local execution, the AdmissionCheckStates are kept `Pending` during the remote job execution.
- Since updating the status of a local Job could conflict with the Kubernetes Job controller, the manager does not sync the Job status during the job execution. The manager copies the final status of the remote Job when the remote workload is marked as `Finished`.

#### MultiKueueBatchJobWithManagedBy enabled

When you want to submit Job to a ClusterQueue with a MultiKueue admission check, you should set the `spec.managedBy` field to `kueue.x-k8s.io/multikueue`, otherwise the admission check controller will `Reject` the workload.

The `managedBy` field is available as an Alpha feature staring Kubernetes 1.30.0, check the [Delegation of managing a Job object to external controller](https://kubernetes.io/docs/concepts/workloads/controllers/job/#delegation-of-managing-a-job-object-to-external-controller) for details.

### JobSet

We recommend using JobSet v0.5.1 or newer.

### Kubeflow

The supported version of the Kubeflow Trainer is v1.7.0, or a newer version.
The Management cluster should only install the CRDs and not the package itself. 
On the other hand, the Worker cluster should install the full kubeflow operator.

## Plain Pods

MultiKueue supports the remote creation and management of Plain Pods and Group of Pods.

## Deployments

MultiKueue supports the remote creation and management of Deployment replica Pods.

Known Limitations:
- When creating a Deployment in environments with more than 1 worker cluster it is possible that replicas are scheduled in different clusters.

{{% alert title="Note" color="primary" %}}
Follow steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin) to learn how to enable and configure the `pod` integration which is required for enabling the `deployment` integration.
{{% /alert %}}

## Submitting Jobs
In a [configured MultiKueue environment](/docs/tasks/manage/setup_multikueue), you can submit any MultiKueue supported job to the Manager cluster, targeting a ClusterQueue configured for Multikueue.
Kueue delegates the job to the configured worker clusters without any additional configuration changes.

## What’s next? 
- Learn how to [setup a MultiKueue environment](/docs/tasks/manage/setup_multikueue/)
- Learn how to [run jobs](/docs/tasks/run/multikueue) in MultiKueue environment.
