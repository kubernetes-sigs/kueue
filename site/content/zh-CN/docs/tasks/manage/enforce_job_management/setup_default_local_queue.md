---
title: "Setup default LocalQueue"
date: 2024-12-12
weight: 10
description: >
  Setup default LocalQueue to fullfil a queue label on jobs that submited without queue label.
---

{{< feature-state state="beta" for_version="v0.12" >}}

{{% alert title="Note" color="primary" %}}

`LocalQueueDefaulting` is a Beta feature that is enabled by default.

You can disable it by setting the `LocalQueueDefaulting` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.
{{% /alert %}}

This page describes how to setup default LocalQueue to ensure that all Workloads submitted to a specific namespace are managed by Kueue,
even if the `kueue.x-k8s.io/queue-name` label is not specified explicitly.

## Setup default LocalQueue

LocalQueueDefaulting is an Beta feature that allows the use of a LocalQueue with name `default` as the default LocalQueue
for workloads in the same namespace that do not have the `kueue.x-k8s.io/queue-name` label.
The feature is gated by the `LocalQueueDefaulting` feature gate, and is enabled by default. To use this feature:

- create a LocalQueue with the name `default` in a namespace.

That's all! Now, to test the feature, create a Job in the same namespace. Observe that the Job is updated with the `kueue.x-k8s.io/queue-name: default` label.

Note that workloads created in a different namespace or workloads that already have the `kueue.x-k8s.io/queue-name` label won't be modified.
