---
title: "运行 Flux MiniCluster"
linkTitle: "Flux MiniClusters"
date: 2022-02-14
weight: 2
description: >
  运行由 Kueue 调度的 Flux MiniCluster。
---

本页展示了在运行 [Flux Operator](https://flux-framework.org/flux-operator/) 的 MiniClusters 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批量用户](/docs/tasks#batch-user)。更多信息请参见 [Kueue 概述](/docs/overview)。

## 开始之前

请查阅[管理集群配额](/docs/tasks/manage/administer_cluster_quotas)以了解初始集群设置的详细信息。

请查阅 [Flux Operator 安装指南](https://flux-framework.org/flux-operator/getting_started/user-guide.html#install)。

## MiniCluster 定义

由于 Flux MiniCluster 以 [`batch/Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/) 形式运行，Kueue 无需额外组件即可管理 Flux MiniCluster。
但请注意以下几点：

### a. 队列选择

目标 [local queue](/docs/concepts/local_queue) 应在 MiniCluster 配置的 `spec.jobLabels` 部分指定。

```yaml
  jobLabels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求

工作负载的资源需求可在 MiniCluster 配置的 `spec.container[*].resources` 部分进行配置。

```yaml
spec:
  containers:
    - image: <image>
      resources:
        requests:
          cpu: 4
          memory: "200Mi"
```

## MiniCluster 示例

```yaml
apiVersion: flux-framework.org/v1alpha1
kind: MiniCluster
metadata:
  generateName: flux-sample-kueue-
spec:
  size: 1
  containers:
    - image: ghcr.io/flux-framework/flux-restful-api:latest
      command: sleep 10 
      resources:
        requests:
          cpu: 4
          memory: "200Mi"
  jobLabels:
    kueue.x-k8s.io/queue-name: user-queue
```

如需使用 Python 实现的等效操作，请参见[运行 Python 作业](/docs/tasks/run/python_jobs/#flux-operator-job)。
