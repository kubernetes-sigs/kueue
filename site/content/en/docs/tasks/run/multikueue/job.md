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

The recommended way of running MultiKueue depends on the configuration of the `JobManagedBy` feature gate in your cluster. 

{{% alert title="Note" color="primary" %}}
The `JobManagedBy` feature gate is disabled in 1.30 and 1.31 by default, and will be enabled in 1.32 by default.
{{% /alert %}}

### Cluster with JobManagedBy enabled

When `JobManagedBy` is enabled in your cluster we recommend configuring Kueue to enable the `MultiKueueBatchJobWithManagedBy` feature gate. 

When `MultiKueueBatchJobWithManagedBy` is enabled the Job is processed by MultiKueue, which effectively reflects the job state in the management cluster.

So regardless of the number of clusters, all states of all jobs are available in the management cluster.

This in turn allows you to track the progress of a job as it occurs.

### Cluster with JobManagedBy disabled

When `JobManagedBy` is disabled in your cluster you should make sure `MultiKueueBatchJobWithManagedBy` is also disabled in Kueue. 

This is important so that MultiKueue does not conflict with the build-in Job controller on the management cluster. 

As a limitation of this deployment mode, to get the actual status of the job, you need to access the worker cluster.

You can identify the worker cluster running the job by checking the AC status message of the workload object in the management cluster.

Also, the job is suspended from the perspective of the management cluster until it is `Finished`.

## Run Kubernetes Job example

Once the setup is complete you can test it by running the example below:

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}
