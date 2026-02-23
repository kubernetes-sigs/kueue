---
title: "Topology"
date: 2026-01-29
weight: 6
description: >
  集群范围的资源，表示数据中心中节点的层次拓扑结构。
---

`Topology` 是一个集群范围的对象，定义了数据中心内节点的层次结构。
它通过提供一种使用节点标签表示组织单元（如区域、块和机架）层次结构的模型，
启用了[拓扑感知调度](/zh-cn/docs/concepts/topology_aware_scheduling)。

`Topology` 对象通过 [ResourceFlavor](/zh-cn/docs/concepts/resource_flavor)
中的 `.spec.topologyName` 字段引用，以将规约与特定的拓扑结构关联。

`Topology` 定义如下所示：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: Topology
metadata:
  name: "default"
spec:
  levels:
  - nodeLabel: "topology.kubernetes.io/zone"
  - nodeLabel: "cloud.provider.com/topology-block"
  - nodeLabel: "cloud.provider.com/topology-rack"
  - nodeLabel: "kubernetes.io/hostname"
```

## Topology 层级

`.spec.levels` 字段定义了拓扑层级的层次结构，从最高（最粗略）级别到最低（最细致）级别排序。
每个层级由一个节点标签标识，你的集群中的节点必须拥有这个标签。

例如，在典型的数据中心中：
- **区域层级**：区域或可用区，由类似 `topology.kubernetes.io/zone` 的标签标识
- **块层级**：机架组，由类似 `cloud.provider.com/topology-block` 的标签标识
- **机架层级**：块内的单独机架，由类似 `cloud.provider.com/topology-rack` 的标签标识
- **节点层级**：单独节点，通常由 `kubernetes.io/hostname` 标识

在同一拓扑域（例如，相同的机架）内运行的 Pod 相比不同域的 Pod 具有更好的网络带宽。

### 验证规则

`levels` 字段具有以下约束：

- **最小条目数**：1
- **最大条目数**：16
- **不可变性**：字段在创建后不能更改
- **唯一性**：每个层级必须有一个唯一的 `nodeLabel`
- **主机名限制**：`kubernetes.io/hostname` 标签只能用于最低（最后）层级

## 从 ResourceFlavor 引用 Topology

要启用拓扑感知调度，使用 `.spec.topologyName` 字段从 `ResourceFlavor
引用一个 `Topology`：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: "tas-flavor"
spec:
  nodeLabels:
    cloud.provider.com/node-group: "tas-group"
  topologyName: "default"
```

当 ResourceFlavor 引用 Topology 时：

- **至少需要一个 nodeLabel**：ResourceFlavor 必须在 `.spec.nodeLabels` 中至少有一个条目。
- **Spec 变为不可变**：一旦 ResourceFlavor 设置了 `topologyName`，整个 `.spec` 字段将不能被修改。

## 接下来是什么？

- 学习如何使用 [拓扑感知调度](/zh-cn/docs/concepts/topology_aware_scheduling)
- 阅读 `Topology` 的 [API 参考](/zh-cn/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-Topology)
