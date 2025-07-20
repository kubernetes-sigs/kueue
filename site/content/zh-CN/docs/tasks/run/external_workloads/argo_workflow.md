---
title: "运行 Argo Workflow"
linkTitle: "Argo Workflow"
date: 2025-01-23
weight: 3
description: >
  将 Kueue 与 Argo Workflow 集成。
---

此页面展示了在运行 [Argo Workflow](https://argo-workflows.readthedocs.io/en/latest/)
时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-cn/docs/tasks#batch-user)。
欲了解更多信息，请参见 [Kueue 概述](/zh-CN/docs/overview)。

目前，Kueue 不直接支持 Argo Workflow 的 [Workflow](https://argo-workflows.readthedocs.io/en/latest/workflow-concepts/) 资源，
但你可以利用 Kueue [管理普通 Pod](/zh-CN/docs/tasks/run_plain_pods) 的能力来集成它们。

## 开始之前

1. 学习如何[使用自定义管理器配置安装 Kueue](/zh-CN/docs/installation/#install-a-custom-configured-released-version)。

2. 遵循[运行普通 Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin)
   中的步骤来学习如何启用和配置 `pod` 集成。

3. 安装 [Argo Workflow](https://argo-workflows.readthedocs.io/en/latest/installation/#installation)

## Workflow 定义  {#workflow-definition}

### a. 针对单个本地队列

如果你希望整个工作流针对单个[本地队列](/zh-CN/docs/concepts/local_queue)，
应在 Workflow 配置的 `spec.podMetadata` 部分指定它。

{{< include "examples/pod-based-workloads/workflow-single-queue.yaml" "yaml" >}}

### b. 针对每个模板的不同本地队列

如果你希望针对工作流的每一步使用不同的[本地队列](/zh-CN/docs/concepts/local_queue)，
可以在 Workflow 配置的 `spec.templates[].metadata` 部分定义队列。

在这个例子中，`hello1` 和 `hello2a` 将针对 `user-queue`，
而 `hello2b` 将针对 `user-queue-2`。

{{< include "examples/pod-based-workloads/workflow-queue-per-template.yaml" "yaml" >}}

### c. 限制

- Kueue 仅管理由 Argo Workflow 创建的 Pod。它不会以任何方式管理 Argo Workflow 资源。
- Workflow 中的每个 Pod 都将创建一个新的 Workload 资源，并且必须等待 Kueue 的准入。
- 没有办法确保 Workflow 在开始前完成。如果多步骤工作流中的某一步没有可用配额，
  Argo Workflow 将运行所有之前的步骤并等待配额变为可用。
- Kueue 不理解 Argo Workflow 的 `suspend` 标志，也不会管理它。
- Kueue 不管理 `suspend`、`http` 或 `resource` 模板类型，因为它们不创建 Pod。
