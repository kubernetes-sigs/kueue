---
title: "Run StatefulSet in Multi-Cluster"
linkTitle: "StatefulSet"
weight: 2
date: 2025-01-15
description: >
  Run a MultiKueue scheduled StatefulSet.
---

## Before you begin

Check the [MultiKueue installation guide](/docs/tasks/manage/setup_multikueue) on how to properly setup MultiKueue clusters.

{{% alert title="Pod integration requirements" color="primary" %}}
Since Kueue v0.15, you don't need to explicitly enable `"pod"` integration to use the `"statefulset"` integration.

For Kueue v0.14 and earlier, `"pod"` integration must be explicitly enabled.

See [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin) for configuration details.
{{% /alert %}}

## Run StatefulSet example

Once the setup is complete you can test it by running the example below:

{{< include "examples/serving-workloads/sample-statefulset.yaml" "yaml" >}}
