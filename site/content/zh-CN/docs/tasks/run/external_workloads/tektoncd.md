---
title: "运行 Tekton Pipeline"
linkTitle: "Tekton Pipeline"
date: 2025-02-01
weight: 7
description: 将 Kueue 集成到 Tekton Pipelines。
---

本页展示了在运行 [Tekton pipelines](https://tekton.dev/docs/) 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批量用户](/docs/tasks#batch-user)。更多信息请参见 [Kueue 概述](/docs/overview)。

我们演示了如何基于 [Plain Pod](/docs/tasks/run_plain_pods) 集成支持在 Kueue 中调度 Tekton Pipelines 的任务，其中 Pipeline 的每个 Pod 都被视为一个独立的原生 Pod。

## 开始之前

1. 学习如何[使用自定义管理器配置安装 Kueue](/docs/installation/#install-a-custom-configured-released-version)。

1. 按照[运行原生 Pod](docs/tasks/run/plain_pods/#before-you-begin)中的步骤，了解如何启用和配置 `pod` 集成。

1. 查阅[管理员集群配额](/docs/tasks/manage/administer_cluster_quotas/)以了解 Kueue 的初始设置。

1. 集群已[安装](https://tekton.dev/docs/installation/pipelines/) Tekton pipelines。


## Tekton 背景

Tekton 有 [Pipelines](https://tekton.dev/vault/pipelines-v0.59.x-lts/pipelines/)、[Tasks](https://tekton.dev/vault/pipelines-v0.59.x-lts/tasks/) 和 [PipelineRun](https://tekton.dev/vault/pipelines-v0.59.x-lts/pipelineruns/) 的概念。

一个 pipeline 由多个 task 组成。task 和 pipeline 必须在运行 pipeline 前创建。

PipelineRun 用于运行 pipeline。

TaskRun 用于运行单个 task。PipelineRun 会复用 TaskRun 来运行 pipeline 中的每个 task。

### Tekton 定义

作为简单示例，我们将定义两个名为 sleep 和 hello 的 task：

{{< include "examples/pod-based-workloads/tekton-sleep-task.yaml" "yaml" >}}

{{< include "examples/pod-based-workloads/tekton-hello-task.yaml" "yaml" >}}

pipeline 组合这些 task。

{{< include "examples/pod-based-workloads/tekton-pipeline.yaml" "yaml" >}}

## a. 目标为单个 LocalQueue

如果你希望每个 task 都使用同一个 [local queue](/docs/concepts/local_queue)，
应在 PipelineRun 配置的 `metadata.label` 部分指定。

{{< include "examples/pod-based-workloads/tekton-pipeline-run.yaml" "yaml" >}}

这会在 pipeline 的每个 pod 上注入 kueue 标签。当你超出配额限制时，Kueue 会阻塞这些 pod。

## 限制

- Kueue 只管理 Tekton 创建的 pod。
- Workflow 中的每个 pod 都会创建一个新的 Workload 资源，并且必须等待 Kueue 的准入。
- 无法保证 Workflow 会在启动前就能全部完成。如果多步骤 Workflow 的某一步没有可用配额，Tekton pipelines 会运行所有前置步骤，然后等待配额可用。
