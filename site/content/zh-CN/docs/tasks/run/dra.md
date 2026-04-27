---
title: "使用 DRA 设备运行工作负载"
linkTitle: "DRA"
date: 2026-03-22
weight: 7
description: >
  运行请求由 Kubernetes 动态资源分配 (DRA) 管理硬件设备的工作负载，并使用 Kueue 配额管理。
---

本页面展示如何运行请求硬件设备（如 GPU）的工作负载，这些设备由
[动态资源分配 (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
在已启用 Kueue 的 Kubernetes 集群中管理。示例使用批量 Job，但相同的做法适用于
Kueue 支持的任何[工作负载类型](/docs/concepts/workload)。

本页面的目标受众是[批量用户](/docs/tasks#batch-user)。

有关 Kueue 如何处理 DRA 资源的概念性详细信息，请参阅
[动态资源分配概念](/docs/concepts/dynamic_resource_allocation)。

## 准备工作

确保满足以下条件：

- 正在运行 Kubernetes 集群。
- kubectl 命令行工具可以与集群通信。
- [已安装 Kueue](/docs/installation)。
- 集群已[配置配额](/docs/tasks/manage/administer_cluster_quotas)，
  且 `ClusterQueue` 中包含 DRA 资源。
- 您的管理员已在 Kueue 中[配置 DRA 支持](/docs/tasks/manage/setup_dra)。

## 0. 识别您命名空间中可用的队列

运行以下命令列出您命名空间中可用的 `LocalQueue`。

```shell
kubectl -n default get localqueues
```

输出类似于：

```
NAME         CLUSTERQUEUE    PENDING WORKLOADS
user-queue   cluster-queue   0
```

[ClusterQueue](/docs/concepts/cluster_queue) 定义队列的配额。

## 1. 定义工作负载

使用 DRA 设备运行工作负载与[运行常规 Job](/docs/tasks/run/jobs) 类似。
您必须设置 `kueue.x-k8s.io/queue-name` 标签来选择要提交工作负载的 `LocalQueue`。

根据管理员配置集群的方式，有两种请求 DRA 设备的方式。请选择与您的设置匹配的方式。

### 使用 ResourceClaimTemplate

当您需要显式描述所需的设备时，请使用此方法。创建 `ResourceClaimTemplate` 并从工作负载中引用它：

{{< include "examples/dra/sample-dra-rct-job.yaml" "yaml" >}}

### 使用扩展资源

当集群中存在带有 `spec.extendedResourceName` 的 `DeviceClass` 时，请使用此方法。
您可以使用标准的 `resources.requests` 语法请求设备，就像请求 CPU 或内存一样。
无需 `ResourceClaimTemplate`：

{{< include "examples/dra/sample-dra-extended-resource-job.yaml" "yaml" >}}

如果您不确定使用哪种方法，请咨询您的管理员。

## 2. 运行工作负载

您可以使用以下命令运行工作负载。

对于基于 ResourceClaimTemplate 的工作负载：

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-rct-job.yaml
```

对于基于扩展资源的工作负载：

```shell
kubectl create -f https://kueue.sigs.k8s.io/examples/dra/sample-dra-extended-resource-job.yaml
```

在内部，Kueue 将为此 Job 创建相应的[工作负载](/docs/concepts/workload)。

## 3.（可选）监控工作负载状态

您可以使用以下命令查看工作负载状态：

```shell
kubectl -n default get workloads.kueue.x-k8s.io
```

要检查工作负载是否已 admission 并查看 DRA 资源核算：

```shell
kubectl -n default describe workload <workload-name>
```

查看 `Conditions` 部分了解 admission 状态，查看 `Events` 部分了解详细信息。
如果工作负载已获 admission，您可以验证 `status.admission.podSetAssignments[].resourceUsage` 字段中配额的资源使用情况：

```shell
kubectl -n default get workloads.kueue.x-k8s.io <workload-name> -o yaml
```

## 故障排除

### 工作负载未获 admission

如果工作负载保持 `Pending` 状态：

- 验证 `ClusterQueue` 有 DRA 资源的配额，且未被其他工作负载完全占用。
- 运行 `kubectl -n default describe workload <workload-name>` 并查看 Events 部分了解 admission 拒绝原因。

### 重复计数（扩展资源路径）

如果配额使用显示的值是预期的两倍（例如，单个 GPU 显示为 `2` 而不是 `1`），
则可能未启用 `DRAExtendedResources` 功能门控。
请管理员验证 [DRA 配置](/docs/tasks/manage/setup_dra)。

### 缺少 DeviceClass

对于扩展资源路径，`DeviceClass` 必须在提交工作负载之前存在。
如果它是在您的工作负载被拒绝之后创建的，则在另一个集群事件触发重新排队之前，
工作负载可能不会重新评估。删除并重新创建工作负载以强制重新评估。

有关一般故障排除，请参阅[故障排除指南](/docs/tasks/troubleshooting)。