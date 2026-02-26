---
title: "使用 HAMi"
linkTitle: "HAMi vGPU"
date: 2025-12-15
weight: 6
description: >
  通过 Kueue 使用 HAMi vGPU 资源
---

此页面演示了如何将 Kueue 与
[HAMi](https://github.com/Project-HAMi/HAMi)（异构 AI 计算虚拟化中间件）结合使用，
以进行 vGPU 资源管理的工作负载运行。

当通过 HAMi 使用 vGPU 资源时，Pod 会请求每个 vGPU
的资源（`nvidia.com/gpumem`、`nvidia.com/gpucores`）以及
vGPU 的数量（`nvidia.com/gpu`）。对于配额管理，你需要跟踪所有 vGPU 实例的总资源消耗。

下面我们将演示如何在 Kueue
中使用[用于配额管理的资源转换](/docs/tasks/manage/administer_cluster_quotas/#transform-resources-for-quota-management)来支持这一点。

本指南适用于对 Kueue 有基本了解的[批处理用户](/docs/tasks#batch-user)。
有关更多信息，请参阅 [Kueue 概述](/docs/overview)。

## 在你开始之前

1. `Deployment` 集成默认启用。

2. 请按照[使用自定义配置的安装说明](/docs/installation#install-a-custom-configured-released-version)配置带有
   ResourceTransformation 的 Kueue。

3. 确保你的集群已安装 HAMi 并拥有可用的 vGPU 资源。
   HAMi 通过诸如 `nvidia.com/gpu`、`nvidia.com/gpucores`
   和 `nvidia.com/gpumem` 等资源提供 vGPU 资源管理。

## 将 HAMi 与 Kueue 结合使用

在 Kueue 上运行请求 vGPU 资源的 Pod 时，请考虑以下方面：

### a. 配置 ResourceTransformation

配置 Kueue 使用 ResourceTransformation 自动计算总的 vGPU 资源：

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
resources:
  transformations:
  - input: nvidia.com/gpucores
    strategy: Replace
    multiplyBy: nvidia.com/gpu
    outputs:
      nvidia.com/total-gpucores: "1"
  - input: nvidia.com/gpumem
    strategy: Replace
    multiplyBy: nvidia.com/gpu
    outputs:
      nvidia.com/total-gpumem: "1"
```

此配置告诉 Kueue 将每个 vGPU 资源值乘以 vGPU 实例的数量。
例如，如果一个 Pod 请求 `nvidia.com/gpu: 2` 和 `nvidia.com/gpumem: 1024`，
Kueue 将计算 `nvidia.com/total-gpumem: 2048`（1024 × 2）。

### b. 配置 ClusterQueue

配置你的 ClusterQueue 以跟踪 vGPU 实例计数和总资源：

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: vgpu-cluster-queue
spec:
  resourceGroups:
  - coveredResources: ["nvidia.com/gpu", "nvidia.com/total-gpucores", "nvidia.com/total-gpumem"]
    flavors:
    - name: default-flavor
      resources:
      - name: nvidia.com/gpu
        nominalQuota: 50
      - name: nvidia.com/total-gpucores
        nominalQuota: 1000
      - name: nvidia.com/total-gpumem
        nominalQuota: 10240
```

### c. 队列选择

目标 [本地队列](/zh-cn/docs/concepts/local_queue) 应当在 Pod
配置的 `metadata.labels` 部分指定。

```yaml
metadata:
  labels:
    kueue.x-k8s.io/queue-name: vgpu-queue
```

### d. 配置资源需求

工作负载的资源需求可以在 `spec.containers` 部分配置：

```yaml
resources:
  limits:
    nvidia.com/gpu: "2"         # Number of vGPU instances
    nvidia.com/gpucores: "20"   # Cores per vGPU
    nvidia.com/gpumem: "1024"   # Memory per vGPU (MiB)
```

Kueue 会自动将这些转换为总资源：
- `nvidia.com/total-gpucores: 40` （20 核心 × 2 vGPU）
- `nvidia.com/total-gpumem: 2048` （1024 MiB × 2 vGPU）

## 示例

这是一个示例：

{{< include "examples/serving-workloads/sample-hami.yaml" "yaml" >}}

你可以使用以下命令创建 vGPU Deployment：

```sh
kubectl create -f https://kueue.sigs.k8s.io/examples/serving-workloads/sample-hami.yaml
```

要检查 ClusterQueue 中的资源使用情况，查看 `status.flavorsReservation` 字段：

```shell
kubectl get clusterqueue hami-queue -o yaml
```

`status.flavorsReservation` 显示了 `nvidia.com/total-gpucores`
和 `nvidia.com/total-gpumem` 的当前资源消耗：

```yaml
status:
  flavorsReservation:
  - name: hami-flavor
    resources:
    - name: nvidia.com/total-gpucores
      total: "60"  # Current usage (30 cores × 2 vGPUs)
    - name: nvidia.com/total-gpumem
      total: "2048"  # Current usage (1024 MiB × 2 vGPUs)
```
