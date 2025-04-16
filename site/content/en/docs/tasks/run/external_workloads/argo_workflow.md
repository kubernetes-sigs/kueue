---
title: "Run An Argo Workflow"
linkTitle: "Argo Workflow"
date: 2025-01-23
weight: 3
description: >
  Integrate Kueue with Argo Workflows.
---

This page shows how to leverage Kueue's scheduling and resource management capabilities when running [Argo Workflows](https://argo-workflows.readthedocs.io/en/latest/).

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of Kueue. For more information, see [Kueue's overview](/docs/overview).

Currently Kueue doesn't support Argo Workflows [Workflow](https://argo-workflows.readthedocs.io/en/latest/workflow-concepts/) resources directly,
but you can take advantage of the ability for Kueue to [manage plain pods](/docs/tasks/run_plain_pods) to integrate them.

## Before you begin

1. Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

2. Follow steps in [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin)
to learn how to enable and configure the `pod` integration.

3. Install [Argo Workflows](https://argo-workflows.readthedocs.io/en/latest/installation/#installation)

## Workflow definition

### a. Targeting a single LocalQueue

If you want the entire workflow to target a single [local queue](/docs/concepts/local_queue),
it should be specified in the `spec.podMetadata` section of the Workflow configuration.

{{< include "examples/pod-based-workloads/workflow-single-queue.yaml" "yaml" >}}

### b. Targeting a different LocalQueue per template

If you prefer to target a different [local queue](/docs/concepts/local_queue) for each step of your Workflow,
you can define the queue in the `spec.templates[].metadata` section of the Workflow configuration.

In this example `hello1` and `hello2a` will target `user-queue` and `hello2b` will
target `user-queue-2`.

{{< include "examples/pod-based-workloads/workflow-queue-per-template.yaml" "yaml" >}}

### c. Limitations

- Kueue will only manage pods created by Argo Workflows. It does not manage the Argo Workflows resources in any way.
- Each pod in a Workflow will create a new Workload resource and must wait for admission by Kueue.
- There is no way to ensure that a Workflow will complete before it is started. If one step of a multi-step Workflow does not have
available quota, Argo Workflows will run all previous steps and then wait for quota to become available.
- Kueue does not understand Argo Workflows `suspend` flag and will not manage it.
- Kueue does not manage `suspend`, `http`, or `resource` template types since they do not create pods.
