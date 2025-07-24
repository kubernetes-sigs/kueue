---
title: "运行包装过的自定义工作负载"
linkTitle: "Custom Workload"
date: 2025-01-14
weight: 1
description: >
  使用 AppWrapper 在 Kueue 上运行自定义工作负载。
---

本页面展示了如何使用 [AppWrapper](https://project-codeflare.github.io/appwrapper/)
使 Kueue 的调度和资源管理能力可用于没有专用 Kueue 集成的工作负载类型。
对于在其定义中使用 `PodSpecTemplates` 的工作负载，这提供了一种比[构建自定义集成](/zh-CN/docs/tasks/dev/integrate_a_custom_job)
更简便的方法来启用与自定义工作负载类型一起使用 Kueue。

本指南适用于对 Kueue 有基本了解的[批处理用户](/zh-CN/docs/tasks#batch-user)。
欲了解更多信息，请参阅[Kueue 概述](/zh-CN/docs/overview)。

## 开始之前

1. 确保你正在使用 Kueue v0.11.0 版本或更新版本以及 AppWrapper v1.0.2 或更新版本。

2. 遵循[运行 AppWrapper](/zh-CN/docs/tasks/run/appwrappers/#before-you-begin)
   中的步骤学习如何启用和配置 `workload.codeflare.dev/appwrapper` 集成。

## 使用 LeaderWorkerSet 作为自定义工作负载的示例  {#example-using-leaderworkersets-as-the-custom-workload}

我们使用 [LeaderWorkerSet](https://github.com/kubernetes-sigs/lws)
为例，来解释如何在 AppWrapper 内运行自定义类型的工作负载。

1. 按照[安装](https://github.com/kubernetes-sigs/lws/blob/main/docs/setup/install.md)
   说明进行 LeaderWorkerSet 的安装。

2. 编辑 `appwrapper-manager-role` `ClusterRole`，添加以下片段以允许
   Appwrapper 控制器操作 LeaderWorketSet。

```yaml
- apiGroups:
  - leaderworkerset.x-k8s.io
  resources:
  - leaderworkersets
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
```

3. 包含 LeaderWorkerSet 的 AppWrapper 如下所示。
   特别注意 `podSets` 数组的每个元素的 `replicas` 和 `path`
   如何对应于 `template` 中的 `PodSpecTemplate` 和副本计数。
   这为 AppWrapper 控制器提供了足够的信息，使其能够“理解”被包裹的资源，
   并提供 Kueue 管理该资源所需的信息。

{{< include "examples/appwrapper/leaderworkerset-sample.yaml" "yaml" >}}

