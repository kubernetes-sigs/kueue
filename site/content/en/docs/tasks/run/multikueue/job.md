---
title: "Run Kubernetes Job in Multi-Cluster"
linkTitle: "Kubernetes Job"
weight: 2
date: 2024-11-05
description: >
  Run a MultiKueue scheduled Kubernetes Job.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

For the ease of setup and use we recommend using at least Kueue v0.8.1.

## Run Kubernetes Job example

Once the setup is complete you can test it by running the [`job-sample.yaml`](/docs/tasks/run/jobs/#1-define-the-job) Job. 


## ManagedBy

The batch/Job can work in two different ways depending on the state of `MultiKueueBatchJobWithManagedBy` feature gate.

When `MultiKueueBatchJobWithManagedBy` is disabled, the management cluster keeps the status of the Job `Pending` during the remote job execution.
The sync happens only when the remote workload is marked as `Finished`.

When `MultiKueueBatchJobWithManagedBy` is enabled, Kueue defaults the `spec.managedBy` field to `kueue.x-k8s.io/multikueue`.
This allows the Jobs Controller to ignore the Jobs managed by MultiKueue on the management cluster, while 
the status of the Job is synchronized during the remote job execution.

{{% alert title="Note" color="primary" %}}
Note: The MultiKueue admission check controller will `Reject` the workload that does not have the `spec.managedBy` field set to `kueue.x-k8s.io/multikueue`, causing it to be marked as `Finished` with an error indicating the cause.

{{% /alert %}}
