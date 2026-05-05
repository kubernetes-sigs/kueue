---
title: "设置并发准入（Concurrent Admission）"
linkTitle: "并发准入"
date: 2026-05-05
weight: 8
description: >
  配置 Kueue 通过同时尝试多个资源规格来准入工作负载。
---

{{< feature-state state="alpha" for_version="v0.18" >}}

并发准入（Concurrent Admission）允许 Kueue 为同一个
[工作负载](/docs/concepts/workload)同时尝试多个
[资源规格](/docs/concepts/resource_flavor)。工作负载可以先在第一个成功准入的规格上启动。
同时，Kueue 仍可以继续尝试更优先的规格，并在更好的规格可用时迁移工作负载。

当工作负载可以接受中断，并且你希望用额外调度开销换取更快放置或迁移到更优先规格时，
可以使用并发准入。例如，将工作负载迁移到预留资源规格。

本文面向[批处理管理员](/docs/tasks#batch-administrator)。

## 准备工作 {#before-you-begin}

确保满足以下条件：

- Kubernetes 集群正在运行。
- `kubectl` 命令行工具可以访问集群。
- 已安装 0.18 或更高版本的 [Kueue](/docs/installation)。
- Kueue controller manager 已启用 `ConcurrentAdmission` 特性门控。

## 启用特性门控 {#enable-the-feature-gate}

`ConcurrentAdmission` 是 alpha 特性，默认关闭。

安装或重新配置 Kueue，并启用该特性门控：

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
featureGates:
  ConcurrentAdmission: true
```

有关修改 Kueue 配置的详细步骤，请参阅
[自定义配置安装说明](/docs/installation#install-a-custom-configured-released-version)。

## 配置规格和队列 {#configure-flavors-and-queues}

创建包含多个规格的 `ClusterQueue`，并启用 `.spec.concurrentAdmissionPolicy`。
所有规格必须位于同一个 `resourceGroup` 中。规格顺序很重要：请按照从最高优先级到
最低优先级的顺序列出规格。

{{< include "examples/admin/concurrent-admission-setup.yaml" "yaml" >}}

创建上述配置：

```shell
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/concurrent-admission-setup.yaml
```

使用此配置时，Kueue 可以为每个规格创建一个准入尝试：

- `reservation` 是最高优先级规格。
- `on-demand` 的优先级低于 `reservation`。
- `spot` 是最低优先级规格。

当前唯一支持的迁移模式是 `TryPreferredFlavors`。在该模式下，如果工作负载先在 `spot`
上启动，Kueue 会继续尝试 `reservation` 和 `on-demand`。如果之后更高优先级的规格被准入，
Kueue 会将工作负载迁移到该规格。

## 将迁移限制到最低优先规格 {#limit-migration-to-a-minimum-preferred-flavor}

当你只希望工作负载迁移到某个规格，或迁移到比该规格优先级更高的规格时，
可以使用 `minPreferredFlavorName`。

例如，以下策略允许工作负载迁移到 `reservation`，但不允许从 `spot` 迁移到 `on-demand`：

```yaml
concurrentAdmissionPolicy:
  migration:
    mode: TryPreferredFlavors
    constraints:
      minPreferredFlavorName: reservation
```

Kueue 会根据 `ClusterQueue` 中 `.spec.resourceGroups[*].flavors` 的顺序比较
`minPreferredFlavorName`。

## 观察并发准入 {#observe-concurrent-admission}

向指向该 `ClusterQueue` 的 `LocalQueue` 提交工作负载。
当 `ClusterQueue` 启用并发准入时，Kueue 会将原始 Workload 标记为父 Workload，
并创建由该父对象拥有的变体 Workload。每个变体都被限制到一个 ResourceFlavor。

列出 Workload：

```shell
kubectl get workloads
```

查看父 Workload：

```shell
kubectl describe workload WORKLOAD_NAME
```

查找某个父 Workload 的变体。可以查看 Workload 的 YAML，并寻找指向该父对象的
owner reference：

```shell
kubectl get workloads -o yaml
```

父 Workload 是作业集成观察准入状态的对象。变体 Workload 是内部准入尝试。
不要手动创建或编辑父对象标签，也不要手动编辑变体注解。

## 约束 {#constraints}

并发准入当前有以下约束：

- 该特性可用于 `v1beta2` ClusterQueue API。
- 必须启用 `ConcurrentAdmission` 特性门控。
- 配置了 `.spec.concurrentAdmissionPolicy` 的 `ClusterQueue` 必须正好有一个
  `resourceGroup`。
- 该 `resourceGroup` 最多可以包含 16 个 ResourceFlavor。
- 创建 `ClusterQueue` 后，`concurrentAdmissionPolicy` 字段不可变。
- `TryPreferredFlavors` 是当前唯一支持的迁移模式。

## 接下来 {#whats-next}

- 了解 [ClusterQueue 规格顺序](/docs/concepts/cluster_queue#resource-flavors-and-resources)。
- 阅读[工作负载概念](/docs/concepts/workload)，了解父 Workload 和变体 Workload。
- 阅读 [`ConcurrentAdmissionPolicy` API 参考](/docs/reference/kueue.v1beta2/#kueue-x-k8s-io-v1beta2-ConcurrentAdmissionPolicy)。
