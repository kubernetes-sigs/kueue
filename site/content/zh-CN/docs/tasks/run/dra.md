---
title: "使用 DRA 设备运行工作负载"
linkTitle: "DRA"
date: 2026-03-22
weight: 7
description: >
  使用由 Kubernetes 动态资源分配（DRA）和 Kueue 配额管理的硬件设备运行工作负载。
---
本页面向你展示如何在启用了 Kueue 的 Kubernetes 集群中运行请求由
[动态资源分配 (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
管理的硬件设备（比如 GPU）的工作负载。示例使用了 batch Job，但同样的方法适用于
[Kueue 支持的任何类型的工作负载](/zh-CN/docs/concepts/workload)。

本页面的目标受众是 [batch users](/zh-CN/docs/tasks#batch-user)。

有关 Kueue 如何处理 DRA 资源的概念性细节，请参考
[动态资源分配的概念](/docs/concepts/dynamic_resource_allocation).

## 开始之前

请确保满足以下条件：

- 一个正在运行的 Kubernetes 集群。
- kubectl 命令行工具可以与集群通信。
- [已安装 Kueue](/zh-CN/docs/installation)。
- 集群 [已配置配额](/zh-CN/docs/tasks/manage/administer_cluster_quotas)，并在 `ClusterQueue` 包含了 DRA 资源。
- 你的管理员已经 [在 Kueue 中设置了 DRA 的支持](/docs/tasks/manage/setup_dra)。

## 0. 确定命名空间中可用的队列

运行下面这个命令，列出你的 namespace 中可用的 `LocalQueues`。

```shell
kubectl -n default get localqueues
```

输出类似于下面这样：

```
NAME         CLUSTERQUEUE    PENDING WORKLOADS
user-queue   cluster-queue   0
```

[ClusterQueue](/zh-CN/docs/concepts/cluster_queue) 定义了队列的配额。

## 1. 定义工作负载

使用 DRA 设备运行工作负载与[运行一个常规 Job](/zh-CN/docs/tasks/run/jobs) 类似。
你必须设置 `kueue.x-k8s.io/queue-name` 标签以便选择要将工作负载提交到哪个 `LocalQueue`。

根据管理员配置集群方式的不同，有两种请求 DRA 设备的方法。请选择与你的设置相匹配的方法。

### 使用 ResourceClaimTemplate

当你需要显式描述所需的设备时，请使用此方法。
创建一个 `ResourceClaimTemplate` 并在工作负载中引用它：

{{< include "examples/dra/sample-dra-rct-job.yaml" "yaml" >}}

### 使用扩展资源

当集群中存在带有 `spec.extendedResourceName` 的 `DeviceClass` 时，请使用此方法。
你可以像请求 CPU 或内存一样，使用标准的 `resources.requests` 语法来请求设备，
无需 `ResourceClaimTemplate`：

{{< include "examples/dra/sample-dra-extended-resource-job.yaml" "yaml" >}}

### 使用可分区设备

如果你的管理员已配置
[基于计数器的配额](/docs/tasks/manage/setup_dra/#set-up-counter-based-quota-partitionable-devices)，
则你的工作负载将按设备的计数器值（例如 GPU 内存）而非设备数量计费。
你提交工作负载的方式与上面的 ResourceClaimTemplate 相同。

{{< include "examples/dra/sample-dra-counter-job.yaml" "yaml" >}}

### 使用可消耗容量（共享设备）

{{% alert title="Note" color="info" %}}
这个功能需要打开 `KueueDRAIntegrationConsumableCapacity` 开关，在 v0.19 中该功能默认处于禁用状态。
{{% /alert %}}

如果你的管理员已配置
[基于容量的配额](/docs/tasks/manage/setup_dra/#set-up-capacity-based-quota-consumable-capacity)，
则你的工作负载将按设备的容量消耗（例如 GPU 内存）而非设备数量计费。
你可以提交一个工作负载并在 `ResourceClaimTemplate` 的 `capacity.requests` 中指定所需的容量：

{{< include "examples/dra/sample-dra-capacity-job.yaml" "yaml" >}}

如果您省略 `capacity.requests`，Kueue 将按设备的 `RequestPolicy.Default` 或设备的完整容量计费。

如果你不确定使用哪种方法，请咨询你的管理员。

## 2. 运行工作负载

你可以使用以下命令运行工作负载。

对于基于 ResourceClaimTemplate 的工作负载：

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-rct-job.yaml
```

对于基于扩展资源的工作负载：

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-extended-resource-job.yaml
```

Kueue 将在内部为这个 Job 创建一个对应的 [Workload](/zh-CN/docs/concepts/workload)。

## 3.（可选）监控工作负载的状态

你可以使用以下命令查看 Workload 状态：

```shell
kubectl -n default get workloads.kueue.x-k8s.io
```

要检查工作负载是否被准入并查看 DRA 资源核算情况：

```shell
kubectl -n default describe workload <workload-name>
```

查看 `Conditions` 部分了解准入状态，查看 `Events` 部分了解详细信息。
如果工作负载已被准入，您可以在
`status.admission.podSetAssignments[].resourceUsage` 字段中验证为配额计费的资源：

```shell
kubectl -n default get workloads.kueue.x-k8s.io <workload-name> -o yaml
```

## 故障排除

### 工作负载不被准入

如果 Workload 一直处于 `Pending` 状态：

- 检查 `ClusterQueue` 是否有 DRA 资源的配额，且未被其他工作负载完全消耗。
- 运行 `kubectl -n default describe workload <workload-name>` 然后查看 Events 部分了解准入被拒绝的原因。

### 重复计数（扩展资源路径）

如果配额的使用量显示为预期值的两倍（例如，单个 GPU 显示为 `2` 而非 `1`），
请验证 `KueueDRAIntegrationExtendedResource` 是否未被显式禁用。
这个 gate 从 v0.19 起默认启用，可确保 Kueue 对由 DRA 支持的扩展资源仅计算一次，
而不是将其同时计为标准资源请求和 DRA 设备。

### 缺少 DeviceClass

对于扩展资源路径，`DeviceClass` 必须在你提交工作负载之前就存在。
如果它是在你的工作负载被拒绝之后才创建的，那么在另一个集群事件触发重新入队之前，该工作负载可能不会被重新评估。
请删除并重新创建工作负载以强制重新评估。


对于其他常见的故障排除，请参见
[故障排除指南](/zh-CN/docs/tasks/troubleshooting).
