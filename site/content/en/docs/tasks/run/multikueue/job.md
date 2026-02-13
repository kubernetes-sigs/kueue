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

The current status of a Job being executed by MultiKueue on a worker cluster is live-updated on the management cluster.

This gives the users and automation tools the ability to track the progress of Job status (.status) without lookup to the
worker cluster, making MultiKueue transparent from that perspective.

## Example

Once the setup is complete you can test it by running the example below:

{{< include "examples/jobs/sample-job.yaml" "yaml" >}}
