---
title: "Run A Sandbox"
linkTitle: "Sandbox"
date: 2026-04-19
weight: 8
description: >
  Integrate Kueue with the Sandbox operator.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Sandboxes](https://github.com/kubernetes-sigs/agent-sandbox).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

The Sandbox operator provides isolated environments for running AI agent workloads.
Kueue manages the Pods created by the Sandbox controller through the [Plain Pod](/docs/tasks/run/plain_pods) integration,
where each Sandbox Pod is represented as a single independent Plain Pod.

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

1. Follow the steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin) to learn how to enable and configure the `pod` integration.

1. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

1. Install the [Sandbox operator](https://github.com/kubernetes-sigs/agent-sandbox).

## Sandbox definition

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the
`spec.podTemplate.metadata.labels` section of the Sandbox configuration.

```yaml
spec:
  podTemplate:
    metadata:
      labels:
        kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs

The resource needs of the workload can be configured in the `spec.podTemplate.spec.containers`.

```yaml
spec:
  podTemplate:
    spec:
      containers:
      - resources:
          requests:
            cpu: "100m"
            memory: "200Mi"
```

## Sample Sandbox

Here is a sample Sandbox:

{{< include "examples/pod-based-workloads/sample-sandbox.yaml" "yaml" >}}

## Limitations

- Kueue will only manage pods created by the Sandbox operator.
- Each Sandbox Pod will create a new Workload resource and must wait for admission by Kueue.
