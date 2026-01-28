---
title: "运行 Flux MiniCluster"
linkTitle: "Flux MiniClusters"
date: 2022-02-14
weight: 2
description: >
  运行 Kueue 调度的 Flux MiniCluster.
---

此页面展示了在运行 [Flux Operator 的](https://flux-framework.org/flux-operator/)
MiniCluster 时，如何利用 Kueue 的调度和资源管理能力。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-CN/docs/tasks#batch-user)。
欲了解更多信息，请参见 [Kueue 概述](/zh-CN/docs/overview)。

## 开始之前

查阅[管理集群配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)，
以获取初始集群设置的详细信息。

查阅 [Flux Operator 安装指南](https://flux-framework.org/flux-operator/getting_started/user-guide.html#install)。

## MiniCluster 定义  {#miniCluster-definition}

由于 Flux MiniCluster 作为 [`batch/Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/)
运行，Kueue 不需要额外的组件来管理 Flux MiniCluster。
然而，需要注意以下方面：

### a. 队列选择

目标[本地队列](/zh-CN/docs/concepts/local_queue)应在 MiniCluster
配置的 `spec.jobLabels` 部分中指定。

```yaml
  jobLabels:
    kueue.x-k8s.io/queue-name: user-queue
```

### b. 配置资源需求

工作负载的资源需求可以在 MiniCluster 配置的 `spec.container[*].resources` 部分中配置。

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

有关在 Python 中执行此操作的等效说明，请参阅[运行 Python 作业](/zh-CN/docs/tasks/run/python_jobs/#flux-operator-job)。
