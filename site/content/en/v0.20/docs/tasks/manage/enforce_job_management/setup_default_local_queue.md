---
title: "Setup default LocalQueue"
date: 2024-12-12
weight: 10
description: >
  Setup default LocalQueue to fullfil a queue label on jobs that submited without queue label.
---

{{< feature-state state="stable" for_version="v0.17" >}}

This page describes how to setup default LocalQueue to ensure that all Workloads submitted to a specific namespace are managed by Kueue,
even if the `kueue.x-k8s.io/queue-name` label is not specified explicitly.

## Setup default LocalQueue

LocalQueueDefaulting is a feature that allows the use of a LocalQueue with name `default` as the default LocalQueue
for workloads in the same namespace that do not have the `kueue.x-k8s.io/queue-name` label.

To use this feature:

- create a LocalQueue with the name `default` in a namespace.

That's all! Now, to test the feature, create a Job in the same namespace. Observe that the Job is updated with the `kueue.x-k8s.io/queue-name: default` label.

Note that workloads created in a different namespace or workloads that already have the `kueue.x-k8s.io/queue-name` label won't be modified.
