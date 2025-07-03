---
title: "资源规格（Resource Flavor）"
date: 2022-03-14
weight: 2
description: >
  一种对象，用于定义集群中可用的计算资源，并通过将工作负载与特定节点类型关联，实现细粒度的资源管理。
---

集群中的资源通常不是同构的。资源可能在以下方面有所不同：

- 价格和可用性（例如，竞价型与按需型虚拟机）
- 架构（例如，x86 与 ARM CPU）
- 品牌和型号（例如，Radeon 7000、Nvidia A100、T4 GPU）

资源规格（ResourceFlavor）是一个表示这些资源差异的对象，并允许你通过标签、污点和容忍度将它们与集群节点关联。

{{% alert title="注意" color="primary" %}}
如果你的集群资源是同构的，你可以使用[空 ResourceFlavor](#empty-resourceflavor)，而无需为自定义 ResourceFlavor 添加标签。
{{% /alert %}}

## ResourceFlavor 容忍度实现自动调度 {#resource-flavor-tolerations}

**需要 Kubernetes 1.23 或更高版本**

这种方式适合希望自动将 Pod 调度到合适节点的团队。
但当集群中存在多种专用硬件（如两种不同的 nvidia.com/gpu 资源，即 T4 和 A100 GPU）时，会有一个限制：系统可能无法区分它们，Pod 可能会被调度到任意一种硬件上。

要将 ResourceFlavor 与集群中某一部分节点关联，可以在 `.spec.nodeLabels` 字段中配置能唯一标识这些节点的标签。
如果你使用 [cluster autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)（或类似控制器），请确保控制器在添加新节点时会添加这些标签。

为了保证 [Workload](/docs/concepts/workload) 中的 Pod 运行在 Kueue 选定的 flavor 所关联的节点上，Kueue 会执行以下步骤：

1. 在接纳 Workload 时，Kueue 会将 PodSpec 中的 [`.nodeSelector`](https://kubernetes.io/zh-cn/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector) 和 [`.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution`](https://kubernetes.io/zh-cn/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity) 字段与 ResourceFlavor 的标签进行匹配。
不匹配 Workload 节点亲和性的 `ResourceFlavors` 无法分配给 Workload 的 podSet。

2. 一旦 Workload 被接纳：
   - 如果 Workload 的 nodeSelector 中未包含 ResourceFlavor 的标签，Kueue 会将 ResourceFlavor 的标签添加到底层 Workload Pod 模板的 `.nodeSelector` 字段。
     例如，对于 [batch/v1.Job](https://kubernetes.io/zh-cn/docs/concepts/workloads/controllers/job/)，Kueue 会将标签添加到 `.spec.template.spec.nodeSelector` 字段。
     这样可以保证 Workload 的 Pod 只能调度到 flavor 所指定的节点上。

   - Kueue 会将容忍度添加到底层 Workload Pod 模板中。

     例如，对于 [batch/v1.Job](https://kubernetes.io/zh-cn/docs/concepts/workloads/controllers/job/)，Kueue 会将容忍度添加到 `.spec.template.spec.tolerations` 字段。
     这样可以让 Workload 的 Pod 调度到带有特定污点的节点上。

此类型的 ResourceFlavor 示例如下：

{{< include "examples/admin/resource-flavor-tolerations.yaml" "yaml" >}}

定义如上 ResourceFlavor 时，应设置以下值：
- `.metadata.name` 字段，用于在 [ClusterQueue](/docs/concepts/cluster_queue) 的 `.spec.resourceGroups[*].flavors[*].name` 字段中引用 ResourceFlavor。
- `spec.nodeLabels` 将 ResourceFlavor 与某个节点或节点子集关联。
- `spec.tolerations` 为需要 GPU 的 Pod 添加指定的容忍度。


## ResourceFlavor 污点实现用户选择性调度 {#resource-flavor-taints}

这种方式适合希望将 Workload 选择性调度到特定硬件类型的团队。
可以为每种专用硬件类型创建一个额外的 ResourceFlavor，并设置不同的污点和容忍度。
用户可以在 Workload 中添加相应的容忍度，将 Pod 调度到合适的节点上。

在 ResourceFlavor 层级添加污点，可以确保只有显式容忍该污点的工作负载才能消耗配额。

ResourceFlavor 上的污点与 [节点污点](https://kubernetes.io/zh-cn/docs/concepts/scheduling-eviction/taint-and-toleration/) 类似，
但只支持 `NoExecute` 和 `NoSchedule` 效果，`PreferNoSchedule` 会被忽略。

Kueue 要[接纳](/docs/concepts#admission) Workload 使用 ResourceFlavor，Workload 的 PodSpec 必须包含相应的容忍度。
另一方面，如果 ResourceFlavor 的 `.spec.tolerations` 字段也设置了匹配的容忍度，
则在[接纳](/docs/concepts#admission)期间不会考虑污点。
与 [ResourceFlavor 容忍度实现自动调度](#ResourceFlavor-容忍度实现自动调度)不同，
Kueue 不会为 flavor 污点自动添加容忍度。

此类型的 ResourceFlavor 示例如下：

{{< include "examples/admin/resource-flavor-taints.yaml" "yaml" >}}

定义如上 ResourceFlavor 时，应设置以下值：
- `.metadata.name` 字段，用于在 [ClusterQueue](/docs/concepts/cluster_queue) 的 `.spec.resourceGroups[*].flavors[*].name` 字段中引用 ResourceFlavor。
- `spec.nodeLabels` 将 ResourceFlavor 与某个节点或节点子集关联。
- `spec.nodeTaints` 限制 ResourceFlavor 的使用。
这些污点通常应与关联节点的污点一致。

## 空 ResourceFlavor {#empty-resourceflavor}

如果你的集群资源是同构的，或者你不需要为不同资源规格分别管理配额，可以创建一个不包含任何标签或污点的 ResourceFlavor。
这种 ResourceFlavor 称为空 ResourceFlavor，其定义如下：

{{< include "examples/admin/resource-flavor-empty.yaml" "yaml" >}}

## 下一步？

- 了解[集群队列（cluster queues）](/docs/concepts/cluster_queue)。
- 阅读 `ResourceFlavor` 的 [API 参考](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ResourceFlavor)。
