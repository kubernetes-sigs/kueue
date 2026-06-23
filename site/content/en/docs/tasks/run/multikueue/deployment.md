---
title: "Run Deployment in Multi-Cluster"
linkTitle: "Deployment"
weight: 2
date: 2025-02-17
description: >
  Run a MultiKueue scheduled Deployment.
---

## Before you begin

1. Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

2. Deployments receive live status updates through the status from the remote Pods created on the worker cluster.

   {{% alert title="Pod integration requirements" color="primary" %}}
   Since Kueue v0.15, you don't need to explicitly enable `"pod"` integration to use the `"deployment"` integration.

   For Kueue v0.14 and earlier, `"pod"` integration must be explicitly enabled.

   See [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin) for configuration details.
   {{% /alert %}}

{{< feature-state state="beta" for_version="v0.11.0" >}}

{{% alert title="Note" color="primary" %}}
In this current implementation, when Deployments are created in environments with multiple worker clusters, Pods are allocated to any worker.
{{% /alert %}}

## Example

Once the setup is complete you can test it by running the example below:

{{< include "examples/serving-workloads/sample-deployment.yaml" "yaml" >}}
