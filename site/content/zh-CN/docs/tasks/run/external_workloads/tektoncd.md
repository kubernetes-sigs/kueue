---
title: "运行 Tekton Pipeline"
linkTitle: "Tekton Pipeline"
date: 2025-02-01
weight: 7
description: >
  将 Kueue 与 Tekton Pipeline 集成。
---

此页面展示了在运行 [Tekton Pipeline](https://tekton.dev/docs/) 时，
如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-CN/docs/tasks#batch-user)。
欲了解更多，请参阅 [Kueue 概述](/zh-CN/docs/overview)。

我们将演示如何基于 [Plain Pod](/zh-CN/docs/tasks/run_plain_pods) 集成，
在 Kueue 中支持 Tekton Pipeline 任务的调度，其中来自 Pipeline 的每个 Pod 都表现为单个独立的 Plain Pod。

## 开始之前

1. 学习如何[安装具有自定义管理器配置的 Kueue](/zh-CN/docs/installation/#install-a-custom-configured-released-version)。

2. 按照[运行 Plain Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin)
   中的步骤学习如何启用和配置 `pod` 集成。

3. 查看[管理员集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas/)以获取初始 Kueue 步骤的详细信息。

4. 确保你的集群已安装 Tekton Pipeline：[安装指南](https://tekton.dev/docs/installation/pipelines/)。

## Tekton 背景   {#tekton-background}

Tekton 有 [Pipeline](https://tekton.dev/vault/pipelines-v0.59.x-lts/pipelines/)、
[Task](https://tekton.dev/vault/pipelines-v0.59.x-lts/tasks/) 和
[PipelineRun](https://tekton.dev/vault/pipelines-v0.59.x-lts/pipelineruns/) 的概念。

一个 Pipeline 由多个 Task 组成。Task 和 Pipeline 必须在运行 Pipeline 之前创建。

一个 PipelineRun 运行 Pipeline。

一个 TaskRun 运行单个 Task。PipelineRun 将重用 TaskRun 来运行 Pipeline 中的每个 Task。

### Tekton 定义   {#tekton-defintions}

作为一个简单的例子，我们将定义两个名为 sleep 和 hello 的 Task：

{{< include "examples/pod-based-workloads/tekton-sleep-task.yaml" "yaml" >}}

{{< include "examples/pod-based-workloads/tekton-hello-task.yaml" "yaml" >}}

一个 Pipeline 由这些 Task 组成。

{{< include "examples/pod-based-workloads/tekton-pipeline.yaml" "yaml" >}}

## a. 针对单个本地队列

如果你希望每个任务都针对一个单独的[本地队列](/zh-CN/docs/concepts/local_queue)，
那么它应该在 PipelineRun 配置的 `metadata.label` 部分中指定。

{{< include "examples/pod-based-workloads/tekton-pipeline-run.yaml" "yaml" >}}

这将在 Pipeline 的每个 Pod 上注入 Kueue 标签。一旦超出配额限制，Kueue 将阻止这些 Pod。

## 限制

- Kueue 仅管理由 Tekton 创建的 Pod。
- 工作流中的每个 Pod 都将创建一个新的 Workload 资源，并且必须等待 Kueue 的准入。
- 没有办法确保工作流在开始之前完成。如果多步骤工作流的一个步骤没有可用配额，
  Tekton Pipeline 将运行所有之前的步骤，然后等待配额变为可用。
