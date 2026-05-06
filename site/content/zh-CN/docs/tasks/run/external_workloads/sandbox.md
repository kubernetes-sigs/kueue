---
title: "运行 Sandbox"
linkTitle: "Sandbox"
date: 2026-04-19
weight: 8
description: >
  将 Kueue 与 Sandbox Operator 集成。
---

此页面展示了在运行 [Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) 时，
如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-CN/docs/tasks#batch-user)。
欲了解更多，请参阅 [Kueue 概述](/zh-CN/docs/overview)。

Sandbox Operator 为运行 AI Agent 工作负载提供隔离环境。
Kueue 通过 [Plain Pod](/zh-CN/docs/tasks/run/plain_pods) 集成来管理 Sandbox 控制器创建的 Pod，
其中每个 Sandbox Pod 都会表现为一个独立的 Plain Pod。

## 开始之前

1. 学习如何[安装具有自定义管理器配置的 Kueue](/zh-CN/docs/installation/#install-a-custom-configured-released-version)。

2. 按照[运行 Plain Pod](/zh-CN/docs/tasks/run/plain_pods/#before-you-begin)
   中的步骤学习如何启用和配置 `pod` 集成。

3. 查看[管理员集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas/)以获取初始 Kueue 设置的详细信息。

4. 安装 [Sandbox Operator](https://github.com/kubernetes-sigs/agent-sandbox)。

## Sandbox 定义

### a. 选择队列

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 Sandbox 配置的
`spec.podTemplate.metadata.labels` 部分中指定。

```yaml
spec:
  podTemplate:
    metadata:
      labels:
        kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求

工作负载的资源需求可以在 `spec.podTemplate.spec.containers` 中配置。

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

## Sandbox 示例

下面是一个 Sandbox 示例：

{{< include "examples/pod-based-workloads/sample-sandbox.yaml" "yaml" >}}

## 限制

- Kueue 只会管理由 Sandbox Operator 创建的 Pod。
- 每个 Sandbox Pod 都会创建一个新的 Workload 资源，并且必须等待 Kueue 准入。
