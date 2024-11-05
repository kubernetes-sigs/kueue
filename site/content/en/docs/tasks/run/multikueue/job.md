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

For the ease of setup and use we recommend using at least Kueue v0.8.1 and Kubernetes v1.30.0.

## Run Kubernetes Job example

Once the setup is complete you can test it by running the [`job-sample.yaml`](/docs/tasks/run/jobs/#1-define-the-job) Job. 

The recommended way of running MultiKueue depends on the configuration of the `JobManagedBy` feature gate in your cluster. 

NOTE: The `JobManagedBy` feature gate is disabled in 1.30 and 1.31 by default, and will be enabled in 1.32 by default.

## Cluster with JobManagedBy enabled

When `JobManagedBy` is enabled in your cluster we recommend configuring Kueue to enable the `MultiKueueBatchJobWithManagedBy` feature gate. 

When `MultiKueueBatchJobWithManagedBy` is enabled Kueue defaults the `spec.managedBy` field to `kueue.x-k8s.io/multikueue`. This allows to disable processing of the Job by the built-in controller, and allows MultiKueue
to send live updates reflecting the status of the mirror Job on the worker.

## Cluster with JobManagedBy disabled

When `JobManagedBy` is disabled in your cluster you should make sure `MultiKueueBatchJobWithManagedBy` 
is also disabled in Kueue. 

This is important so that MultiKueue does not conflict with the build-in Job controller
on the management cluster. 

Note that Kueue will not perform live updates of the mirror Job from a worker 
cluster.