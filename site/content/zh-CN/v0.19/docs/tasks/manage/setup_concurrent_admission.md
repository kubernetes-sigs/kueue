---
title: "设置并发准入"
linkTitle: "并发准入"
date: 2026-05-05
weight: 8
description: >
  配置 Kueue，将已准入的工作负载迁移到更优先的 ResourceFlavor，并发运行按规格划分的准入检查。
---

{{< feature-state state="alpha" for_version="v0.18" >}}

本文介绍如何为 ClusterQueue 设置[并发准入](/v0.19/docs/concepts/concurrent_admission)。

## 准备工作 {#before-you-begin}

确保满足以下条件：

- Kubernetes 集群正在运行。
- `kubectl` 命令行工具可以访问集群。
- 已安装 0.18 或更高版本的 [Kueue](/v0.19/docs/installation)。

## 启用特性门控 {#enable-the-feature-gate}

`ConcurrentAdmission` 是 alpha 特性，默认关闭。

请按照[特性门控配置说明](/v0.19/docs/installation/#change-the-feature-gates-configuration)，
在 Kueue controller manager 中启用 `ConcurrentAdmission` 特性门控。

## 配置规格和队列 {#configure-flavors-and-queues}

创建一个定义了 `.spec.concurrentAdmissionPolicy` 的 `ClusterQueue`，
并在同一个 ResourceGroup 中配置多个 ResourceFlavor。规格顺序很重要：
请按照从最高优先级到最低优先级的顺序列出规格。

{{< include "v0.19/examples/admin/concurrent-admission-setup.yaml" "yaml" >}}

创建上述配置：

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/concurrent-admission-setup.yaml
```

使用此配置时，Kueue 可以为每个规格创建一个准入尝试：

- `reservation` 是最高优先级规格。
- `on-demand` 的优先级低于 `reservation`。
- `spot` 是最低优先级规格。

当前唯一支持的迁移模式是 `TryPreferredFlavors`。在该模式下，如果 Workload
先在 `spot` 上启动，说明 `reservation` 和 `on-demand` 当时没有准入该 Workload，
原因可能是配额不可用，也可能是所需的准入检查仍在等待。Kueue 会继续尝试
`reservation` 和 `on-demand`。如果之后某个更高优先级规格对应的 Variant 被准入，
Kueue 会将 Workload 迁移到该规格。

## 将迁移限制到最后可接受规格 {#limit-migration-to-a-last-acceptable-flavor}

如果希望将迁移限制到某个规格偏好阈值以上，请使用 `lastAcceptableFlavorName` API。
该字段用于定义 Workload 可以迁移到的最低可接受规格。

例如，使用以下策略时，Workload 可以从 `spot` 迁移到 `reservation`，
也可以从 `on-demand` 迁移到 `reservation`，但不能从 `spot` 迁移到 `on-demand`：

```yaml
concurrentAdmissionPolicy:
  migration:
    mode: TryPreferredFlavors
    constraints:
      lastAcceptableFlavorName: reservation
```

## 设置预留规格和同质规格 {#reservation-with-homogeneous-flavors-setup}

当你有一个优先的预留规格，以及多个优先级较低且同质的规格时，可以使用
`lastAcceptableFlavorName`。这样，如果预留规格可用，Workload 可以迁移到该规格；
但不会在同质的兜底规格之间迁移。

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

## 观察并发准入 {#observe-concurrent-admission}

向指向该 `ClusterQueue` 的 `LocalQueue` 提交工作负载。
当 `ClusterQueue` 启用并发准入时，Kueue 会将原始 Workload 标记为 Parent，
并创建由该 Parent 拥有的 Variant Workload。每个 Variant 都被限制到一个 ResourceFlavor。

列出 Workload：

```shell
kubectl get workloads
```

查看 Parent Workload：

```shell
kubectl describe workload WORKLOAD_NAME
```

通过 Parent 标签 `kueue.x-k8s.io/concurrent-admission-parent` 查找 Parent Workload：

```shell
kubectl get workloads -l kueue.x-k8s.io/concurrent-admission-parent=true
```

Parent Workload 的元数据可以如下所示：

```yaml
metadata:
  name: sample-job
  labels:
    kueue.x-k8s.io/concurrent-admission-parent: "true"
```

查看 Variant 元数据，并确认它通过 `ownerReferences` 引用 Parent Workload：

```shell
kubectl get workload VARIANT_WORKLOAD_NAME -o yaml
```

Variant Workload 的元数据可以如下所示：

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

## 接下来 {#whats-next}

- 阅读[并发准入概念](/v0.19/docs/concepts/concurrent_admission)。
- 阅读 [`ConcurrentAdmissionPolicy` API 参考](/v0.19/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-ConcurrentAdmissionPolicy)。
