---
title: "准入公平分共享（Admission Fair Sharing）"
date: 2025-05-28
weight: 6
description: >
  一种基于源 LocalQueue 历史资源使用情况对工作负载进行排序的机制，优先考虑那些随着时间的推移消耗资源较少的工作负载。
---

{{< feature-state state="alpha" for_version="v0.12" >}}

{{% alert title="注意" color="primary" %}}
`AdmissionFairSharing` 目前是一个 Alpha 特性，默认未启用。

您可以通过编辑 `AdmissionFairSharing` 特性门控来启用它。有关特性门控配置的详细信息，
请查阅 [安装指南](/docs/installation/#change-the-feature-gates-configuration)。
{{% /alert %}}


# 准入公平共享（Admission Fair Sharing）{#admission-fair-sharing}

准入公平共享有助于在多个指向同一 ClusterQueue 的 LocalQueue 之间公平分配资源。
它会根据各自 LocalQueue 的历史资源使用情况对工作负载进行排序，
优先考虑那些随着时间推移消耗资源较少的工作负载。

## 工作原理

当多个工作负载在同一个 ClusterQueue 中竞争资源时：

1. Kueue 会跟踪每个 LocalQueue 的资源使用历史
2. 来自历史使用量较低的 LocalQueue 的工作负载会优先被接纳，高使用量队列的工作负载则靠后
3. 使用值会根据可配置参数随时间衰减

## 配置 {#configuration}

### Kueue 配置

可以在 Kueue 的配置文件中通过 `.admissionFairSharing` 配置以下参数：

- `usageHalfLifeDecayTime`：控制历史使用量衰减的速度
- `usageSamplingInterval`：资源使用量采样的频率
- `resourceWeights`：不同资源类型的重要性权重

#### 示例配置：{#example-configuration}

```
admissionFairSharing:
  usageHalfLifeTime: "168h"
  usageSamplingInterval: "5m"
  resourceWeights:
    cpu: 2.0 # cpu 使用量的重要性是内存的两倍
    memory: 1.0
```

### ClusterQueue 配置 {#cluster-queue-configuration}

通过在 ClusterQueue 中添加 AdmissionScope 来启用准入公平共享：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: sample-queue
spec:
  admissionScope:
    admissionMode: UsageBasedFairSharing
  resources:
    # ...现有资源配置...
```

### LocalQueue 配置 {#local-queue-configuration}

您可以在 LocalQueue 中定义 `fairSharing` 部分，以调整其在公平共享计算中的权重（默认为 `1`）：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  name: team-a-queue
  namespace: team-a
spec:
  clusterQueue: shared-queue
  fairSharing:
    weight: "2"  # 该队列会被视为只消耗了一半的资源
```

### 可观测性 {#observability}

您可以通过 LocalQueue 的 `status.FairSharing` 字段跟踪其历史资源使用情况，例如使用如下命令：
```
kubectl get lq user-queue -o jsonpath={.status.fairSharing}
```

输出类似于：

```
{"admissionFairSharingStatus":{"consumedResources":{"cpu":"31999m"},"lastUpdate":"2025-06-03T14:25:15Z"},"weightedShare":0}
```