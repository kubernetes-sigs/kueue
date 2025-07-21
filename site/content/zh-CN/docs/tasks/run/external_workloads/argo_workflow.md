---
title: "运行 Argo Workflow"
linkTitle: "Argo Workflow"
date: 2025-01-23
weight: 3
description: 将 Kueue 集成到 Argo Workflows。
---

本页展示了在运行 [Argo Workflows](https://argo-workflows.readthedocs.io/en/latest/) 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批量用户](/docs/tasks#batch-user)。更多信息请参见 [Kueue 概述](/docs/overview)。

目前 Kueue 不直接支持 Argo Workflows 的 [Workflow](https://argo-workflows.readthedocs.io/en/latest/workflow-concepts/) 资源，
但你可以利用 Kueue [管理原生 Pod](/docs/tasks/run_plain_pods) 的能力进行集成。

## 开始之前

1. 学习如何[使用自定义管理器配置安装 Kueue](/docs/installation/#install-a-custom-configured-released-version)。

2. 按照[运行原生 Pod](/docs/tasks/run/plain_pods/#before-you-begin)中的步骤，
了解如何启用和配置 `pod` 集成。

3. 安装 [Argo Workflows](https://argo-workflows.readthedocs.io/en/latest/installation/#installation)

## Workflow 定义

### a. 目标为单个 LocalQueue

如果你希望整个 workflow 只使用一个 [local queue](/docs/concepts/local_queue)，
应在 Workflow 配置的 `spec.podMetadata` 部分指定。

{{< include "examples/pod-based-workloads/workflow-single-queue.yaml" "yaml" >}}

### b. 每个模板目标不同的 LocalQueue

如果你希望 workflow 的每个步骤使用不同的 [local queue](/docs/concepts/local_queue)，
可以在 Workflow 配置的 `spec.templates[].metadata` 部分指定队列。

在此示例中，`hello1` 和 `hello2a` 会使用 `user-queue`，`hello2b` 会
使用 `user-queue-2`。

{{< include "examples/pod-based-workloads/workflow-queue-per-template.yaml" "yaml" >}}

### c. 限制

- Kueue 只管理 Argo Workflows 创建的 Pod，不会以任何方式管理 Argo Workflows 资源本身。
- Workflow 中的每个 Pod 都会创建一个新的 Workload 资源，并且必须等待 Kueue 的准入。
- 无法保证 Workflow 会在启动前就能全部完成。如果多步骤 Workflow 的某一步没有
可用配额，Argo Workflows 会运行所有前置步骤，然后等待配额可用。
- Kueue 不理解 Argo Workflows 的 `suspend` 标志，也不会管理它。
- Kueue 不管理 `suspend`、`http` 或 `resource` 类型的模板，因为它们不会创建 Pod。
