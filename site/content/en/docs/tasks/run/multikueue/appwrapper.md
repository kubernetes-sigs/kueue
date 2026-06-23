---
title: "Run AppWrappers in Multi-Cluster"
linkTitle: "AppWrappers"
weight: 3
date: 2025-06-23
description: >
  Run a MultiKueue scheduled AppWrapper.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

For the ease of setup and use we recommend using at least Kueue v0.11.0 and at least AppWrapper Operator v1.1.1.

See [AppWrapper Quick-Start Guide](https://project-codeflare.github.io/appwrapper/quick-start/) for installation and configuration details of the AppWrapper Operator.

{{% alert title="Note" color="primary" %}}
MultiKueue support for AppWrappers is not available in versions of Kueue before v0.11.0.
{{% /alert %}}

## MultiKueue integration

Once the setup is complete you can test it by running an AppWrapper [`appwrapper-pytorch-sample.yaml`](/docs/tasks/run/appwrappers/#example-appwrapper-containing-a-pytorchjob).

{{% alert title="Note" color="primary" %}}
Note: Kueue defaults the `spec.managedBy` field to `kueue.x-k8s.io/multikueue` on the management cluster for AppWrappers.

This allows the AppWrapper Operator to ignore the Jobs managed by MultiKueue on the management cluster, and in particular skip creation of the wrapped resources.

The resources are created and the actual computation will happen on the mirror copy of the AppWrapper on the selected worker cluster.
The mirror copy of the AppWrapper does not have the field set.
{{% /alert %}}
