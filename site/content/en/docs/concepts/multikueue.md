---
title: "MultiKueue"
date: 2024-02-26
weight: 7
description: >
  Kueue multi cluster job dispatching.
---

{{% alert title="Warning" color="warning" %}}
_Available in Kueue v0.6.0 and later_
{{% /alert %}}

{{% alert title="Warning" color="warning" %}}
MultiKueue is currently an alpha feature and disabled by default. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.
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
Known Limitations:
- Since unsuspending a Job in the manager cluster will lead to its local execution, the AdmissionCheckStates are kept `Pending` during the remote job execution.
- Since updating the status of a local Job could conflict with the Kubernetes Job controller, the manager does not sync the Job status during the job execution. The manager copies the final status of the remote Job when the remote workload is marked as `Finished`.

There is an ongoing effort to overcome these limitations by adding the possibility to disable the reconciliation of some jobs by the Kubernetes `batch/Job` controller. Details in `kubernetes/enhancements` [KEP-4368](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/4368-support-managed-by-label-for-batch-jobs#readme).

### JobSet

Since unsuspending a JobSet in the manager cluster will lead to its local execution and updating the status of a local JobSet could conflict with its main controller, MultiKueue expects the JobSets submitted to a ClusterQueue using it to have `spec.managedBy` set to `kueue.x-k8s.io/multikueue`. The JobSet `managedBy` field is available since JobSet v0.5.0.

## Submitting Jobs
In a [configured MultiKueue environment](/docs/tasks/manage/setup_multikueue), you can submit any MultiKueue supported job to the Manager cluster, targeting a ClusterQueue configured for Multikueue.
Kueue delegates the job to the configured worker clusters without any additional configuration changes.

## What’s next? 
- Learn how to [setup a MultiKueue environment](/docs/tasks/manage/setup_multikueue/)
- Learn how to [submit JobSets](/docs/tasks/run/jobsets/#jobset-definition) to a running Kueue cluster.
- Learn how to [submit batch/Jobs](/docs/tasks/run/jobs/#1-define-the-job) to a running Kueue cluster.
