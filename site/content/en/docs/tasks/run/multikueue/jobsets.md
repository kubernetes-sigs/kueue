---
title: "Run Jobsets in Multi-Cluster"
linkTitle: "Jobsets"
weight: 3
date: 2024-09-25
description: >
  Run a MultiKueue scheduled JobSet.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

For the ease of setup and use we recommend using at least Kueue v0.8.1 and JobSet v0.6.0 (see [JobSet Installation](https://jobset.sigs.k8s.io/docs/installation/) for more details).

## Run Jobsets example

Once the setup is complete you can test it by running the [`jobset-sample.yaml`](/docs/tasks/run/jobsets/#example-jobset) Job. 

In that setup, the `spec.managedBy` field is defaulted to `kueue.x-k8s.io/multikueue`
automatically, if not specified, as long as  the `kueue.x-k8s.io/queue-name` annotation
is specified and the corresponding Cluster Queue uses the Multi Kueue admission check.