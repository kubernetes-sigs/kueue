---
title: "运行自定义封装工作负载"
linkTitle: "自定义工作负载"
date: 2025-01-14
weight: 1
description: 使用 AppWrapper 在 Kueue 上运行自定义工作负载。
---

本页展示了如何使用 [AppWrappers](https://project-codeflare.github.io/appwrapper/) 让
Kueue 的调度和资源管理能力可用于没有专用 Kueue 集成的工作负载类型。对于在定义中使用 `PodSpecTemplates` 的工作负载，
这比[构建自定义集成](/docs/tasks/dev/integrate_a_custom_job)更简单，
可以让 Kueue 支持自定义工作负载类型。

本指南适用于对 Kueue 有基本了解的[批量用户](/docs/tasks#batch-user)。更多信息请参见 [Kueue 概述](/docs/overview)。

## 开始之前

1. 请确保你使用的是 Kueue v0.11.0 或更高版本，以及 AppWrapper v1.0.2 或更高版本。

2. 按照[运行 AppWrappers](/docs/tasks/run/appwrappers/#before-you-begin)中的步骤，
了解如何启用和配置 `workload.codeflare.dev/appwrapper` 集成。

## 使用 LeaderWorkerSets 作为自定义工作负载的示例

我们以 [LeaderWorkerSets](https://github.com/kubernetes-sigs/lws) 为例，
说明如何在 AppWrapper 中运行自定义类型的工作负载。

1. 按照 [安装](https://github.com/kubernetes-sigs/lws/blob/main/docs/setup/install.md)
说明安装 LeaderWorkerSets。

2. 编辑 `appwrapper-manager-role` 的 `ClusterRole`，添加如下内容，
以允许 appwrapper 控制器操作 LeaderWorkerSets。
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
特别注意 `podSets` 数组中每个元素的 `replicas` 和 `path` 如何对应于 `template` 中的 `PodSpecTemplate` 和副本数。
这为 AppWrapper 控制器提供了足够的信息，使其能够“理解”被封装的资源，并为 Kueue 提供所需信息以进行管理。

{{< include "examples/appwrapper/leaderworkerset-sample.yaml" "yaml" >}}

