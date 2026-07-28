---
title: "并发准入（Concurrent Admission）"
linkTitle: "并发准入"
date: 2026-05-05
weight: 8
description: >
  将已准入的工作负载迁移到更优先的 ResourceFlavor，并发运行按规格划分的准入检查。
---

{{< feature-state state="alpha" for_version="v0.18" >}}

并发准入（Concurrent Admission）允许 Kueue 为同一个[工作负载](/v0.19/docs/concepts/workload)
保留多个准入尝试，每个准入尝试都被限制到不同的
[ResourceFlavor](/v0.19/docs/concepts/resource_flavor)。这样，工作负载可以先在已准入的规格上启动，
同时 Kueue 继续尝试更优先的规格。

并发准入包含两个主要组件：

- **事件驱动迁移：** 当更优先的 ResourceFlavor 可用时，Kueue 可以将正在运行的
  Workload 迁移到该规格。
- **并发多规格尝试：** 一个 Workload 可以同时独立尝试多个 ResourceFlavor。

Kueue 通过为原始 Workload 创建 Variant 来实现此功能。原始 Workload 称为 Parent Workload。
每个 Variant 都是 Parent Workload 的副本，并被分配到特定的 ResourceFlavor，因此这些 Variant 可以在各自的规格上并发且独立地尝试调度。
ClusterQueue 的资源组中 ResourceFlavor 的顺序决定规格偏好，列表中的第一个规格优先级最高。

当工作负载可以接受中断，并且你希望用额外调度开销换取更快放置、并发准入检查，
或迁移到更优先规格时，可以使用并发准入。例如，将工作负载迁移到预留资源规格。

## 规格偏好和迁移 {#flavor-preference-and-migration}

当前唯一支持的迁移模式是 `TryPreferredFlavors`。在该模式下，如果 Workload
先在较低优先级的规格上启动，Kueue 会继续尝试更高优先级规格对应的 Variant。
如果之后某个更高优先级规格对应的 Variant 被准入，Kueue 会将 Workload 迁移到该规格。

如果希望将迁移限制到某个规格偏好阈值以上，请使用 `lastAcceptableFlavorName` API。
该字段定义 Workload 可以迁移到的最低可接受规格。

例如，使用以下策略时，Workload 可以从 `spot` 迁移到 `reservation`，
也可以从 `on-demand` 迁移到 `reservation`，但不能从 `spot` 迁移到 `on-demand`：

```yaml
concurrentAdmissionPolicy:
  migration:
    mode: TryPreferredFlavors
    constraints:
      lastAcceptableFlavorName: reservation
```

在预留规格与同质兜底规格配合使用的场景中，`lastAcceptableFlavorName`
可以让 Workload 在预留规格可用时迁移到该规格，同时避免在同质兜底规格之间迁移。

例如，运行在 `zone-b` 上的 Workload 可以迁移到 `reservation`，但不会迁移到 `zone-a`：

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: cluster-queue
spec:
  namespaceSelector: {}
  concurrentAdmissionPolicy:
    migration:
      mode: TryPreferredFlavors
      constraints:
        lastAcceptableFlavorName: reservation
  resourceGroups:
  - coveredResources: ["cpu", "memory"]
    flavors:
    - name: reservation
      resources:
      - name: cpu
        nominalQuota: 4
      - name: memory
        nominalQuota: 16Gi
    - name: zone-a
      resources:
      - name: cpu
        nominalQuota: 8
      - name: memory
        nominalQuota: 32Gi
    - name: zone-b
      resources:
      - name: cpu
        nominalQuota: 8
      - name: memory
        nominalQuota: 32Gi
    - name: zone-c
      resources:
      - name: cpu
        nominalQuota: 8
      - name: memory
        nominalQuota: 32Gi
  admissionChecksStrategy:
    admissionChecks:
    - name: capacity-check
      onFlavors: [zone-a, zone-b, zone-c]
```

## Parent Workload 和 Variant Workload {#parent-and-variant-workloads}

当 `ClusterQueue` 启用并发准入时，Kueue 会将原始 Workload 标记为 Parent，
并创建由该 Parent 拥有的 Variant Workload。每个 Variant 都被限制到一个 ResourceFlavor。

Parent Workload 带有 `kueue.x-k8s.io/concurrent-admission-parent` 标签，标签值为 `"true"`。
Parent Workload 的元数据可以如下所示：

```yaml
metadata:
  name: sample-job
  labels:
    kueue.x-k8s.io/concurrent-admission-parent: "true"
```

Variant Workload 通过 `ownerReferences` 引用 Parent Workload。Variant Workload 的元数据可以如下所示：

```yaml
metadata:
  name: sample-job-variant-spot-a2342
  ownerReferences:
  - apiVersion: kueue.x-k8s.io/v1beta2
    kind: Workload
    name: sample-job
    uid: 7a9a0d5e-2c9c-4b3a-9c62-2b64a72f6a3f
    controller: true
    blockOwnerDeletion: true
```

Parent Workload 是作业集成观察准入状态的对象。Variant Workload 是内部准入尝试。
不要手动创建或编辑 Parent 标签，也不要手动编辑 Variant 注解。

## 约束 {#constraints}

并发准入当前有以下约束：

- 该特性可用于 `v1beta2` ClusterQueue API。
- 必须启用 `ConcurrentAdmission` 特性门控。
- 配置了 `.spec.concurrentAdmissionPolicy` 的 `ClusterQueue` 必须使用
  `BestEffortFIFO` 队列策略。不支持 `StrictFIFO`。
- 配置了 `.spec.concurrentAdmissionPolicy` 的 `ClusterQueue` 必须正好有一个
  `resourceGroup`。
- 该 `resourceGroup` 最多可以包含 16 个 ResourceFlavor。
- 创建 `ClusterQueue` 后，`concurrentAdmissionPolicy` 字段不可变。
- `TryPreferredFlavors` 是当前唯一支持的迁移模式。

## 接下来 {#whats-next}

- [设置并发准入](/v0.19/docs/tasks/manage/setup_concurrent_admission)。
- 了解 [ClusterQueue 规格顺序](/v0.19/docs/concepts/cluster_queue#resource-flavors-and-resources)。
- 阅读[工作负载概念](/v0.19/docs/concepts/workload)，了解 Parent Workload 和 Variant Workload。
- 阅读 [`ConcurrentAdmissionPolicy` API 参考](/v0.19/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-ConcurrentAdmissionPolicy)。
