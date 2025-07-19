---
title: "拓扑感知调度"
date: 2024-04-11
weight: 6
description: >
  允许基于数据中心节点拓扑结构调度 Pod。
---

{{< feature-state state="alpha" for_version="v0.9" >}}

AI/ML 工作负载通常需要大量的 Pod 间通信。因此，运行 Pod
之间的网络带宽会直接影响工作负载的执行时间和运行成本。
Pod 之间的可用带宽取决于运行这些 Pod 的节点在数据中心中的分布。

我们观察到，数据中心的组织单元具有层次结构，如机架（rack）和区块（block），一个机架内有多个节点，
一个区块内有多个机架。运行在同一组织单元内的 Pod 之间的网络带宽优于运行在不同单元的 Pod。
我们认为，不同机架上的节点比同一机架内的节点距离更远。同理，不同区块的节点比同一区块内的节点距离更远。

本特性（称为拓扑感知调度，简称 TAS）引入了一种约定，用于表示[分层节点拓扑信息](#node-topology-information)，
并为 Kueue 管理员和用户提供一组 API，以利用这些信息优化 Pod 的放置。

### 节点拓扑信息 {#node-topology-information}

我们提出了一种轻量级模型，通过节点标签来表示数据中心内节点的层次结构。在该模型中，节点标签由云服务商设置，或由本地集群管理员手动设置。

此外，我们假设用于 TAS 的每个节点都带有一组标签，这些标签能唯一标识其在树状结构中的位置。
我们不要求每一层标签全局唯一，即可能存在两个节点具有相同的 "rack" 标签，但位于不同的 "block"。

例如，数据中心层次结构的表示如下：

|  节点  |  cloud.provider.com/topology-block | cloud.provider.com/topology-rack |
|:------:|:----------------------------------:|:--------------------------------:|
| node-1 |               block-1              |              rack-1              |
| node-2 |               block-1              |              rack-2              |
| node-3 |               block-2              |              rack-1              |
| node-4 |               block-2              |              rack-3              |

注意，node-1 和 node-3 虽然 "cloud.provider.com/topology-rack" 标签值相同，但位于不同的 block。

### 容量计算

对于每个 PodSet，TAS 通过以下方式确定每个拓扑域（如某个机架）的当前可用容量：
- 仅包括已就绪（`Ready=True` 条件）且可调度（`.spec.unschedulable=false`）节点的 Node 可分配容量（基于 `.status.allocatable` 字段），
- 减去所有其他已准入 TAS 工作负载的用量，
- 减去所有其他非 TAS Pod（主要由 DaemonSet 拥有，也包括静态 Pod、Deployment 等）的用量。

### 管理员 API

作为管理员，启用该特性需：
1. 确保已启用 `TopologyAwareScheduling` 特性门控（feature gate）
2. 至少创建一个 `Topology` API 实例
3. 通过 `.spec.topologyName` 字段在专用 ResourceFlavor 中引用 `Topology` API

#### 示例

{{< include "examples/tas/sample-queues.yaml" "yaml" >}}

### 用户 API

TAS 配置好并可用后，用户可在 PodTemplate 层级设置如下注解来创建 Job：
- `kueue.x-k8s.io/podset-preferred-topology` —— 表示 PodSet 需要拓扑感知调度，
  但所有 Pod 调度到同一拓扑域节点只是偏好而非强制。系统会自下而上逐级评估注解指定的层级。
  如果 PodSet 无法适配某一拓扑域，则考虑上一级拓扑。如果在最高层级仍无法适配，
  则会分布在多个拓扑域。
- `kueue.x-k8s.io/podset-required-topology` —— 表示 PodSet 需要拓扑感知调度，
  且要求所有 Pod 必须调度到注解值指定的拓扑层级（如同一机架或同一区块）内的节点。

#### 示例

以下是用户提交以使用 TAS 的 Job 示例。假设存在名为 `tas-user-queue` 的 LocalQueue，且其引用的 ClusterQueue 指向 TAS ResourceFlavor。

{{< include "examples/tas/sample-job-preferred.yaml" "yaml" >}}

### ClusterAutoscaler 支持

TAS 通过 [Provisioning AdmissionCheck](/docs/admission-check-controllers/provisioning/)
集成 [Kubernetes ClusterAutoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)。

当工作负载分配到带有 Provisioning AdmissionCheck 的 TAS ResourceFlavor 时，其准入流程如下：
1. **配额预留**：预留配额，Workload 获得 `QuotaReserved` 条件。如配置了抢占，则会评估抢占。
2. **准入检查**：Kueue 等待所有 AdmissionCheck（包括 Provisioning）在 Workload 的 `status.admissionChecks` 字段中报告 `Ready`。
3. **拓扑分配**：Kueue 在 Workload 对象上设置拓扑分配，计算时会考虑新扩容的节点。

另请参阅 [ProvisioningRequestConfig 中的 PodSet 更新](site/content/en/docs/admission-check-controllers/provisioning.md)，了解如何配置 Kueue 以限制调度到新扩容节点（前提是扩容类支持）。

### 热插拔（Hot swap）支持
{{% alert title="注意" color="primary" %}}
要启用此特性，需将 `TASFailedNodeReplacement`  [特性门控](https://kubernetes.io/zh-cn/docs/reference/command-line-tools-reference/feature-gates/)设置为 `true`，
且最低拓扑标签必须为 `kubernetes.io/hostname`。此特性自 Kueue 0.12 版本引入。
{{% /alert %}}

当拓扑最低层级为节点时，TAS 会为 Pod 到节点做固定分配，并注入 NodeSelector，
确保 Pod 调度到选定节点。但这意味着如果运行期间节点发生故障或被删除，
工作负载无法迁移到其他节点。为避免整个 TAS 工作负载的高成本重调度，引入了节点热插拔特性。

启用后，TAS 会尝试为所有受影响的工作负载找到故障或被删除节点的替代节点，
同时保持其他拓扑分配不变。目前仅支持单节点故障，多节点故障时工作负载会被驱逐。
若节点的 `conditions.Status.Ready` 至少 30 秒不为 `True`，
或节点被移除（从集群中消失），则视为节点故障。

注意，在原有域（如机架）内找到替代节点并非总是可行。因此，建议使用
[WaitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready/) 并配置 `waitForPodsReady.recoveryTimeout`，
以防止工作负载无限等待替换节点。

### 限制

目前，TAS 与其他特性兼容性存在如下限制：
- 某些调度指令（如 Pod 亲和性和反亲和性）会被忽略，
- 若底层 ClusterAutoscaler 无法扩容满足域约束的节点，则 "podset-required-topology" 注解可能失效，
- [MultiKueue](multikueue.md) 的 ClusterQueue 若引用带拓扑名（`.spec.topologyName`）的 ResourceFlavor，则会被标记为非活跃。

这些场景计划在 Kueue 后续版本中支持。

## 缺点

启用该特性后，Kueue 会开始跟踪系统中所有 Pod 和节点，这会导致 Kueue 占用更多内存。此外，Kueue 调度工作负载时需考虑拓扑信息，调度耗时也会增加。
