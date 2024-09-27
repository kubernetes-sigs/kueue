---
title: "Run Jobsets in MultiKueue"
linkTitle: "Jobsets"
weight: 2
date: 2024-09-25
description: >
  Run a MultiKueue scheduled JobSet.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

We recommend using Kueue v0.8.1 or newer and JobSet v0.5.2 or newer.

## Run Jobsets example

The same `jobset-sample.yaml` file from [single cluster environment](docs/tasks/run/jobsets) can be used in a [MultiKueue environment](#multikueue-environment).

In that setup, the `spec.managedBy` field will be set to `kueue.x-k8s.io/multikueue`
automatically, if not specified, as long as  the `kueue.x-k8s.io/queue-name` annotation
is specified and the corresponding Cluster Queue uses the Multi Kueue admission check.